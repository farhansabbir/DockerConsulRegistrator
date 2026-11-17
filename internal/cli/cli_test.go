package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/consul"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/logx"
)

func parse(args ...string) (config.Config, int, bool, string, string) {
	var out, errOut bytes.Buffer
	cfg, code, done := Parse(args, "1.2.3", &out, &errOut)
	return cfg, code, done, out.String(), errOut.String()
}

func TestHelpAndVersion(t *testing.T) {
	_, code, done, out, _ := parse("--help")
	if !done || code != 0 || !strings.HasPrefix(out, "Usage: docker-consul-registrator") {
		t.Fatalf("--help: %d %v %q", code, done, out[:min(len(out), 80)])
	}
	_, code, done, out, _ = parse("-v")
	if !done || code != 0 || out != "docker-consul-registrator 1.2.3\n" {
		t.Fatalf("-v: %d %q", code, out)
	}
}

// TestHelpNamesEveryFlag keeps the written help and the flags in step.
func TestHelpNamesEveryFlag(t *testing.T) {
	for _, f := range []string{"socket", "runtime", "containerd-namespace", "consul", "token-file", "ca-file", "cert-file", "key-file",
		"domain", "tags", "address", "check", "check-interval", "deregister-after", "resync", "ignore", "agent", "agent-bin",
		"agent-config-dir", "agent-data-dir", "agent-node", "agent-bind", "debug", "version", "help"} {
		if !strings.Contains(help, "--"+f+" ") && !strings.Contains(help, "--"+f+"\n") {
			t.Errorf("the help does not name --%s", f)
		}
		if _, _, done, _, errOut := parse("--" + f + "=x"); done && strings.Contains(errOut, "unknown flag") {
			t.Errorf("--%s is in the help but is not a flag", f)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"--nope"},
		{"extra"},
		{"--check", "http"},
		{"--tags", "a b"},
		{"--domain", "Dmarts.app"},
		{"--socket", "relative.sock"},
		{"--consul", "http://x:1/path"},
		{"--agent-node", "n"},
		{"--tags", "a\nb"},
	} {
		_, code, done, _, errOut := parse(args...)
		if !done || code != 2 {
			t.Errorf("%q: code %d done %v", args, code, done)
		}
		if !strings.Contains(errOut, "Run 'docker-consul-registrator --help' for usage.") || strings.Count(errOut, "\n") != 2 {
			t.Errorf("%q: %q", args, errOut)
		}
	}
}

func TestParse(t *testing.T) {
	cfg, _, done, _, errOut := parse("-s", "/run/podman/podman.sock", "--runtime", "podman", "-t", "prod,v2", "-t", "web",
		"--address", "192.168.1.93", "--address", "2001:db8::93", "--domain", "dmarts.app", "--agent",
		"--agent-config-dir", "/consul/config", "--agent-config-dir", "/run/secrets/consul", "-i")
	if done {
		t.Fatalf("usage error: %s", errOut)
	}
	if cfg.Socket != "/run/podman/podman.sock" || cfg.Runtime != "podman" || strings.Join(cfg.Tags, "|") != "prod|v2|web" ||
		len(cfg.Addresses) != 2 || cfg.Domain != "dmarts.app" || !cfg.Agent || !cfg.Ignore ||
		strings.Join(cfg.AgentConfigDirs, "|") != "/consul/config|/run/secrets/consul" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestCheckDomain(t *testing.T) {
	log := logx.New(&bytes.Buffer{}, "t")
	self := func(d, alt string) consul.Self {
		var s consul.Self
		s.DebugConfig.DNSDomain, s.DebugConfig.DNSAltDomain = d, alt
		return s
	}
	cfg := config.Default()
	if d, err := checkDomain(cfg, self("consul.", ""), log); err != nil || d != "consul" {
		t.Errorf("no --domain: %q %v", d, err)
	}
	cfg.Domain = "dmarts.app"
	if d, err := checkDomain(cfg, self("dmarts.app.", "consul."), log); err != nil || d != "dmarts.app" {
		t.Errorf("served: %q %v", d, err)
	}
	if d, err := checkDomain(cfg, self("consul.", "dmarts.app"), log); err != nil || d != "dmarts.app" {
		t.Errorf("served as the alternative: %q %v", d, err)
	}
	if _, err := checkDomain(cfg, self("consul.", ""), log); err == nil || !strings.Contains(err.Error(), `domain = "dmarts.app"`) {
		t.Errorf("not served: %v", err)
	}
}
