package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/consul"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/logx"
)

func parse(args ...string) (config.Config, int, bool, string, string) {
	return parseEnv(nil, args...)
}

func parseEnv(env map[string]string, args ...string) (config.Config, int, bool, string, string) {
	var out, errOut bytes.Buffer
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	cfg, code, done := Parse(args, lookup, "1.2.3", &out, &errOut)
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
	for _, f := range []string{"socket", "runtime", "containerd-namespace", "label", "consul", "token-file", "ca-file", "cert-file", "key-file",
		"domain", "tags", "address", "check", "check-interval", "deregister-after", "resync", "ignore", "agent", "agent-bin",
		"agent-config-dir", "agent-data-dir", "agent-node", "agent-bind", "join", "datacenter", "encrypt-file", "agent-token-file",
		"dns-token-file", "debug", "version", "help"} {
		if !strings.Contains(help, "--"+f+" ") && !strings.Contains(help, "--"+f+"\n") && !strings.Contains(help, "--"+f+",") {
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
		if !strings.HasPrefix(errOut, "docker-consul-registrator: cannot start:\n  - ") || !strings.HasSuffix(errOut, "Run 'docker-consul-registrator --help' for every setting.\n") {
			t.Errorf("%q: %q", args, errOut)
		}
	}
}

func TestParse(t *testing.T) {
	cfg, _, done, _, errOut := parse("-s", "/run/podman/podman.sock", "--runtime", "podman", "-t", "prod,v2", "-t", "web",
		"--address", "192.168.1.93", "--address", "2001:db8::93", "--domain", "dmarts.app", "--agent", "--join", "10.0.0.11",
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

func TestRetryIsQuietAtFirst(t *testing.T) {
	var out bytes.Buffer
	log := logx.New(&out, "t")
	n := 0
	err := retry(context.Background(), log, "waiting", func() error {
		n++
		if n < 2 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil || strings.Contains(out.String(), "waiting") {
		t.Fatalf("a quick success logged: %q, %v", out.String(), err)
	}
	old := quietFor
	quietFor = 0
	defer func() { quietFor = old }()
	n = 0
	_ = retry(context.Background(), log, "waiting", func() error {
		n++
		if n < 2 {
			return errors.New("not yet")
		}
		return nil
	})
	if !strings.Contains(out.String(), "waiting") {
		t.Fatalf("a wait past quietFor not logged: %q", out.String())
	}
	stop := retry(context.Background(), log, "waiting", func() error { return backoffStop{errors.New("refused")} })
	if stop == nil || stop.Error() != "refused" {
		t.Fatalf("backoffStop: %v", stop)
	}
}

const gossip = "pUqJrVyVRj5jsiYEkM/tFQYfWyJIv4s3XkvDwy7Cu5s="

func noAgentConfig(t *testing.T) {
	old := configFiles
	configFiles = func([]string) bool { return false }
	t.Cleanup(func() { configFiles = old })
}

func TestTheEnvironmentConfiguresIt(t *testing.T) {
	noAgentConfig(t)
	cfg, _, done, _, errOut := parseEnv(map[string]string{
		"REGISTRATOR_AGENT":          "true",
		"CONSUL_RETRY_JOIN":          "10.0.0.11,10.0.0.12",
		"CONSUL_DATACENTER":          "lab",
		"CONSUL_DOMAIN":              "dmarts.app",
		"REGISTRATOR_LABEL":          "app.dmarts/register=true",
		"REGISTRATOR_TAGS":           "prod,v2",
		"REGISTRATOR_CHECK":          "tcp",
		"CONSUL_ENCRYPT":             gossip,
		"CONSUL_AGENT_TOKEN":         "agent-tok",
		"CONSUL_HTTP_TOKEN":          "registrator-tok",
		"CONSUL_DNS_TOKEN":           "dns-tok",
		"CONSUL_LOCAL_CONFIG":        `{"log_level":"warn"}`,
		"REGISTRATOR_DRY_RUN":        "1",
		"REGISTRATOR_CHECK_INTERVAL": "5s",
	}, "--check", "ttl")
	if done {
		t.Fatalf("refused: %s", errOut)
	}
	if !cfg.Agent || strings.Join(cfg.Join, "|") != "10.0.0.11|10.0.0.12" || cfg.Datacenter != "lab" || cfg.Domain != "dmarts.app" ||
		cfg.LabelKey != "app.dmarts/register" || cfg.LabelValue != "true" || strings.Join(cfg.Tags, ",") != "prod,v2" ||
		cfg.Encrypt != gossip || cfg.AgentToken != "agent-tok" || cfg.Token != "registrator-tok" || cfg.DNSToken != "dns-tok" ||
		cfg.LocalConfig == "" || !cfg.Ignore || cfg.CheckInterval.String() != "5s" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.Check != "ttl" {
		t.Fatalf("the command line did not win over REGISTRATOR_CHECK: %s", cfg.Check)
	}
}

func TestItSaysEverythingMissingAtOnce(t *testing.T) {
	noAgentConfig(t)
	_, code, done, _, errOut := parseEnv(map[string]string{
		"REGISTRATOR_AGENT":  "true",
		"CONSUL_AGENT_TOKEN": "agent-tok", // no registrator token
		"CONSUL_ENCRYPT":     "not-a-key",
		"REGISTRATOR_LABEL":  "no-equals",
		"CONSUL_DATACENTER":  "bad dc",
	})
	if !done || code != 2 {
		t.Fatalf("not refused: %d %v", code, done)
	}
	for _, want := range []string{"CONSUL_RETRY_JOIN (--join) is required", "both CONSUL_AGENT_TOKEN and CONSUL_HTTP_TOKEN",
		"CONSUL_ENCRYPT is not a gossip key", "REGISTRATOR_LABEL (--label)", "CONSUL_DATACENTER"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("no %q in:\n%s", want, errOut)
		}
	}
	if strings.Contains(errOut, "agent-tok") || strings.Contains(errOut, "not-a-key") {
		t.Errorf("a secret reached the output:\n%s", errOut)
	}
}

func TestAMountedConfigurationIsEnough(t *testing.T) {
	old := configFiles
	configFiles = func([]string) bool { return true }
	t.Cleanup(func() { configFiles = old })
	if _, _, done, _, errOut := parseEnv(map[string]string{"REGISTRATOR_AGENT": "true"}); done {
		t.Fatalf("refused with the agent configured in /consul/config: %s", errOut)
	}
}

func TestSecretsRefused(t *testing.T) {
	noAgentConfig(t)
	file := t.TempDir() + "/tok"
	if err := os.WriteFile(file, []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string]map[string]string{
		"value and file":       {"CONSUL_AGENT_TOKEN": "a", "CONSUL_AGENT_TOKEN_FILE": file},
		"a file that is not":   {"CONSUL_HTTP_TOKEN_FILE": "/nonexistent/token"},
		"two http tokens":      {"CONSUL_HTTP_TOKEN": "a", "CONSUL_HTTP_TOKEN_FILE": file},
		"dns without agent":    {"CONSUL_DNS_TOKEN": "d", "CONSUL_HTTP_TOKEN": "r"},
		"agent settings alone": {"CONSUL_RETRY_JOIN": "10.0.0.11"}, // without --agent
		"a control character":  {"CONSUL_DOMAIN": "dmarts.app\nevil"},
		"local config":         {"CONSUL_LOCAL_CONFIG": "[1,2]"},
	} {
		if name != "agent settings alone" {
			env["REGISTRATOR_AGENT"] = "true"
			env["CONSUL_RETRY_JOIN"] = "10.0.0.11"
		}
		if _, code, done, _, errOut := parseEnv(env); !done || code != 2 {
			t.Errorf("%s: accepted (%s)", name, errOut)
		}
	}
}
