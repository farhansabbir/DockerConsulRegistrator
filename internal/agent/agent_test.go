package agent

import (
	"bytes"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestArgs(t *testing.T) {
	a := Agent{
		DataDir:    "/consul/data",
		ConfigDirs: []string{"/consul/config", "/run/secrets/consul"},
		Node:       "docker1",
		Bind:       netip.MustParseAddr("192.168.1.93"),
		IPv6:       netip.MustParseAddr("2001:db8::93"),
		Domain:     "dmarts.app",
	}
	want := `agent -data-dir=/consul/data -config-dir=/consul/config -config-dir=/run/secrets/consul -node=docker1 ` +
		`-bind=192.168.1.93 -advertise=192.168.1.93 -hcl=advertise_addr_ipv6 = "2001:db8::93" -domain=dmarts.app -alt-domain=consul`
	if got := strings.Join(a.Args(), " "); got != want {
		t.Fatalf("args:\n%s\nwant:\n%s", got, want)
	}
	a = Agent{DataDir: "/d", Domain: "consul"}
	if got := strings.Join(a.Args(), " "); got != "agent -data-dir=/d" {
		t.Fatalf("minimal args: %s", got)
	}
}

// TestStartStop runs a stand-in for consul: a script that prints its
// arguments and waits for SIGTERM.
func TestStartStop(t *testing.T) {
	sh, err := os.Stat("/bin/sh")
	if err != nil || sh.IsDir() {
		t.Skip("no /bin/sh")
	}
	bin := filepath.Join(t.TempDir(), "consul")
	script := "#!/bin/sh\necho \"args: $*\"\ntrap 'echo left; exit 0' TERM\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var out syncBuffer
	p, err := Agent{Bin: bin, DataDir: "/d", Node: "n1", Stdout: &out, Stderr: &out}.Start()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "args: agent -data-dir=/d -node=n1") {
		if time.Now().After(deadline) {
			t.Fatalf("output: %q", out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	p.Stop(5 * time.Second)
	select {
	case <-p.Done():
	default:
		t.Fatal("still running after Stop")
	}
	if !strings.Contains(out.String(), "left") {
		t.Fatalf("the agent did not get SIGTERM: %q", out.String())
	}
	p.Stop(time.Second) // a second Stop does nothing
}

func TestStartRefusesWhatIsNotAProgram(t *testing.T) {
	f := filepath.Join(t.TempDir(), "consul")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (Agent{Bin: f}).Start(); err == nil {
		t.Fatal("a file without the executable bit started")
	}
	if _, err := (Agent{Bin: filepath.Dir(f)}).Start(); err == nil {
		t.Fatal("a directory started")
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestWriteConfig(t *testing.T) {
	dir := t.TempDir()
	files, err := WriteConfig(dir, Settings{Datacenter: "lab", Join: []string{"192.168.1.92"}, Encrypt: "k3y=",
		AgentToken: "agent-tok", DNSToken: "dns-tok", Defaults: true}, `{"log_level":"debug"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || filepath.Base(files[0]) != "registrator.json" || filepath.Base(files[1]) != "local.json" {
		t.Fatalf("files = %v", files)
	}
	b, _ := os.ReadFile(files[0])
	for _, want := range []string{`"datacenter": "lab"`, `"retry_join": [`, `"192.168.1.92"`, `"encrypt": "k3y="`, `"client_addr": "127.0.0.1"`,
		`"only_passing": true`, `"grpc": -1`, `"default_policy": "deny"`, `"agent": "agent-tok"`, `"default": "dns-tok"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("no %s in\n%s", want, b)
		}
	}
	for _, f := range files {
		if fi, _ := os.Stat(f); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v, want 0600", f, fi.Mode().Perm())
		}
	}
	// Configured elsewhere, nothing given here: no file at all.
	if files, err := WriteConfig(t.TempDir(), Settings{}, ""); err != nil || len(files) != 0 {
		t.Fatalf("nothing to write: %v %v", files, err)
	}
	// Without a token, no ACL block.
	files, _ = WriteConfig(t.TempDir(), Settings{Join: []string{"s1"}, Defaults: true}, "")
	if b, _ := os.ReadFile(files[0]); strings.Contains(string(b), "acl") {
		t.Fatalf("ACLs without a token:\n%s", b)
	}
}

func TestEnvironDropsTheSecrets(t *testing.T) {
	got := Environ([]string{"PATH=/bin", "CONSUL_HTTP_TOKEN=x", "CONSUL_AGENT_TOKEN=y", "CONSUL_HTTP_TOKEN_FILE=/run/secrets/t", "CONSUL_RETRY_JOIN=s1"})
	if strings.Join(got, " ") != "PATH=/bin CONSUL_HTTP_TOKEN_FILE=/run/secrets/t CONSUL_RETRY_JOIN=s1" {
		t.Fatalf("Environ = %v", got)
	}
}
