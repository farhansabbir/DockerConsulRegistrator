package registrator

import (
	"bytes"
	"context"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/consul"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/logx"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
)

type fakeRuntime struct {
	host       runtime.Host
	containers []runtime.Container
	services   []runtime.SwarmService
}

func (f *fakeRuntime) Kind() string    { return runtime.Docker }
func (f *fakeRuntime) Version() string { return "test" }
func (f *fakeRuntime) Host(context.Context) (runtime.Host, error) {
	h := f.host
	if h.Name == "" {
		h.Name = "docker1"
	}
	return h, nil
}
func (f *fakeRuntime) Containers(_ context.Context, labels ...string) ([]runtime.Container, error) {
	var out []runtime.Container
	for _, c := range f.containers {
		for _, l := range labels {
			if _, ok := c.Labels[l]; ok {
				out = append(out, c)
				break
			}
		}
	}
	return out, nil
}
func (f *fakeRuntime) Watch(ctx context.Context, _ []string, opened func(), _ func(string)) error {
	opened()
	<-ctx.Done()
	return nil
}
func (f *fakeRuntime) SwarmServices(context.Context) ([]runtime.SwarmService, error) {
	if !f.host.SwarmManager {
		return nil, runtime.ErrUnsupported
	}
	return f.services, nil
}

type fakeConsul struct {
	mu       sync.Mutex
	services map[string]consul.Service
	calls    []string
	kv       map[string]string
	kvCalls  []string
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
func (f *fakeConsul) KVList(_ context.Context, prefix string, _ uint64, _ time.Duration) ([]consul.KVPair, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []consul.KVPair
	for _, k := range sortedIDs(f.kv) {
		if strings.HasPrefix(k, prefix) {
			out = append(out, consul.KVPair{Key: k, Value: []byte(f.kv[k])})
		}
	}
	return out, 1, nil
}
func (f *fakeConsul) KVPut(_ context.Context, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kvCalls = append(f.kvCalls, "put "+key)
	f.kv[key] = string(value)
	return nil
}
func (f *fakeConsul) KVDelete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kvCalls = append(f.kvCalls, "delete "+key)
	delete(f.kv, key)
	return nil
}
func (f *fakeConsul) takeKV() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := strings.Join(f.kvCalls, "\n")
	f.kvCalls = nil
	return s
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
	}, kv: map[string]string{}}
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
	if !strings.Contains(log.String(), "its label's value is not the one to register") {
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

func swarmTask(id, service, serviceID string) runtime.Container {
	return runtime.Container{ID: id, Name: service + ".1." + id[:6], Running: true,
		Labels: map[string]string{runtime.LabelSwarmService: service, runtime.LabelSwarmServiceID: serviceID}}
}

// TestServiceLabelOnAWorker: a worker cannot read Swarm services; it
// registers its tasks of the services the managers wrote to the KV store.
func TestServiceLabelOnAWorker(t *testing.T) {
	r, rt, c, log := setup(
		swarmTask(strings.Repeat("a", 64), "web-svc", "svc123"),
		swarmTask(strings.Repeat("b", 64), "other", "svc999"), // not labelled
	)
	rt.host = runtime.Host{Name: "docker2", SwarmAddr: "192.168.1.94"}
	c.kv[KVPrefix+"cluster1/svc123"] = `{"name":"web-svc","ports":[{"proto":"tcp","target":80,"published":8181}]}`
	c.kv[KVPrefix+"cluster1/svcbad"] = `{"name":"x","ports":[{"proto":"icmp","target":0,"published":1}]}`
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.take(); got != "register dcr-aaaaaaaaaaaa-8181-tcp-ipv4\nttl service:dcr-aaaaaaaaaaaa-8181-tcp-ipv4 passing" {
		t.Fatalf("calls:\n%s", got)
	}
	if !r.swarmWorker.Load() {
		t.Fatal("not marked a Swarm worker: it would not follow the KV store")
	}
	if !strings.Contains(log.String(), "a Swarm service in the KV store is not one") || strings.Contains(log.String(), "other") {
		t.Fatalf("log: %s", log.String())
	}
	if got := c.takeKV(); got != "" {
		t.Fatalf("a worker wrote the KV store:\n%s", got)
	}
}

// TestAManagerWritesTheSwarmServices: what is labelled, on the service or
// its containers, with its routing-mesh ports; nothing else.
func TestAManagerWritesTheSwarmServices(t *testing.T) {
	r, rt, c, _ := setup(swarmTask(strings.Repeat("a", 64), "web-svc", "svcA"))
	rt.host = runtime.Host{Name: "docker1", SwarmAddr: "192.168.1.93", SwarmManager: true, SwarmCluster: "cluster1"}
	rt.services = []runtime.SwarmService{
		{ID: "svcA", Name: "web-svc", Labels: map[string]string{Label: "yes"},
			Ports: []runtime.Port{{Proto: "tcp", Target: 80, HostPort: 8181, Mode: "ingress"}, {Proto: "tcp", Target: 81, HostPort: 8282, Mode: "host"}}},
		{ID: "svcB", Name: "by-container", ContainerLabels: map[string]string{Label: "yes"}},
		{ID: "svcC", Name: "not-labelled"},
	}
	c.kv[KVPrefix+"cluster1/gone"] = `{"name":"gone"}`
	c.kv[KVPrefix+"other-swarm/svcZ"] = `{"name":"theirs"}`
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "put " + KVPrefix + "cluster1/svcA\nput " + KVPrefix + "cluster1/svcB\ndelete " + KVPrefix + "cluster1/gone"
	if got := c.takeKV(); got != want {
		t.Fatalf("KV writes:\n%s\nwant:\n%s", got, want)
	}
	if got := c.kv[KVPrefix+"cluster1/svcA"]; got != `{"name":"web-svc","ports":[{"proto":"tcp","target":80,"published":8181}]}` {
		t.Fatalf("svcA = %s", got)
	}
	if _, ok := c.kv[KVPrefix+"other-swarm/svcZ"]; !ok {
		t.Fatal("another Swarm's entry was removed")
	}
	if got := c.take(); got != "register dcr-aaaaaaaaaaaa-8181-tcp-ipv4\nttl service:dcr-aaaaaaaaaaaa-8181-tcp-ipv4 passing" {
		t.Fatalf("the manager's own task:\n%s", got)
	}
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.takeKV(); got != "" {
		t.Fatalf("nothing changed, yet:\n%s", got)
	}
}
