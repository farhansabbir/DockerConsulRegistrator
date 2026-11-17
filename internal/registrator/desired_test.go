package registrator

import (
	"net/netip"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
)

var dual = Addrs{V4: netip.MustParseAddr("192.168.1.93"), V6: netip.MustParseAddr("2001:db8::93")}

func opts() Options {
	return Options{Addrs: dual, Node: "docker1", Check: config.CheckAuto, Interval: 10 * time.Second, DeregisterAfter: 10 * time.Minute}
}

func web(ports ...runtime.Port) runtime.Container {
	return runtime.Container{ID: strings.Repeat("ab", 32), Name: "web", Labels: map[string]string{Label: "yes"}, Running: true, Ports: ports}
}

func tcp(target, port int, hostIP string) runtime.Port {
	return runtime.Port{Proto: "tcp", Target: target, HostIP: hostIP, HostPort: port}
}

func ids(want map[string]Want) string {
	var out []string
	for id, w := range want {
		out = append(out, id+"="+w.Reg.Name+"@"+w.Reg.Address)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestOneServicePerPortAndFamily(t *testing.T) {
	want, notes := Desired([]runtime.Container{web(tcp(80, 8080, "0.0.0.0"), tcp(80, 8080, "::"))}, nil, opts())
	if len(notes) != 0 {
		t.Fatalf("notes: %+v", notes)
	}
	if got := ids(want); got != "dcr-abababababab-8080-tcp-ipv4=web@192.168.1.93 dcr-abababababab-8080-tcp-ipv6=web@2001:db8::93" {
		t.Fatalf("got %s", got)
	}
	v6 := want["dcr-abababababab-8080-tcp-ipv6"].Reg
	if v6.Port != 8080 || strings.Join(v6.Tags, ",") != "tcp" || v6.Check == nil || v6.Check.TCP != "[2001:db8::93]:8080" {
		t.Fatalf("ipv6 registration: %+v %+v", v6, v6.Check)
	}
	if v6.Check.DeregisterCriticalServiceAfter != "10m0s" || v6.Check.Interval != "10s" || v6.Check.Timeout != "2s" {
		t.Fatalf("check: %+v", v6.Check)
	}
	if v6.Meta[MetaOwner] != Owner || v6.Meta["container-port"] != "80/tcp" || v6.Meta["docker-node"] != "docker1" || v6.Meta[MetaHash] == "" {
		t.Fatalf("meta: %v", v6.Meta)
	}
}

func TestIPv6OnlyWhenPublishedOnItAndKnown(t *testing.T) {
	want, _ := Desired([]runtime.Container{web(tcp(80, 8080, "0.0.0.0"))}, nil, opts())
	if len(want) != 1 {
		t.Fatalf("published on IPv4 only: %s", ids(want))
	}
	o := opts()
	o.Addrs.V6 = netip.Addr{}
	want, notes := Desired([]runtime.Container{web(tcp(80, 8080, "0.0.0.0"), tcp(80, 8080, "::"))}, nil, o)
	if len(want) != 1 || len(notes) != 0 {
		t.Fatalf("no IPv6 address known: %s %+v", ids(want), notes)
	}
}

func TestNamesAndProtocols(t *testing.T) {
	// A container is its own service, named after it - Compose's too; a
	// Swarm task is an instance of its service. Ports share the name and
	// are told apart by tags.
	two := web(tcp(80, 8080, ""), tcp(443, 8443, ""))
	dns := runtime.Container{ID: strings.Repeat("cd", 32), Name: "dns", Labels: map[string]string{Label: "yes"}, Running: true,
		Ports: []runtime.Port{{Proto: "tcp", Target: 53, HostPort: 53}, {Proto: "udp", Target: 53, HostPort: 53}}}
	swarm := web(tcp(80, 9090, "0.0.0.0"))
	swarm.ID = strings.Repeat("ef", 32)
	swarm.Labels = map[string]string{Label: "yes", runtime.LabelSwarmService: "MyStack_Web"}
	compose := web(tcp(80, 7070, ""))
	compose.ID, compose.Name = strings.Repeat("12", 32), "shop-api-1"
	compose.Labels = map[string]string{Label: "yes", "com.docker.compose.project": "shop", "com.docker.compose.service": "api"}
	o := opts()
	o.Addrs.V6 = netip.Addr{}
	want, _ := Desired([]runtime.Container{two, dns, swarm, compose}, nil, o)
	got := map[string]bool{}
	for _, w := range want {
		got[w.Reg.Name+" "+strings.Join(w.Reg.Tags, ",")] = true
		if w.Reg.Name == "dns" && w.Reg.Tags[0] == "udp" && w.Reg.Check != nil {
			t.Errorf("a UDP port without a health check got a check: %+v", w.Reg.Check)
		}
	}
	for _, n := range []string{"web tcp,port-80", "web tcp,port-443", "dns tcp", "dns udp", "mystack-web tcp", "shop-api-1 tcp"} {
		if !got[n] {
			t.Errorf("no %q in %v", n, got)
		}
	}
}

func TestHealthBecomesATTLCheck(t *testing.T) {
	c := web(tcp(80, 8080, ""))
	for status, want := range map[string]string{"healthy": "passing", "unhealthy": "critical", "starting": "critical"} {
		c.Health = &runtime.Health{Status: status, ExitCode: 1, Output: "refused\n"}
		got, _ := Desired([]runtime.Container{c}, nil, opts())
		w := got["dcr-abababababab-8080-tcp-ipv4"]
		if w.Reg.Check == nil || w.Reg.Check.TTL != "30s" || w.TTL == nil || w.TTL.Status != want || w.Reg.Check.Status != want {
			t.Errorf("%s: %+v %+v", status, w.Reg.Check, w.TTL)
		}
		if w.TTL.CheckID != "service:dcr-abababababab-8080-tcp-ipv4" {
			t.Errorf("check ID %s", w.TTL.CheckID)
		}
	}
	c.Health = nil
	o := opts()
	o.Check = config.CheckTTL
	got, _ := Desired([]runtime.Container{c}, nil, o)
	if w := got["dcr-abababababab-8080-tcp-ipv4"]; w.TTL == nil || w.TTL.Status != "passing" {
		t.Errorf("--check ttl without a health check: %+v", w.TTL)
	}
	o.Check = config.CheckNone
	got, _ = Desired([]runtime.Container{c}, nil, o)
	if w := got["dcr-abababababab-8080-tcp-ipv4"]; w.Reg.Check != nil {
		t.Errorf("--check none: %+v", w.Reg.Check)
	}
}

func TestTheHashLeavesTheStatusOut(t *testing.T) {
	c := web(tcp(80, 8080, ""))
	c.Health = &runtime.Health{Status: "healthy"}
	a, _ := Desired([]runtime.Container{c}, nil, opts())
	c.Health = &runtime.Health{Status: "unhealthy", ExitCode: 1}
	b, _ := Desired([]runtime.Container{c}, nil, opts())
	id := "dcr-abababababab-8080-tcp-ipv4"
	if a[id].Hash != b[id].Hash {
		t.Fatal("a health change changes the hash: it would register again")
	}
	o := opts()
	o.Tags = []string{"prod"}
	d, _ := Desired([]runtime.Container{c}, nil, o)
	if d[id].Hash == b[id].Hash || strings.Join(d[id].Reg.Tags, ",") != "prod,tcp" {
		t.Fatal("new tags do not change the hash")
	}
}

func TestWhatIsNotRegistered(t *testing.T) {
	stopped := web(tcp(80, 8080, ""))
	stopped.Running = false
	loop := web(tcp(80, 8080, "127.0.0.1"))
	loop.ID = strings.Repeat("cd", 32)
	none := web()
	none.ID = strings.Repeat("ef", 32)
	nameless := web(tcp(80, 1, ""))
	nameless.ID, nameless.Name = strings.Repeat("12", 32), "___"
	want, notes := Desired([]runtime.Container{stopped, loop, none, nameless}, nil, opts())
	if len(want) != 0 {
		t.Fatalf("registered: %s", ids(want))
	}
	var msgs []string
	for _, n := range notes {
		msgs = append(msgs, n.Msg)
	}
	joined := strings.Join(msgs, "|")
	for _, w := range []string{"loopback", "publishes no port", "no service name"} {
		if !strings.Contains(joined, w) {
			t.Errorf("no note about %q in %q", w, joined)
		}
	}
}

func TestASpecificAddress(t *testing.T) {
	want, _ := Desired([]runtime.Container{web(tcp(80, 8080, "192.168.1.200"), tcp(80, 8080, "::ffff:192.168.1.201"))}, nil, opts())
	if got := ids(want); got != "dcr-abababababab-8080-tcp-c0a801c8=web@192.168.1.200 dcr-abababababab-8080-tcp-c0a801c9=web@192.168.1.201" {
		t.Fatalf("got %s", got)
	}
}

func TestEachMeshTaskIsAnInstance(t *testing.T) {
	// Labelled on the service only: the containers carry no consul label.
	task := func(id string, h *runtime.Health) runtime.Container {
		return runtime.Container{ID: id, Name: "web-svc.1." + id[:4], Running: true, Health: h,
			Labels: map[string]string{runtime.LabelSwarmService: "web-svc", runtime.LabelSwarmServiceID: "svc123"}}
	}
	mesh := map[string][]runtime.Port{"svc123": {{Proto: "tcp", Target: 80, HostPort: 8181, Mode: "ingress"}}}
	sick := task(strings.Repeat("a", 64), &runtime.Health{Status: "unhealthy", ExitCode: 1})
	plain := task(strings.Repeat("b", 64), nil)
	want, notes := Desired([]runtime.Container{sick, plain}, mesh, opts())
	if len(notes) != 0 {
		t.Fatalf("notes: %+v", notes)
	}
	if got := ids(want); got != "dcr-aaaaaaaaaaaa-8181-tcp-ipv4=web-svc@192.168.1.93 dcr-bbbbbbbbbbbb-8181-tcp-ipv4=web-svc@192.168.1.93" {
		t.Fatalf("got %s", got)
	}
	a, b := want["dcr-aaaaaaaaaaaa-8181-tcp-ipv4"], want["dcr-bbbbbbbbbbbb-8181-tcp-ipv4"]
	if a.TTL == nil || a.TTL.Status != "critical" || a.Reg.Meta["published"] != "ingress" {
		t.Fatalf("the unhealthy task: %+v %+v", a.Reg, a.TTL)
	}
	// No health check: a TTL check that passes while the task runs - a TCP
	// check through the mesh would reach any task.
	if b.TTL == nil || b.TTL.Status != "passing" || b.Reg.Check.Name != "Container running" || b.Reg.Check.TCP != "" {
		t.Fatalf("the task without a health check: %+v %+v", b.Reg.Check, b.TTL)
	}
}

func TestLongNamesStayDNSLabels(t *testing.T) {
	c := web(tcp(80, 1, ""), tcp(8443, 2, ""))
	c.Name = strings.Repeat("x", 80)
	want, _ := Desired([]runtime.Container{c}, nil, Options{Addrs: Addrs{V4: dual.V4}, Check: config.CheckNone, Interval: time.Second})
	if len(want) != 2 {
		t.Fatalf("got %s", ids(want))
	}
	for _, w := range want {
		if w.Reg.Name != strings.Repeat("x", 63) {
			t.Errorf("name %q", w.Reg.Name)
		}
	}
}

func TestContainerdNamesMakeSafeIDs(t *testing.T) {
	if got := short("web-1"); len(got) != 12 || strings.Trim(got, "0123456789abcdef") != "" {
		t.Fatalf("short(web-1) = %q", got)
	}
	if got := short(strings.Repeat("ab", 32)); got != "abababababab" {
		t.Fatalf("short(hex) = %q", got)
	}
}
