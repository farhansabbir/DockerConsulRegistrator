// Package config is what the registrator runs with: the command line's
// values, checked.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/safe"
)

// The check modes.
const (
	CheckAuto = "auto" // the container's HEALTHCHECK as a TTL check when it has one, else TCP
	CheckTCP  = "tcp"  // TCP to the published port (no check for UDP)
	CheckTTL  = "ttl"  // a TTL check from Docker: its health, or running
	CheckNone = "none" // no check
)

// The runtimes --runtime takes.
const (
	RuntimeAuto       = "auto"
	RuntimeDocker     = "docker"
	RuntimePodman     = "podman"
	RuntimeContainerd = "containerd"
)

// SocketAuto is --socket's default: look for a runtime's socket where
// runtimes keep them.
const SocketAuto = "auto"

// Config holds the registrator's settings.
type Config struct {
	Socket              string // SocketAuto, a path, unix:///path or tcp://host:port
	Runtime             string // RuntimeAuto or the one behind Socket
	ContainerdNamespace string
	Consul              string // host:port, http://host:port or https://host:port
	Domain              string // the DNS domain Consul serves the services in

	TokenFile string // the Consul ACL token, read from a file only
	CAFile    string
	CertFile  string
	KeyFile   string

	Tags            []string
	Addresses       []string // registered instead of the host's: one IPv4, one IPv6
	Check           string
	CheckInterval   time.Duration
	DeregisterAfter time.Duration
	Resync          time.Duration
	Ignore          bool // dry run
	Debug           bool

	Agent           bool // start and supervise the bundled Consul agent
	AgentBin        string
	AgentConfigDirs []string
	AgentDataDir    string
	AgentNode       string
	AgentBind       string
}

// Default is the configuration with no flags given.
func Default() Config {
	return Config{
		Socket:              SocketAuto,
		Runtime:             RuntimeAuto,
		ContainerdNamespace: "default",
		Consul:              "127.0.0.1:8500",
		Check:               CheckAuto,
		CheckInterval:       10 * time.Second,
		DeregisterAfter:     10 * time.Minute,
		Resync:              time.Minute,
		AgentBin:            "/bin/consul",
		AgentConfigDirs:     []string{"/consul/config"},
		AgentDataDir:        "/consul/data",
	}
}

// Validate is what is wrong with c, or nil.
func (c *Config) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.Socket != SocketAuto {
		network, _, err := Endpoint(c.Socket)
		if err != nil {
			bad("--socket: %v", err)
		} else if network != "unix" && c.Runtime == RuntimeContainerd {
			bad("--socket: containerd listens on a Unix socket only")
		}
	}
	switch c.Runtime {
	case RuntimeAuto, RuntimeDocker, RuntimePodman, RuntimeContainerd:
	default:
		bad("--runtime: %q is not auto, docker, podman or containerd", safe.Short(c.Runtime, 20))
	}
	if !namespaceRE.MatchString(c.ContainerdNamespace) {
		bad("--containerd-namespace: %q is not a namespace name", safe.Short(c.ContainerdNamespace, 70))
	}
	if c.Domain != "" && !DomainOK(c.Domain) {
		bad("--domain: %q is not a DNS domain: lowercase labels of letters, digits and hyphens, no dot at the end", safe.Short(c.Domain, 80))
	}
	u, err := ConsulURL(c.Consul)
	if err != nil {
		bad("--consul: %v", err)
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		bad("--cert-file and --key-file go together")
	}
	if u != nil && u.Scheme != "https" && (c.CAFile != "" || c.CertFile != "") {
		bad("--ca-file, --cert-file and --key-file need an https:// --consul")
	}
	if len(c.Tags) > 64 {
		bad("--tags: %d tags, 64 at most", len(c.Tags))
	}
	for _, t := range c.Tags {
		if !safe.TagOK(t) {
			bad("--tags: %q is not a tag: 1 to 255 printable ASCII characters, no space or comma", safe.Short(t, 40))
		}
	}
	var v4, v6 int
	for _, s := range c.Addresses {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || a.IsLoopback() || a.Is4In6() {
			bad("--address: %q is not an address other hosts can reach", safe.Short(s, 60))
			continue
		}
		if a.Is4() {
			v4++
		} else {
			v6++
		}
	}
	if v4 > 1 || v6 > 1 {
		bad("--address: one IPv4 and one IPv6 address at most")
	}
	switch c.Check {
	case CheckAuto, CheckTCP, CheckTTL, CheckNone:
	default:
		bad("--check: %q is not auto, tcp, ttl or none", safe.Short(c.Check, 20))
	}
	if c.CheckInterval < time.Second || c.CheckInterval > time.Hour {
		bad("--check-interval: %v is not between 1s and 1h", c.CheckInterval)
	}
	if c.DeregisterAfter != 0 && (c.DeregisterAfter < time.Minute || c.DeregisterAfter > 7*24*time.Hour) {
		bad("--deregister-after: %v is not 0 or between 1m (Consul's least) and 168h", c.DeregisterAfter)
	}
	if c.Resync < 5*time.Second || c.Resync > 24*time.Hour {
		bad("--resync: %v is not between 5s and 24h", c.Resync)
	}
	if c.Agent {
		if !filepath.IsAbs(c.AgentBin) {
			bad("--agent-bin: %q is not an absolute path", safe.Short(c.AgentBin, 60))
		}
		if len(c.AgentConfigDirs) == 0 {
			bad("--agent-config-dir: none given")
		}
		for _, d := range c.AgentConfigDirs {
			if !filepath.IsAbs(d) {
				bad("--agent-config-dir: %q is not an absolute path", safe.Short(d, 60))
			}
		}
		if !filepath.IsAbs(c.AgentDataDir) {
			bad("--agent-data-dir: %q is not an absolute path", safe.Short(c.AgentDataDir, 60))
		}
		if c.AgentNode != "" && safe.Label(c.AgentNode) != c.AgentNode {
			bad("--agent-node: %q is not a node name: lowercase letters, digits and hyphens, 63 at most", safe.Short(c.AgentNode, 70))
		}
		if c.AgentBind != "" {
			if a, err := netip.ParseAddr(c.AgentBind); err != nil || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || a.IsLoopback() {
				bad("--agent-bind: %q is not an address other nodes can reach", safe.Short(c.AgentBind, 60))
			}
		}
		if u != nil && !loopback(u.Hostname()) {
			bad("--agent: the bundled agent listens on this host; --consul %s is not a loopback address", safe.Short(c.Consul, 60))
		}
	} else if c.AgentNode != "" || c.AgentBind != "" {
		bad("--agent-node and --agent-bind need --agent")
	}
	return errors.Join(errs...)
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

// namespaceRE is a containerd namespace, as containerd checks them.
var namespaceRE = regexp.MustCompile(`^[A-Za-z0-9]+(?:[._-][A-Za-z0-9]+)*$`)

// DomainOK is whether s can be the DNS domain Consul serves: a lowercase
// host name, no dot at the end.
func DomainOK(s string) bool {
	return safe.HostnameOK(s) && strings.ToLower(s) == s
}

// Endpoint parses a runtime's socket: the network ("unix" or "tcp") and
// address to dial. A bare absolute path is a Unix socket.
func Endpoint(s string) (network, addr string, err error) {
	if filepath.IsAbs(s) {
		return "unix", filepath.Clean(s), nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", "", fmt.Errorf("%q is not a path, unix:///path or tcp://host:port", safe.Short(s, 80))
	}
	switch {
	case u.Scheme == "unix" && u.Host == "" && filepath.IsAbs(u.Path) && u.RawQuery == "" && u.User == nil:
		return "unix", u.Path, nil
	case u.Scheme == "tcp" && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.User == nil:
		if err := hostPort(u.Host); err != nil {
			return "", "", err
		}
		return "tcp", u.Host, nil
	}
	return "", "", fmt.Errorf("%q is not a path, unix:///path or tcp://host:port", safe.Short(s, 80))
}

// ConsulURL parses --consul: host:port, or a URL with http or https and no
// path.
func ConsulURL(s string) (*url.URL, error) {
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("%q is not host:port, http://host:port or https://host:port", safe.Short(s, 80))
	}
	if err := hostPort(u.Host); err != nil {
		return nil, err
	}
	u.Path = ""
	return u, nil
}

func hostPort(hp string) error {
	host, port, err := net.SplitHostPort(hp)
	if err != nil || host == "" {
		return fmt.Errorf("%q is not host:port", safe.Short(hp, 80))
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%q: the port is not 1 to 65535", safe.Short(hp, 80))
	}
	if _, err := netip.ParseAddr(host); err != nil && !safe.HostnameOK(host) {
		return fmt.Errorf("%q is not a host name or address", safe.Short(host, 80))
	}
	return nil
}

// String is c for the start-up log line. It names the token file, never the
// token.
func (c Config) String() string {
	var b strings.Builder
	add := func(k string, v any) { fmt.Fprintf(&b, " %s=%v", k, v) }
	add("socket", c.Socket)
	add("runtime", c.Runtime)
	add("consul", c.Consul)
	if c.Domain != "" {
		add("domain", c.Domain)
	}
	if c.TokenFile != "" {
		add("token_file", c.TokenFile)
	}
	add("tags", strings.Join(c.Tags, ","))
	if len(c.Addresses) > 0 {
		add("address", strings.Join(c.Addresses, ","))
	}
	add("check", c.Check)
	add("check_interval", c.CheckInterval)
	add("deregister_after", c.DeregisterAfter)
	add("resync", c.Resync)
	add("dry_run", c.Ignore)
	add("agent", c.Agent)
	return strings.TrimSpace(b.String())
}
