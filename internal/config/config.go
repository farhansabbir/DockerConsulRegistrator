// Package config is what the registrator runs with - from its command line
// and its environment - checked before anything starts.
package config

import (
	"encoding/json"
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
	CheckAuto = "auto" // the container's health check as a TTL check when it has one, else TCP
	CheckTCP  = "tcp"  // TCP to the published port (no check for UDP)
	CheckTTL  = "ttl"  // a TTL check from the runtime: its health, or running
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

// Env is the environment variable of each flag that has one: a flag on the
// command line wins over its variable. Where Consul has a name of its own
// (CONSUL_HTTP_ADDR, CONSUL_CACERT...), it is that one.
var Env = map[string]string{
	"socket":               "REGISTRATOR_SOCKET",
	"runtime":              "REGISTRATOR_RUNTIME",
	"containerd-namespace": "REGISTRATOR_CONTAINERD_NAMESPACE",
	"label":                "REGISTRATOR_LABEL",
	"tags":                 "REGISTRATOR_TAGS",
	"address":              "REGISTRATOR_ADDRESS",
	"check":                "REGISTRATOR_CHECK",
	"check-interval":       "REGISTRATOR_CHECK_INTERVAL",
	"deregister-after":     "REGISTRATOR_DEREGISTER_AFTER",
	"resync":               "REGISTRATOR_RESYNC",
	"ignore":               "REGISTRATOR_DRY_RUN",
	"debug":                "REGISTRATOR_DEBUG",
	"agent":                "REGISTRATOR_AGENT",
	"consul":               "CONSUL_HTTP_ADDR",
	"token-file":           "CONSUL_HTTP_TOKEN_FILE",
	"ca-file":              "CONSUL_CACERT",
	"cert-file":            "CONSUL_CLIENT_CERT",
	"key-file":             "CONSUL_CLIENT_KEY",
	"domain":               "CONSUL_DOMAIN",
	"join":                 "CONSUL_RETRY_JOIN",
	"datacenter":           "CONSUL_DATACENTER",
	"agent-node":           "CONSUL_NODE_NAME",
	"agent-bind":           "CONSUL_BIND_ADDR",
	"encrypt-file":         "CONSUL_ENCRYPT_FILE",
	"agent-token-file":     "CONSUL_AGENT_TOKEN_FILE",
	"dns-token-file":       "CONSUL_DNS_TOKEN_FILE",
}

// Name is how an error names a setting: its variable and its flag.
func Name(flag string) string {
	if e, ok := Env[flag]; ok {
		return e + " (--" + flag + ")"
	}
	return "--" + flag
}

// Config holds the registrator's settings.
type Config struct {
	Socket              string // SocketAuto, a path, unix:///path or tcp://host:port
	Runtime             string // RuntimeAuto or the one behind Socket
	ContainerdNamespace string
	LabelKey            string // what marks what is registered: LabelKey=LabelValue
	LabelValue          string
	Consul              string // host:port, http://host:port or https://host:port
	Domain              string // the DNS domain Consul serves the services in

	TokenFile string // the registrator's ACL token, read from this file...
	Token     string // ...or given in CONSUL_HTTP_TOKEN. Secret: never logged.
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
	// AgentConfigFound is whether AgentConfigDirs hold configuration files:
	// then the agent is configured there, and need not be from here.
	AgentConfigFound bool

	// The bundled agent's settings, written into its configuration.
	Join        []string
	Datacenter  string
	LocalConfig string // more configuration, a JSON object (CONSUL_LOCAL_CONFIG)
	// Its secrets, from the environment or a file. Never logged.
	EncryptFile    string
	AgentTokenFile string
	DNSTokenFile   string
	Encrypt        string
	AgentToken     string
	DNSToken       string
}

// Default is the configuration with nothing given.
func Default() Config {
	return Config{
		Socket:              SocketAuto,
		Runtime:             RuntimeAuto,
		ContainerdNamespace: "default",
		LabelKey:            "consul",
		LabelValue:          "yes",
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

var (
	// namespaceRE is a containerd namespace, as containerd checks them.
	namespaceRE  = regexp.MustCompile(`^[A-Za-z0-9]+(?:[._-][A-Za-z0-9]+)*$`)
	labelKeyRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	labelValueRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	// datacenterRE is a Consul datacenter's name, as Consul checks them.
	datacenterRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ParseLabel reads --label: KEY=VALUE.
func ParseLabel(s string) (key, value string, err error) {
	key, value, ok := strings.Cut(s, "=")
	if !ok || !labelKeyRE.MatchString(key) || !labelValueRE.MatchString(value) {
		return "", "", fmt.Errorf("%q is not KEY=VALUE: a key of letters, digits, dots, slashes, hyphens and underscores, and a value of letters, digits, dots, hyphens and underscores", safe.Short(s, 80))
	}
	return key, value, nil
}

// Validate is everything wrong with c, or nil.
func (c *Config) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.Socket != SocketAuto {
		network, _, err := Endpoint(c.Socket)
		if err != nil {
			bad("%s: %v", Name("socket"), err)
		} else if network != "unix" && c.Runtime == RuntimeContainerd {
			bad("%s: containerd listens on a Unix socket only", Name("socket"))
		}
	}
	switch c.Runtime {
	case RuntimeAuto, RuntimeDocker, RuntimePodman, RuntimeContainerd:
	default:
		bad("%s: %q is not auto, docker, podman or containerd", Name("runtime"), safe.Short(c.Runtime, 20))
	}
	if !namespaceRE.MatchString(c.ContainerdNamespace) {
		bad("%s: %q is not a namespace name", Name("containerd-namespace"), safe.Short(c.ContainerdNamespace, 70))
	}
	if !labelKeyRE.MatchString(c.LabelKey) || !labelValueRE.MatchString(c.LabelValue) {
		bad("%s: %q is not KEY=VALUE", Name("label"), safe.Short(c.LabelKey+"="+c.LabelValue, 80))
	}
	if c.Domain != "" && !DomainOK(c.Domain) {
		bad("%s: %q is not a DNS domain: lowercase labels of letters, digits and hyphens, no dot at the end", Name("domain"), safe.Short(c.Domain, 80))
	}
	u, err := ConsulURL(c.Consul)
	if err != nil {
		bad("%s: %v", Name("consul"), err)
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		bad("%s and %s go together", Name("cert-file"), Name("key-file"))
	}
	if u != nil && u.Scheme != "https" && (c.CAFile != "" || c.CertFile != "") {
		bad("%s, %s and %s need an https:// %s", Name("ca-file"), Name("cert-file"), Name("key-file"), Name("consul"))
	}
	if c.Token != "" && c.TokenFile != "" {
		bad("CONSUL_HTTP_TOKEN and %s are both set: set one", Name("token-file"))
	}
	if len(c.Tags) > 64 {
		bad("%s: %d tags, 64 at most", Name("tags"), len(c.Tags))
	}
	for _, t := range c.Tags {
		if !safe.TagOK(t) {
			bad("%s: %q is not a tag: 1 to 255 printable ASCII characters, no space or comma", Name("tags"), safe.Short(t, 40))
		}
	}
	var v4, v6 int
	for _, s := range c.Addresses {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || a.IsLoopback() || a.Is4In6() {
			bad("%s: %q is not an address other hosts can reach", Name("address"), safe.Short(s, 60))
			continue
		}
		if a.Is4() {
			v4++
		} else {
			v6++
		}
	}
	if v4 > 1 || v6 > 1 {
		bad("%s: one IPv4 and one IPv6 address at most", Name("address"))
	}
	switch c.Check {
	case CheckAuto, CheckTCP, CheckTTL, CheckNone:
	default:
		bad("%s: %q is not auto, tcp, ttl or none", Name("check"), safe.Short(c.Check, 20))
	}
	if c.CheckInterval < time.Second || c.CheckInterval > time.Hour {
		bad("%s: %v is not between 1s and 1h", Name("check-interval"), c.CheckInterval)
	}
	if c.DeregisterAfter != 0 && (c.DeregisterAfter < time.Minute || c.DeregisterAfter > 7*24*time.Hour) {
		bad("%s: %v is not 0 or between 1m (Consul's least) and 168h", Name("deregister-after"), c.DeregisterAfter)
	}
	if c.Resync < 5*time.Second || c.Resync > 24*time.Hour {
		bad("%s: %v is not between 5s and 24h", Name("resync"), c.Resync)
	}
	if c.Agent {
		errs = append(errs, c.validateAgent(u)...)
	} else {
		for _, s := range []struct {
			flag string
			set  bool
		}{
			{"join", len(c.Join) > 0}, {"datacenter", c.Datacenter != ""}, {"agent-node", c.AgentNode != ""}, {"agent-bind", c.AgentBind != ""},
			{"encrypt-file", c.Encrypt != ""}, {"agent-token-file", c.AgentToken != ""}, {"dns-token-file", c.DNSToken != ""},
		} {
			if s.set {
				bad("%s configures the bundled Consul agent: it needs %s", Name(s.flag), Name("agent"))
			}
		}
		if c.LocalConfig != "" {
			bad("CONSUL_LOCAL_CONFIG configures the bundled Consul agent: it needs %s", Name("agent"))
		}
	}
	return errors.Join(errs...)
}

func (c *Config) validateAgent(u *url.URL) []error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	if !filepath.IsAbs(c.AgentBin) {
		bad("--agent-bin: %q is not an absolute path", safe.Short(c.AgentBin, 60))
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
		bad("%s: %q is not a node name: lowercase letters, digits and hyphens, 63 at most", Name("agent-node"), safe.Short(c.AgentNode, 70))
	}
	if c.AgentBind != "" {
		if a, err := netip.ParseAddr(c.AgentBind); err != nil || a.Zone() != "" || a.IsUnspecified() || a.IsMulticast() || a.IsLoopback() {
			bad("%s: %q is not an address other nodes can reach", Name("agent-bind"), safe.Short(c.AgentBind, 60))
		}
	}
	if u != nil && !loopback(u.Hostname()) {
		bad("%s: the bundled agent listens on this host; %q is not a loopback address", Name("consul"), safe.Short(c.Consul, 60))
	}
	for _, j := range c.Join {
		if j == "" || len(j) > 512 || safe.Arg(j) != nil {
			bad("%s: %q is not a server address", Name("join"), safe.Short(j, 60))
		}
	}
	if c.Datacenter != "" && !datacenterRE.MatchString(c.Datacenter) {
		bad("%s: %q is not a datacenter name: letters, digits, hyphens and underscores", Name("datacenter"), safe.Short(c.Datacenter, 70))
	}
	if c.LocalConfig != "" {
		var m map[string]any
		if len(c.LocalConfig) > 64<<10 || json.Unmarshal([]byte(c.LocalConfig), &m) != nil {
			bad("CONSUL_LOCAL_CONFIG is not a JSON object of agent configuration (64 KiB at most)")
		}
	}
	if !c.AgentConfigFound {
		// Configured from here only: what the agent cannot do without.
		if len(c.Join) == 0 {
			bad("%s is required: the Consul servers to join, comma-separated (or mount the agent's configuration in %s)",
				Name("join"), strings.Join(c.AgentConfigDirs, ", "))
		}
		registrator := c.Token != "" || c.TokenFile != ""
		if (c.AgentToken != "") != registrator {
			bad("with ACLs on, both CONSUL_AGENT_TOKEN and CONSUL_HTTP_TOKEN are needed (or their _FILE forms): the agent's token and the registrator's; with ACLs off, neither")
		}
		if c.DNSToken != "" && c.AgentToken == "" {
			bad("CONSUL_DNS_TOKEN is set, but CONSUL_AGENT_TOKEN is not: ACLs are on, and the agent needs its token")
		}
	}
	return errs
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

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

// ConsulURL parses the agent's address: host:port, or a URL with http or
// https and no path.
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
