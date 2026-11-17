// Package consul is the part of the Consul agent's HTTP API the registrator
// uses: the agent's own details, its local services, registering and
// deregistering, and TTL check updates. It only ever talks to the local
// agent - the agent's anti-entropy keeps the servers' catalog in step.
package consul

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/safe"
)

const (
	maxBody      = 16 << 20
	maxTokenFile = 4096
)

// ErrDenied is Consul refusing the token (403).
var ErrDenied = errors.New("permission denied")

// ErrNotFound is a service or check the agent does not have (404).
var ErrNotFound = errors.New("not found")

// Options is how to reach the agent.
type Options struct {
	Addr      string // host:port or http(s)://host:port
	TokenFile string
	CAFile    string
	CertFile  string
	KeyFile   string
}

// Client talks to one Consul agent.
type Client struct {
	hc        *http.Client
	blocking  *http.Client // blocking queries: no timeout but their context's
	base      string
	tokenFile string

	mu    sync.Mutex
	token string
	stamp string // the token file's size and time when read
}

// New returns a client for the agent o names.
func New(o Options) (*Client, error) {
	u, err := config.ConsulURL(o.Addr)
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // the local agent, never through a proxy
	if u.Scheme == "https" {
		tc := &tls.Config{MinVersion: tls.VersionTLS12}
		if o.CAFile != "" {
			pem, err := readSmall(o.CAFile, 1<<20)
			if err != nil {
				return nil, fmt.Errorf("--ca-file: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("--ca-file: %s holds no PEM certificate", safe.Short(o.CAFile, 80))
			}
			tc.RootCAs = pool
		}
		if o.CertFile != "" {
			cert, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("--cert-file/--key-file: %s", safe.Short(err.Error(), 200))
			}
			tc.Certificates = []tls.Certificate{cert}
		}
		tr.TLSClientConfig = tc
	}
	c := &Client{hc: &http.Client{Transport: tr, Timeout: 15 * time.Second}, blocking: &http.Client{Transport: tr}, base: u.String(), tokenFile: o.TokenFile}
	if o.TokenFile != "" {
		if _, err := c.readToken(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// readToken is the ACL token, read again whenever its file changed - a
// token rotated in the file is used from the next request on.
func (c *Client) readToken() (string, error) {
	if c.tokenFile == "" {
		return "", nil
	}
	fi, err := os.Stat(c.tokenFile)
	if err != nil {
		return "", fmt.Errorf("--token-file: %w", err)
	}
	stamp := fmt.Sprint(fi.Size(), fi.ModTime().UnixNano())
	c.mu.Lock()
	defer c.mu.Unlock()
	if stamp == c.stamp && c.token != "" {
		return c.token, nil
	}
	b, err := readSmall(c.tokenFile, maxTokenFile)
	if err != nil {
		return "", fmt.Errorf("--token-file: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("--token-file: %s is empty", safe.Short(c.tokenFile, 80))
	}
	for i := 0; i < len(tok); i++ {
		if tok[i] <= ' ' || tok[i] > '~' {
			return "", fmt.Errorf("--token-file: %s holds more than one token, or characters no token has", safe.Short(c.tokenFile, 80))
		}
	}
	c.token, c.stamp = tok, stamp
	return tok, nil
}

// readSmall reads a regular file of at most max bytes.
func readSmall(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", safe.Short(path, 80))
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", safe.Short(path, 80), max)
	}
	return b, nil
}

// Self is the part of /v1/agent/self the registrator reads.
type Self struct {
	Config struct {
		NodeName   string
		Datacenter string
		Version    string
	}
	Member struct {
		Name string
		Addr string // the address the agent advertises
	}
	DebugConfig struct {
		DNSDomain    string // "consul." - the domain its DNS answers name things in
		DNSAltDomain string
	}
}

// Domains are the DNS domains the agent serves: its own (PTR answers name
// services in it) and its alternative, without the dot at the end.
func (s Self) Domains() (domain, alt string) {
	trim := func(d string) string { return strings.TrimSuffix(strings.ToLower(d), ".") }
	return trim(s.DebugConfig.DNSDomain), trim(s.DebugConfig.DNSAltDomain)
}

// Self returns the agent's own details.
func (c *Client) Self(ctx context.Context) (Self, error) {
	var s Self
	err := c.do(ctx, http.MethodGet, "/v1/agent/self", nil, &s)
	return s, err
}

// Service is a service the agent holds.
type Service struct {
	ID      string
	Service string
	Tags    []string
	Meta    map[string]string
	Address string
	Port    int
}

// Services returns the agent's local services by ID: those the token may
// read.
func (c *Client) Services(ctx context.Context) (map[string]Service, error) {
	out := map[string]Service{}
	err := c.do(ctx, http.MethodGet, "/v1/agent/services", nil, &out)
	return out, err
}

// Registration is a service as registered with the agent.
type Registration struct {
	ID                string
	Name              string
	Tags              []string          `json:",omitempty"`
	Address           string            `json:",omitempty"`
	Port              int               `json:",omitempty"`
	Meta              map[string]string `json:",omitempty"`
	EnableTagOverride bool
	Check             *Check `json:",omitempty"`
}

// Check is the service's one check. Its ID is "service:" and the service's.
type Check struct {
	Name                           string
	Notes                          string `json:",omitempty"`
	TCP                            string `json:",omitempty"`
	Interval                       string `json:",omitempty"`
	Timeout                        string `json:",omitempty"`
	TTL                            string `json:",omitempty"`
	Status                         string `json:",omitempty"`
	DeregisterCriticalServiceAfter string `json:",omitempty"`
}

// CheckID is the ID Consul gives the check of the service id.
func CheckID(serviceID string) string { return "service:" + serviceID }

// Register registers r with the agent, replacing what it held under r.ID and
// its checks.
func (c *Client) Register(ctx context.Context, r Registration) error {
	return c.do(ctx, http.MethodPut, "/v1/agent/service/register?replace-existing-checks=true", r, nil)
}

// Deregister removes the service id from the agent.
func (c *Client) Deregister(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPut, "/v1/agent/service/deregister/"+url.PathEscape(id), nil, nil)
}

// UpdateTTL sets a TTL check's status (passing, warning or critical) and
// output.
func (c *Client) UpdateTTL(ctx context.Context, checkID, status, output string) error {
	body := map[string]string{"Status": status, "Output": output}
	return c.do(ctx, http.MethodPut, "/v1/agent/check/update/"+url.PathEscape(checkID), body, nil)
}

// KVPair is a key of Consul's KV store and its value.
type KVPair struct {
	Key   string
	Value []byte
}

// KVList returns the keys under prefix, and the store's index for them.
// With index > 0 it is a blocking query: it waits up to wait for a change
// past index.
func (c *Client) KVList(ctx context.Context, prefix string, index uint64, wait time.Duration) ([]KVPair, uint64, error) {
	q := url.Values{"recurse": {"true"}}
	hc := c.hc
	if index > 0 {
		q.Set("index", strconv.FormatUint(index, 10))
		q.Set("wait", fmt.Sprintf("%ds", int(wait.Seconds())))
		hc = c.blocking
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, wait+wait/16+15*time.Second) // Consul adds up to wait/16
		defer cancel()
	}
	resp, err := c.request(ctx, hc, http.MethodGet, "/v1/kv/"+keyPath(prefix)+"?"+q.Encode(), nil, "")
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	idx, _ := strconv.ParseUint(resp.Header.Get("X-Consul-Index"), 10, 64)
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, idx, nil // nothing under prefix
	}
	if err := statusErr("/v1/kv", resp); err != nil {
		return nil, 0, err
	}
	var out []KVPair
	if err := readJSON("/v1/kv", resp, &out); err != nil {
		return nil, 0, err
	}
	return out, idx, nil
}

// KVPut sets key to value.
func (c *Client) KVPut(ctx context.Context, key string, value []byte) error {
	resp, err := c.request(ctx, c.hc, http.MethodPut, "/v1/kv/"+keyPath(key), bytes.NewReader(value), "application/octet-stream")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return statusErr("/v1/kv", resp)
}

// KVDelete removes key.
func (c *Client) KVDelete(ctx context.Context, key string) error {
	resp, err := c.request(ctx, c.hc, http.MethodDelete, "/v1/kv/"+keyPath(key), nil, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return statusErr("/v1/kv", resp)
}

// keyPath escapes each part of a KV key for a URL path.
func keyPath(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// request sends a request with the token; the caller reads and closes the
// answer, whatever its status.
func (c *Client) request(ctx context.Context, hc *http.Client, method, path string, body io.Reader, ctype string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	tok, err := c.readToken()
	if err != nil {
		return nil, err
	}
	if tok != "" {
		req.Header.Set("X-Consul-Token", tok)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("consul: %w", err)
	}
	return resp, nil
}

// statusErr is the error an answer that is not 200 stands for, its message
// cleaned: it can hold anything.
func statusErr(name string, resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := safe.Short(strings.TrimSpace(string(b)), 200)
	switch resp.StatusCode {
	case http.StatusForbidden:
		return fmt.Errorf("consul: %s: %w: %s", name, ErrDenied, msg)
	case http.StatusNotFound:
		return fmt.Errorf("consul: %s: %w: %s", name, ErrNotFound, msg)
	}
	return fmt.Errorf("consul: %s answered %s: %s", name, resp.Status, msg)
}

func readJSON(name string, resp *http.Response, out any) error {
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("consul: %s: %w", name, err)
	}
	if len(b) > maxBody {
		return fmt.Errorf("consul: %s: the answer is larger than %d bytes", name, maxBody)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("consul: %s: the answer is not what Consul sends: %w", name, err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	ctype := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, ctype = bytes.NewReader(b), "application/json"
	}
	resp, err := c.request(ctx, c.hc, method, path, body, ctype)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	name := strings.SplitN(path, "?", 2)[0]
	if err := statusErr(name, resp); err != nil {
		return err
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return nil
	}
	return readJSON(name, resp, out)
}
