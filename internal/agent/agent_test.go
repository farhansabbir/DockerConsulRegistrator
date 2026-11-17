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
