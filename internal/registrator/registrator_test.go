package registrator

import (
	"bytes"
	"context"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/consul"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/logx"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
)

type fakeRuntime struct {
	containers []runtime.Container
}

func (f *fakeRuntime) Kind() string    { return runtime.Docker }
func (f *fakeRuntime) Version() string { return "test" }
func (f *fakeRuntime) Host(context.Context) (runtime.Host, error) {
	return runtime.Host{Name: "docker1"}, nil
}
func (f *fakeRuntime) Containers(context.Context, string) ([]runtime.Container, error) {
	return f.containers, nil
}
func (f *fakeRuntime) Watch(ctx context.Context, _ string, opened func(), _ func(string)) error {
	opened()
	<-ctx.Done()
	return nil
}
func (f *fakeRuntime) Ingress(context.Context, string) ([]runtime.Port, error) {
	return nil, runtime.ErrUnsupported
}

type fakeConsul struct {
	mu       sync.Mutex
	services map[string]consul.Service
	calls    []string
}

func (f *fakeConsul) Self(context.Context) (consul.Self, error) {
	var s consul.Self
	s.Member.Addr = "192.168.1.93"
	return s, nil
}
func (f *fakeConsul) Services(context.Context) (map[string]consul.Service, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]consul.Service{}
	for k, v := range f.services {
		out[k] = v
	}
	return out, nil
}
func (f *fakeConsul) Register(_ context.Context, r consul.Registration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "register "+r.ID)
	f.services[r.ID] = consul.Service{ID: r.ID, Service: r.Name, Tags: r.Tags, Meta: r.Meta, Address: r.Address, Port: r.Port}
	return nil
}
func (f *fakeConsul) Deregister(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "deregister "+id)
	delete(f.services, id)
	return nil
}
func (f *fakeConsul) UpdateTTL(_ context.Context, id, status, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "ttl "+id+" "+status)
	return nil
}
func (f *fakeConsul) take() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := strings.Join(f.calls, "\n")
	f.calls = nil
	return s
}

func setup(cs ...runtime.Container) (*Registrator, *fakeRuntime, *fakeConsul, *bytes.Buffer) {
	rt := &fakeRuntime{containers: cs}
	c := &fakeConsul{services: map[string]consul.Service{
		"someone-elses": {ID: "someone-elses", Service: "db", Meta: map[string]string{"team": "x"}},
	}}
	var log bytes.Buffer
	r := New(config.Default(), rt, c, logx.New(&log, "registrator"))
	r.IPv6Beside = func(v4 netip.Addr) netip.Addr {
		if v4 == netip.MustParseAddr("192.168.1.93") {
			return netip.MustParseAddr("2001:db8::93")
		}
		return netip.Addr{}
	}
	r.Domain = "dmarts.app"
	return r, rt, c, &log
}

func TestReconcile(t *testing.T) {
	healthy := web(tcp(80, 8080, "0.0.0.0"), tcp(80, 8080, "::"))
	healthy.Health = &runtime.Health{Status: "healthy"}
	r, rt, c, log := setup(healthy)
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	want := "register dcr-abababababab-8080-tcp-ipv4\nregister dcr-abababababab-8080-tcp-ipv6\n" +
		"ttl service:dcr-abababababab-8080-tcp-ipv4 passing\nttl service:dcr-abababababab-8080-tcp-ipv6 passing"
	if got := c.take(); got != want {
		t.Fatalf("first:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(log.String(), "dns=web.service.dmarts.app") {
		t.Fatalf("log: %s", log.String())
	}

	// Nothing changed: nothing to do.
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := c.take(); got != "" {
		t.Fatalf("second, nothing changed:\n%s", got)
	}

	// The container turns unhealthy: one TTL update each, no registering.
	rt.containers[0].Health = &runtime.Health{Status: "unhealthy", ExitCode: 1}
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := c.take(); got != "ttl service:dcr-abababababab-8080-tcp-ipv4 critical\nttl service:dcr-abababababab-8080-tcp-ipv6 critical" {
		t.Fatalf("unhealthy:\n%s", got)
	}

	// The container stops: its services go, someone else's stays.
	rt.containers = nil
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := c.take(); got != "deregister dcr-abababababab-8080-tcp-ipv4\nderegister dcr-abababababab-8080-tcp-ipv6" {
		t.Fatalf("stopped:\n%s", got)
	}
	if _, ok := c.services["someone-elses"]; !ok || len(c.services) != 1 {
		t.Fatalf("services left: %v", c.services)
	}
}

func TestOnlyConsulYes(t *testing.T) {
	other := web(tcp(80, 8080, ""))
	other.Labels[Label] = "true"
	r, _, c, log := setup(other)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.take(); got != "" {
		t.Fatalf("consul=true registered:\n%s", got)
	}
	if !strings.Contains(log.String(), "its consul label is not yes") {
		t.Fatalf("log: %s", log.String())
	}
	n := strings.Count(log.String(), "WARN")
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Count(log.String(), "WARN") != n {
		t.Fatal("the same note was logged twice")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	r, _, c, log := setup(web(tcp(80, 8080, "")))
	r.cfg.Ignore = true
	c.services["dcr-old"] = consul.Service{ID: "dcr-old", Meta: map[string]string{MetaOwner: Owner}}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.take(); got != "" {
		t.Fatalf("dry run wrote:\n%s", got)
	}
	if !strings.Contains(log.String(), "would register") || !strings.Contains(log.String(), "would deregister") {
		t.Fatalf("log: %s", log.String())
	}
}

func TestAddresses(t *testing.T) {
	r, _, _, _ := setup()
	var self consul.Self
	self.Member.Addr = "192.168.1.93"
	a, err := r.addrs(self)
	if err != nil || a.V4.String() != "192.168.1.93" || a.V6.String() != "2001:db8::93" {
		t.Fatalf("addrs = %+v, %v", a, err)
	}
	r.cfg.Addresses = []string{"10.0.0.5"}
	a, _ = r.addrs(self)
	if a.V4.String() != "10.0.0.5" || a.V6.IsValid() {
		t.Fatalf("--address: %+v", a)
	}
	r.cfg.Addresses = nil
	self.Member.Addr = "not-an-address"
	if _, err := r.addrs(self); err == nil {
		t.Fatal("an agent address that is not one: no error")
	}
}
