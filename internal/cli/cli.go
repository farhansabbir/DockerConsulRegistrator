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
)

// Name is the command's name.
const Name = "docker-consul-registrator"

const help = `Usage: docker-consul-registrator [options]

Registers the published ports of the running containers labelled consul=yes
with the local Consul agent - one service per port and address family, so
Consul's DNS answers A, AAAA, SRV and PTR for them - and deregisters them when
the containers stop. It touches only the services it registered.

Container runtime:
  -s, --socket PATH|URL         the runtime's socket: a path, unix:///path or
                                tcp://host:port (default auto: the usual paths
                                of Docker's, Podman's and containerd's)
      --runtime NAME            auto, docker, podman or containerd (default
                                auto: known by how the socket answers)
      --containerd-namespace NS containerd's namespace (default "default")

Consul agent:
  -c, --consul ADDR             its HTTP API: host:port, http://host:port or
                                https://host:port (default 127.0.0.1:8500)
      --token-file FILE         the ACL token to register with, read from FILE
      --ca-file FILE            CA certificate for https
      --cert-file FILE          client certificate for https (with --key-file)
      --key-file FILE           client key for https
      --domain DOMAIN           the DNS domain to serve the services in, e.g.
                                dmarts.app (default: the agent's own); the
                                bundled agent is started serving it

Services:
  -t, --tags TAG,...            Consul tags for every service (repeatable); the
                                protocol (tcp, udp) is always added
      --address IP              the host address to register: one IPv4, one
                                IPv6 (repeatable; default: the agent's address
                                and the IPv6 address beside it)
      --check MODE              auto, tcp, ttl or none (default auto: the
                                container's own health check where it has one,
                                else TCP to the port)
      --check-interval DUR      how often checks run (default 10s)
      --deregister-after DUR    Consul removes a service critical this long, a
                                safety net (default 10m; 0 never; at least 1m)
      --resync DUR              compare everything this often, besides on every
                                change (default 1m)
  -i, --ignore                  dry run: log what would change, change nothing

Bundled Consul agent (the container image):
      --agent                   start the agent in this container, register
                                with it, stop it on the way out
      --agent-bin PATH          (default /bin/consul)
      --agent-config-dir DIR    its configuration: servers to join, gossip key,
                                ACL tokens (repeatable; default /consul/config)
      --agent-data-dir DIR      its state - keep it on a volume (default
                                /consul/data)
      --agent-node NAME         its node name (default: the host's name)
      --agent-bind IP           its address (default: the host's Swarm address,
                                else the default route's)

Other:
      --debug                   log every event and decision
  -v, --version                 print the version
  -h, --help                    print this help
`

// Main runs the command and returns its exit status: 0, 1 when it failed, 2
// for a usage error.
func Main(args []string, version string, stdout, stderr io.Writer) int {
	cfg, code, done := Parse(args, version, stdout, stderr)
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

// Parse reads the command line. done says the command is over - help,
// version or a usage error - with code its exit status.
func Parse(args []string, version string, stdout, stderr io.Writer) (cfg config.Config, code int, done bool) {
	usage := func(format string, a ...any) (config.Config, int, bool) {
		_, _ = fmt.Fprintf(stderr, "%s: %s\nRun '%s --help' for usage.\n", Name, safe.Text(fmt.Sprintf(format, a...)), Name)
		return cfg, 2, true
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
	fs.StringVarP(&cfg.Socket, "socket", "s", cfg.Socket, "")
	fs.StringVar(&cfg.Runtime, "runtime", cfg.Runtime, "")
	fs.StringVar(&cfg.ContainerdNamespace, "containerd-namespace", cfg.ContainerdNamespace, "")
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
	if err := cfg.Validate(); err != nil {
		return usage("%s", strings.ReplaceAll(err.Error(), "\n", "; "))
	}
	return cfg, 0, false
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

	c, err := consul.New(consul.Options{Addr: cfg.Consul, TokenFile: cfg.TokenFile, CAFile: cfg.CAFile, CertFile: cfg.CertFile, KeyFile: cfg.KeyFile})
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
		if errors.Is(err, consul.ErrDenied) {
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
	a := agent.Agent{
		Bin: cfg.AgentBin, ConfigDirs: cfg.AgentConfigDirs, DataDir: cfg.AgentDataDir,
		Node: node, Bind: bind, Domain: cfg.Domain,
		Stdout: os.Stdout, Stderr: os.Stderr,
	}
	if bind.Is4() {
		a.IPv6 = hostnet.IPv6Beside(bind)
	}
	p, err := a.Start()
	if err != nil {
		return nil, err
	}
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

// backoffStop is an error retry does not retry.
type backoffStop struct{ error }

// retry runs f until it succeeds, waiting longer each time up to 30 s, and
// says why it waits now and then.
func retry(ctx context.Context, log *logx.Logger, waiting string, f func() error) error {
	wait := time.Second
	var said time.Time
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
