// Package docker is the part of the Engine API the registrator reads - Docker's,
// and Podman's, which speaks it too:
// the daemon's info, containers, Swarm services and the event stream. It is
// a plain HTTP client over the daemon's socket - no Docker SDK.
//
// Docker's answers are read with a size limit, and the IDs the registrator
// puts in a URL are checked first.
package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/safe"
)

const (
	maxBody  = 16 << 20 // the largest answer read
	maxEvent = 1 << 20  // the longest event line
	// MinAPI is the oldest Engine API spoken: Docker 20.10.
	MinAPI = "1.41"
)

// ErrNotFound is a container or service that is not there (any more).
var ErrNotFound = errors.New("not found")

var (
	containerID = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
)

// Client talks to one Docker daemon.
type Client struct {
	hc      *http.Client // requests, with a timeout
	stream  *http.Client // the event stream, without
	base    string
	version string // "/v1.45" once Ping has run
}

// New returns a client for endpoint (unix:///path or tcp://host:port).
func New(endpoint string) (*Client, error) {
	network, addr, err := config.Endpoint(endpoint)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	base := "http://docker"
	if network == "tcp" {
		base = "http://" + addr
	}
	return &Client{
		hc:     &http.Client{Transport: tr, Timeout: 30 * time.Second},
		stream: &http.Client{Transport: tr},
		base:   base,
	}, nil
}

// Ping is what the daemon says about itself on /_ping.
type Ping struct {
	APIVersion    string // the Engine API version it speaks
	LibpodVersion string // Podman's own API version; "" on Docker
	Server        string // e.g. "Libpod/5.4.0 (linux)" or "Docker/26.1.5 (linux)"
}

// Ping asks the daemon what it is, and from then on speaks its API version:
// the fields the registrator reads are in every version since MinAPI.
func (c *Client) Ping(ctx context.Context) (Ping, error) {
	var p Ping
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/_ping", nil)
	if err != nil {
		return p, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return p, fmt.Errorf("docker: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p, fmt.Errorf("docker: /_ping answered %s", resp.Status)
	}
	p.APIVersion = resp.Header.Get("Api-Version")
	p.LibpodVersion = safe.Short(resp.Header.Get("Libpod-Api-Version"), 40)
	p.Server = safe.Short(resp.Header.Get("Server"), 80)
	if !versionOK(p.APIVersion) {
		return p, fmt.Errorf("docker: API version %q is not one (wanted %s or later)", safe.Short(p.APIVersion, 20), MinAPI)
	}
	if older(p.APIVersion, MinAPI) {
		return p, fmt.Errorf("docker: the daemon speaks API %s; %s or later is needed (Docker 20.10, Podman 3)", p.APIVersion, MinAPI)
	}
	c.version = "/v" + p.APIVersion
	return p, nil
}

var apiVersion = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}$`)

func versionOK(v string) bool { return apiVersion.MatchString(v) }

// older is whether API version a is older than b; both are versionOK.
func older(a, b string) bool {
	am, an, _ := strings.Cut(a, ".")
	bm, bn, _ := strings.Cut(b, ".")
	x, _ := strconv.Atoi(am)
	y, _ := strconv.Atoi(bm)
	if x != y {
		return x < y
	}
	x, _ = strconv.Atoi(an)
	y, _ = strconv.Atoi(bn)
	return x < y
}

// Info is the part of the daemon's /info the registrator reads.
type Info struct {
	Name          string // the host's name
	ServerVersion string
	Swarm         struct {
		NodeID           string
		NodeAddr         string
		LocalNodeState   string // "active" in a Swarm
		ControlAvailable bool   // a manager
		Cluster          *struct {
			ID string // the Swarm's; managers only
		}
	}
}

// Info returns the daemon's info.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var i Info
	err := c.get(ctx, "/info", nil, &i)
	return i, err
}

// Summary is a container as Docker lists it.
type Summary struct {
	ID     string `json:"Id"`
	Names  []string
	Labels map[string]string
	State  string
}

// Containers lists the running containers that carry label (a key, or
// key=value).
func (c *Client) Containers(ctx context.Context, label string) ([]Summary, error) {
	f, _ := json.Marshal(map[string][]string{"label": {label}})
	var out []Summary
	err := c.get(ctx, "/containers/json", url.Values{"filters": {string(f)}}, &out)
	return out, err
}

// Container is the part of a container's inspection the registrator reads.
type Container struct {
	ID    string `json:"Id"`
	Name  string
	State struct {
		Running    bool
		Paused     bool
		Restarting bool
		Health     *Health
	}
	Config struct {
		Labels map[string]string
	}
	NetworkSettings struct {
		Ports map[string][]PortBinding // "80/tcp" -> where it is published
	}
}

// Health is a container's HEALTHCHECK state.
type Health struct {
	Status string // starting, healthy or unhealthy
	Log    []struct {
		ExitCode int
		Output   string
	}
}

// PortBinding is one address and port a container port is published on.
type PortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string
}

// Inspect returns a container's details; ErrNotFound when it is gone.
func (c *Client) Inspect(ctx context.Context, id string) (Container, error) {
	var out Container
	if !containerID.MatchString(id) {
		return out, fmt.Errorf("docker: %q is not a container ID", safe.Short(id, 70))
	}
	err := c.get(ctx, "/containers/"+id+"/json", nil, &out)
	return out, err
}

// Service is the part of a Swarm service the registrator reads.
type Service struct {
	ID   string
	Spec struct {
		Name         string
		Labels       map[string]string
		TaskTemplate struct {
			ContainerSpec struct {
				Labels map[string]string
			}
		}
	}
	Endpoint struct {
		Ports []EndpointPort
	}
}

// EndpointPort is a port a Swarm service publishes.
type EndpointPort struct {
	Protocol      string // tcp, udp or sctp
	TargetPort    int
	PublishedPort int
	PublishMode   string // ingress (the routing mesh) or host
}

// Services returns the Swarm's services. Only managers can answer.
func (c *Client) Services(ctx context.Context) ([]Service, error) {
	var out []Service
	err := c.get(ctx, "/services", nil, &out)
	return out, err
}

// Event is one of Docker's events.
type Event struct {
	Type   string
	Action string
	Actor  struct {
		ID         string
		Attributes map[string]string
	}
}

// Events streams the daemon's events of the given types, calling opened
// once the daemon accepted the stream and onEvent for each event. It returns
// when the stream ends: an error, or nil when ctx is done.
func (c *Client) Events(ctx context.Context, types []string, opened func(), onEvent func(Event)) error {
	f, _ := json.Marshal(map[string][]string{"type": types})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url("/events", url.Values{"filters": {string(f)}}), nil)
	if err != nil {
		return err
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("docker: events: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return apiError("/events", resp)
	}
	opened()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxEvent)
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return fmt.Errorf("docker: events: an event is not JSON: %w", err)
		}
		onEvent(e)
	}
	if ctx.Err() != nil {
		return nil
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("docker: events: %w", err)
	}
	return errors.New("docker: the event stream ended")
}

func (c *Client) url(path string, q url.Values) string {
	u := c.base + c.version + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path, q), nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("docker: %s: %w", path, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return apiError(path, resp)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return fmt.Errorf("docker: %s: %w", path, err)
	}
	if len(b) > maxBody {
		return fmt.Errorf("docker: %s: the answer is larger than %d bytes", path, maxBody)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("docker: %s: the answer is not what Docker sends: %w", path, err)
	}
	return nil
}

// apiError is Docker's error message, cleaned: it can hold anything.
func apiError(path string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var m struct{ Message string }
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		msg = m.Message
	}
	return fmt.Errorf("docker: %s answered %s: %s", path, resp.Status, safe.Short(msg, 200))
}
