// Package cli is the registrator's command line: its flags, its help, and
// putting the pieces together - the runtime, the Consul agent (bundled or
// not), the DNS domain, and the registrator.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/agent"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/consul"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/detect"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/hostnet"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/logx"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/registrator"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/safe"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/secret"
)

// Name is the command's name.
const Name = "docker-consul-registrator"

const help = `Usage: docker-consul-registrator [options]

Registers the published ports of the running containers carrying a label
(consul=yes) - and the tasks of Swarm services carrying it - with the
local Consul agent, one service per port and address family, so Consul's
DNS answers A, AAAA, SRV and PTR for them; and deregisters them when they
stop. It touches only the services it registered.

Every option can be given in its environment variable instead [shown in
brackets]; the command line wins. Secrets come from the environment or a
file only. Before anything starts, everything is checked; what is missing
or wrong is listed, and nothing starts.

The bundled Consul agent (the image starts it: --agent):
      --join ADDR,...           the Consul servers to join - required, unless
                                the agent's configuration is mounted in
                                /consul/config [CONSUL_RETRY_JOIN]
      --datacenter NAME         the servers' datacenter (default dc1)
                                [CONSUL_DATACENTER]
      --domain DOMAIN           the DNS domain to serve the services in, e.g.
                                dmarts.app (default consul) [CONSUL_DOMAIN]
      --agent-node NAME         its node name (default: the host's name)
                                [CONSUL_NODE_NAME]
      --agent-bind IP           its address (default: the host's Swarm
                                address, else the default route's)
                                [CONSUL_BIND_ADDR]
      --agent                   start the agent in this container, register
                                with it, stop it on the way out
                                [REGISTRATOR_AGENT]
      --agent-config-dir DIR    more of its configuration (repeatable; default
                                /consul/config)
      --agent-data-dir DIR      its state - keep it on a volume (default
                                /consul/data)
      --agent-bin PATH          (default /bin/consul)

Secrets - each from its variable, or from the file its _FILE variable names:
  CONSUL_ENCRYPT             the cluster's gossip key
  CONSUL_AGENT_TOKEN         the agent's ACL token          \  with ACLs on,
  CONSUL_HTTP_TOKEN          the registrator's ACL token    /  both needed
  CONSUL_DNS_TOKEN           the token anonymous DNS reads with
  CONSUL_LOCAL_CONFIG        more agent configuration, a JSON object (TLS...)
On the command line, as files only: --encrypt-file FILE, --agent-token-file
FILE, --dns-token-file FILE, --token-file FILE.

Containers:
      --label KEY=VALUE         what marks a container, or a Swarm service, to
                                register (default consul=yes)
                                [REGISTRATOR_LABEL]
  -s, --socket PATH|URL         the runtime's socket: a path, unix:///path or
                                tcp://host:port (default auto: the usual paths
                                of Docker's, Podman's and containerd's)
                                [REGISTRATOR_SOCKET]
      --runtime NAME            auto, docker, podman or containerd (default
                                auto: known by how the socket answers)
                                [REGISTRATOR_RUNTIME]
      --containerd-namespace NS containerd's namespace (default "default")
                                [REGISTRATOR_CONTAINERD_NAMESPACE]

Services:
  -t, --tags TAG,...            Consul tags for every service; the protocol is
                                always added [REGISTRATOR_TAGS]
      --address IP              the host address to register: one IPv4, one
                                IPv6 (default: the agent's address and the IPv6
                                address beside it) [REGISTRATOR_ADDRESS]
      --check MODE              auto, tcp, ttl or none (default auto: the
                                container's own health check where it has one,
                                else TCP to the port) [REGISTRATOR_CHECK]
      --check-interval DUR      how often checks run (default 10s)
                                [REGISTRATOR_CHECK_INTERVAL]
      --deregister-after DUR    Consul removes a service critical this long, a
                                safety net (default 10m; 0 never; at least 1m)
                                [REGISTRATOR_DEREGISTER_AFTER]
      --resync DUR              compare everything this often, besides on every
                                change (default 1m) [REGISTRATOR_RESYNC]
  -i, --ignore                  dry run: log what would change, change nothing
                                [REGISTRATOR_DRY_RUN]

The Consul agent's HTTP API (another agent than the bundled one: no --agent):
  -c, --consul ADDR             host:port, http://host:port or https://host:port
                                (default 127.0.0.1:8500) [CONSUL_HTTP_ADDR]
      --token-file FILE         the registrator's ACL token, read from FILE
                                [CONSUL_HTTP_TOKEN_FILE]
      --ca-file FILE            CA certificate for https [CONSUL_CACERT]
      --cert-file FILE          client certificate for https (with --key-file)
                                [CONSUL_CLIENT_CERT]
      --key-file FILE           client key for https [CONSUL_CLIENT_KEY]

Other:
      --debug                   log every event and decision
                                [REGISTRATOR_DEBUG]
  -v, --version                 print the version
  -h, --help                    print this help
`

// Main runs the command and returns its exit status: 0, 1 when it failed, 2
// when it was not given what it needs.
func Main(args []string, version string, stdout, stderr io.Writer) int {
	cfg, code, done := Parse(args, os.LookupEnv, version, stdout, stderr)
	if done {
		return code
	}
	log := logx.New(stdout, "registrator")
	log.SetDebug(cfg.Debug)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	if err := run(ctx, cfg, version, log, hup); err != nil {
		log.Error("stopped", "err", err)
		return 1
	}
	log.OK("stopped")
	return 0
}

// Parse reads the command line and the environment (env), and checks
// everything. done says the command is over - help, version, or not given
// what it needs - with code its exit status.
func Parse(args []string, env secret.Lookup, version string, stdout, stderr io.Writer) (cfg config.Config, code int, done bool) {
	refuse := func(problems []string) (config.Config, int, bool) {
		_, _ = fmt.Fprintf(stderr, "%s: cannot start:\n", Name)
		for _, p := range problems {
			_, _ = fmt.Fprintf(stderr, "  - %s\n", safe.Text(p))
		}
		_, _ = fmt.Fprintf(stderr, "Run '%s --help' for every setting.\n", Name)
		return cfg, 2, true
	}
	usage := func(format string, a ...any) (config.Config, int, bool) {
		return refuse([]string{fmt.Sprintf(format, a...)})
	}
	for i, a := range args {
		if err := safe.Arg(a); err != nil {
			return usage("argument %d %v; none of the options takes one like it", i+1, err)
		}
	}
	cfg = config.Default()
	fs := pflag.NewFlagSet(Name, pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	label := cfg.LabelKey + "=" + cfg.LabelValue
	fs.StringVarP(&cfg.Socket, "socket", "s", cfg.Socket, "")
	fs.StringVar(&cfg.Runtime, "runtime", cfg.Runtime, "")
	fs.StringVar(&cfg.ContainerdNamespace, "containerd-namespace", cfg.ContainerdNamespace, "")
	fs.StringVar(&label, "label", label, "")
	fs.StringVarP(&cfg.Consul, "consul", "c", cfg.Consul, "")
	fs.StringVar(&cfg.TokenFile, "token-file", "", "")
	fs.StringVar(&cfg.CAFile, "ca-file", "", "")
	fs.StringVar(&cfg.CertFile, "cert-file", "", "")
	fs.StringVar(&cfg.KeyFile, "key-file", "", "")
	fs.StringVar(&cfg.Domain, "domain", "", "")
	fs.StringSliceVarP(&cfg.Tags, "tags", "t", nil, "")
	fs.StringSliceVar(&cfg.Addresses, "address", nil, "")
	fs.StringVar(&cfg.Check, "check", cfg.Check, "")
	fs.DurationVar(&cfg.CheckInterval, "check-interval", cfg.CheckInterval, "")
	fs.DurationVar(&cfg.DeregisterAfter, "deregister-after", cfg.DeregisterAfter, "")
	fs.DurationVar(&cfg.Resync, "resync", cfg.Resync, "")
	fs.BoolVarP(&cfg.Ignore, "ignore", "i", false, "")
	fs.BoolVar(&cfg.Agent, "agent", false, "")
	fs.StringVar(&cfg.AgentBin, "agent-bin", cfg.AgentBin, "")
	fs.StringArrayVar(&cfg.AgentConfigDirs, "agent-config-dir", cfg.AgentConfigDirs, "")
	fs.StringVar(&cfg.AgentDataDir, "agent-data-dir", cfg.AgentDataDir, "")
	fs.StringVar(&cfg.AgentNode, "agent-node", "", "")
	fs.StringVar(&cfg.AgentBind, "agent-bind", "", "")
	fs.StringSliceVar(&cfg.Join, "join", nil, "")
	fs.StringVar(&cfg.Datacenter, "datacenter", "", "")
	fs.StringVar(&cfg.EncryptFile, "encrypt-file", "", "")
	fs.StringVar(&cfg.AgentTokenFile, "agent-token-file", "", "")
	fs.StringVar(&cfg.DNSTokenFile, "dns-token-file", "", "")
	fs.BoolVar(&cfg.Debug, "debug", false, "")
	showVersion := fs.BoolP("version", "v", false, "")
	showHelp := fs.BoolP("help", "h", false, "")
	if err := fs.Parse(args); err != nil {
		return usage("%v", err)
	}
	switch {
	case *showHelp:
		_, _ = fmt.Fprint(stdout, help)
		return cfg, 0, true
	case *showVersion:
		_, _ = fmt.Fprintf(stdout, "%s %s\n", Name, version)
		return cfg, 0, true
	case fs.NArg() > 0:
		return usage("it takes no arguments, only options (got %q)", fs.Arg(0))
	}

	// What the command line left unset, the environment may set.
	var problems []string
	flags := make([]string, 0, len(config.Env))
	for f := range config.Env {
		flags = append(flags, f)
	}
	sort.Strings(flags)
	for _, f := range flags {
		v, ok := env(config.Env[f])
		if !ok || v == "" || fs.Changed(f) {
			continue
		}
		if err := safe.Arg(v); err != nil {
			problems = append(problems, fmt.Sprintf("%s %v", config.Env[f], err))
			continue
		}
		if err := fs.Set(f, v); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", config.Name(f), err))
		}
	}
	if k, v, err := config.ParseLabel(label); err != nil {
		problems = append(problems, fmt.Sprintf("%s: %v", config.Name("label"), err))
	} else {
		cfg.LabelKey, cfg.LabelValue = k, v
	}

	// The secrets: from the environment, or the files named.
	var err error
	read := func(dst *string, name, file string, kind secret.Kind) {
		if *dst, err = secret.From(env, name, file, kind); err != nil {
			problems = append(problems, err.Error())
		}
	}
	read(&cfg.Encrypt, "CONSUL_ENCRYPT", cfg.EncryptFile, secret.GossipKey)
	read(&cfg.AgentToken, "CONSUL_AGENT_TOKEN", cfg.AgentTokenFile, secret.Token)
	read(&cfg.DNSToken, "CONSUL_DNS_TOKEN", cfg.DNSTokenFile, secret.Token)
	if v, ok := env("CONSUL_HTTP_TOKEN"); ok && v != "" {
		if err := secret.Check(v, secret.Token); err != nil {
			problems = append(problems, "CONSUL_HTTP_TOKEN "+err.Error())
		} else {
			cfg.Token = v
		}
	}
	if cfg.TokenFile != "" {
		if _, err := secret.Read(cfg.TokenFile, secret.Token); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", config.Name("token-file"), err))
		}
	}
	if v, ok := env("CONSUL_LOCAL_CONFIG"); ok {
		cfg.LocalConfig = v
	}
	cfg.AgentConfigFound = cfg.Agent && configFiles(cfg.AgentConfigDirs)

	if err := cfg.Validate(); err != nil {
		problems = append(problems, strings.Split(err.Error(), "\n")...)
	}
	if len(problems) > 0 {
		return refuse(problems)
	}
	return cfg, 0, false
}

// configFiles is whether dirs hold Consul configuration files (.hcl,
// .json): the agent is then configured there.
var configFiles = func(dirs []string) bool {
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if n := e.Name(); !e.IsDir() && (strings.HasSuffix(n, ".hcl") || strings.HasSuffix(n, ".json")) {
				return true
			}
		}
	}
	return false
}

// run starts everything and runs until ctx is done or the bundled agent
// stops.
func run(ctx context.Context, cfg config.Config, version string, log *logx.Logger, hup <-chan os.Signal) error {
	log.OK("starting", append([]any{"version", version}, fields(cfg)...)...)

	var rt runtime.Runtime
	err := retry(ctx, log, "the container runtime does not answer yet", func() error {
		var err error
		var at string
		rt, at, err = detect.Open(ctx, cfg.Socket, cfg.Runtime, cfg.ContainerdNamespace)
		if err == nil {
			log.OK("found the container runtime", "runtime", rt.Kind(), "version", rt.Version(), "socket", at)
		}
		return err
	})
	if err != nil {
		return err
	}

	var proc *agent.Process
	if cfg.Agent {
		proc, err = startAgent(ctx, cfg, rt, log)
		if err != nil {
			return err
		}
		defer proc.Stop(30 * time.Second)
	}

	c, err := consul.New(consul.Options{Addr: cfg.Consul, TokenFile: cfg.TokenFile, Token: cfg.Token, CAFile: cfg.CAFile, CertFile: cfg.CertFile, KeyFile: cfg.KeyFile})
	if err != nil {
		return err
	}
	var self consul.Self
	err = retry(ctx, log, "the Consul agent does not answer yet", func() error {
		if proc != nil {
			select {
			case <-proc.Done():
				return backoffStop{fmt.Errorf("the Consul agent stopped before it answered: %v", proc.Err())}
			default:
			}
		}
		var err error
		self, err = c.Self(ctx)
		switch {
		case consul.ServersUnreachable(err):
			return fmt.Errorf("no Consul server answers the agent yet, and it cannot check the token without one: %w", err)
		case errors.Is(err, consul.ErrDenied):
			return backoffStop{fmt.Errorf("the Consul agent refuses the token (it needs agent:read and service:write): %w", err)}
		}
		return err
	})
	if err != nil {
		return err
	}
	domain, err := checkDomain(cfg, self, log)
	if err != nil {
		return err
	}

	r := registrator.New(cfg, rt, c, log)
	r.IPv6Beside = hostnet.IPv6Beside
	r.Domain = domain
	go func() {
		for range hup {
			log.OK("SIGHUP: comparing everything again")
			r.Trigger()
			if proc != nil {
				_ = proc.Signal(syscall.SIGHUP) // the agent reads its configuration again
			}
		}
	}()

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- r.Run(rctx) }()
	var agentDone <-chan struct{}
	if proc != nil {
		agentDone = proc.Done()
	}
	select {
	case <-ctx.Done():
		return <-finished
	case <-agentDone:
		cancel()
		return errors.Join(fmt.Errorf("the Consul agent stopped: %v", proc.Err()), <-finished)
	case err := <-finished:
		return err
	}
}

// startAgent starts the bundled agent with the node name and addresses
// worked out from the runtime and the host.
func startAgent(ctx context.Context, cfg config.Config, rt runtime.Runtime, log *logx.Logger) (*agent.Process, error) {
	var host runtime.Host
	if err := retry(ctx, log, "the container runtime does not describe its host yet", func() error {
		var err error
		host, err = rt.Host(ctx)
		return err
	}); err != nil {
		return nil, err
	}
	node := cfg.AgentNode
	if node == "" {
		node = safe.Label(host.Name)
	}
	if node == "" {
		return nil, fmt.Errorf("the host's name %q makes no Consul node name; give --agent-node", safe.Short(host.Name, 80))
	}
	var bind netip.Addr
	switch {
	case cfg.AgentBind != "":
		bind = netip.MustParseAddr(cfg.AgentBind)
	case host.SwarmAddr != "":
		a, err := netip.ParseAddr(host.SwarmAddr)
		if err != nil {
			return nil, fmt.Errorf("the Swarm address %q is not an address; give --agent-bind", safe.Short(host.SwarmAddr, 60))
		}
		bind = a
	default:
		a, err := hostnet.DefaultIPv4()
		if err != nil {
			return nil, fmt.Errorf("no address to bind the Consul agent to (%v); give --agent-bind", err)
		}
		bind = a
	}
	// The agent's configuration from the registrator's settings, in a
	// directory only this process can read: it holds the secrets.
	dir, err := os.MkdirTemp("", "consul-agent-")
	if err != nil {
		return nil, fmt.Errorf("the Consul agent's configuration: %w", err)
	}
	datacenter := cfg.Datacenter
	if datacenter == "" && !cfg.AgentConfigFound {
		datacenter = "dc1"
	}
	files, err := agent.WriteConfig(dir, agent.Settings{
		Datacenter: datacenter, Join: cfg.Join, Encrypt: cfg.Encrypt,
		AgentToken: cfg.AgentToken, DNSToken: cfg.DNSToken, Defaults: !cfg.AgentConfigFound,
	}, cfg.LocalConfig)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	a := agent.Agent{
		Bin: cfg.AgentBin, ConfigDirs: cfg.AgentConfigDirs, ConfigFiles: files, DataDir: cfg.AgentDataDir,
		Node: node, Bind: bind, Domain: cfg.Domain, Env: agent.Environ(os.Environ()),
		Stdout: os.Stdout, Stderr: os.Stderr,
	}
	if bind.Is4() {
		a.IPv6 = hostnet.IPv6Beside(bind)
	}
	p, err := a.Start()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	go func() { <-p.Done(); _ = os.RemoveAll(dir) }()
	kv := []any{"pid", p.Pid(), "node", node, "bind", bind.String()}
	if a.IPv6.IsValid() {
		kv = append(kv, "ipv6", a.IPv6.String())
	}
	if cfg.Domain != "" {
		kv = append(kv, "domain", cfg.Domain)
	}
	log.OK("started the bundled Consul agent", kv...)
	return p, nil
}

// checkDomain makes sure the agent serves --domain, and returns the domain
// the services are named in.
func checkDomain(cfg config.Config, self consul.Self, log *logx.Logger) (string, error) {
	primary, alt := self.Domains()
	kv := []any{"node", safe.Short(self.Config.NodeName, 80), "datacenter", safe.Short(self.Config.Datacenter, 80), "version", safe.Short(self.Config.Version, 40)}
	switch cfg.Domain {
	case "", primary:
		d := primary
		if d == "" {
			d = "consul"
		}
		log.OK("the Consul agent answers", append(kv, "dns_domain", d)...)
		return d, nil
	case alt:
		log.Warn("the Consul agent serves the domain only as its alternative: its PTR answers name services in its own domain; make it the agent's domain",
			append(kv, "domain", cfg.Domain, "agent_domain", primary)...)
		return cfg.Domain, nil
	}
	return "", fmt.Errorf("the Consul agent serves DNS for %q, not %q: add domain = %q and alt_domain = %q to its configuration, or run with --agent to have the registrator start an agent serving it",
		primary, cfg.Domain, cfg.Domain, primary)
}

// quietFor is how long retry waits before it says why.
var quietFor = 5 * time.Second

// backoffStop is an error retry does not retry.
type backoffStop struct{ error }

// retry runs f until it succeeds, waiting longer each time up to 30 s. It
// says why it waits once that has gone on for quietFor - an agent starting
// takes a second or so - and every 30 s after.
func retry(ctx context.Context, log *logx.Logger, waiting string, f func() error) error {
	wait := time.Second
	said := time.Now().Add(quietFor - 30*time.Second)
	for {
		err := f()
		if err == nil {
			return nil
		}
		var stop backoffStop
		if errors.As(err, &stop) {
			return stop.error
		}
		if time.Since(said) > 30*time.Second {
			log.Warn(waiting, "err", err, "retry_in", wait)
			said = time.Now()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(2*wait, 30*time.Second)
	}
}

func fields(c config.Config) []any {
	kv := []any{"socket", c.Socket, "runtime", c.Runtime, "consul", c.Consul}
	if c.Domain != "" {
		kv = append(kv, "domain", c.Domain)
	}
	if c.TokenFile != "" {
		kv = append(kv, "token_file", c.TokenFile)
	}
	if len(c.Tags) > 0 {
		kv = append(kv, "tags", c.Tags)
	}
	if len(c.Addresses) > 0 {
		kv = append(kv, "address", c.Addresses)
	}
	kv = append(kv, "check", c.Check, "check_interval", c.CheckInterval, "deregister_after", c.DeregisterAfter, "resync", c.Resync)
	if c.Ignore {
		kv = append(kv, "dry_run", true)
	}
	if c.Agent {
		kv = append(kv, "agent", true)
	}
	return kv
}
