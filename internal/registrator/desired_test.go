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
	two := web(tcp(80, 8080, ""), tcp(443, 8443, ""))
	dns := runtime.Container{ID: strings.Repeat("cd", 32), Name: "/dns", Labels: map[string]string{Label: "yes"}, Running: true,
		Ports: []runtime.Port{{Proto: "tcp", Target: 53, HostPort: 53}, {Proto: "udp", Target: 53, HostPort: 53}}}
	swarm := web(tcp(80, 9090, "0.0.0.0"))
	swarm.ID = strings.Repeat("ef", 32)
	swarm.Labels = map[string]string{Label: "yes", runtime.LabelSwarmService: "MyStack_Web"}
	compose := web(tcp(80, 7070, ""))
	compose.ID = strings.Repeat("12", 32)
	compose.Labels = map[string]string{Label: "yes", runtime.LabelComposeProject: "shop", runtime.LabelComposeService: "api"}
	o := opts()
	o.Addrs.V6 = netip.Addr{}
	want, _ := Desired([]runtime.Container{two, dns, swarm, compose}, nil, o)
	names := map[string]string{}
	for _, w := range want {
		names[w.Reg.Name+"/"+w.Reg.Tags[len(w.Reg.Tags)-1]] = w.Reg.ID
	}
	for _, n := range []string{"web-80/tcp", "web-443/tcp", "dns/tcp", "dns/udp", "mystack-web/tcp", "shop-api/tcp"} {
		if names[n] == "" {
			t.Errorf("no %s in %v", n, names)
		}
	}
	for _, w := range want {
		if w.Reg.Name == "dns" && w.Reg.Tags[0] == "udp" && w.Reg.Check != nil {
			t.Errorf("a UDP port without a health check got a check: %+v", w.Reg.Check)
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
	if got := ids(want); got != "dcr-abababababab-8080-tcp-ipv4=web@192.168.1.200 dcr-abababababab-8080-tcp-ipv4=web@192.168.1.201" &&
		!strings.Contains(got, "@192.168.1.200") {
		t.Fatalf("got %s", got)
	}
}

func TestSwarmMeshPortsOnceForAllTasks(t *testing.T) {
	task := func(id, health string) runtime.Container {
		return runtime.Container{ID: id, Name: "web.1." + id[:4], Running: true, Health: &runtime.Health{Status: health},
			Labels: map[string]string{Label: "yes", runtime.LabelSwarmService: "web", runtime.LabelSwarmServiceID: "svc123"}}
	}
	ingress := map[string][]runtime.Port{"svc123": {{Proto: "tcp", Target: 80, HostPort: 8080}}}
	want, notes := Desired([]runtime.Container{task(strings.Repeat("a", 64), "unhealthy"), task(strings.Repeat("b", 64), "healthy")}, ingress, opts())
	if len(notes) != 0 || len(want) != 1 {
		t.Fatalf("got %s %+v", ids(want), notes)
	}
	w := want["dcr-swarm-svc123-8080-tcp-ipv4"]
	if w.Reg.Name != "web" || w.TTL == nil || w.TTL.Status != "passing" || w.Reg.Meta["published"] != "ingress" {
		t.Fatalf("mesh registration: %+v %+v", w.Reg, w.TTL)
	}
}

func TestLongNamesStayDNSLabels(t *testing.T) {
	c := web(tcp(80, 1, ""), tcp(8443, 2, ""))
	c.Name = strings.Repeat("x", 80)
	want, _ := Desired([]runtime.Container{c}, nil, Options{Addrs: Addrs{V4: dual.V4}, Check: config.CheckNone, Interval: time.Second})
	for _, w := range want {
		if len(w.Reg.Name) > 63 || !strings.HasSuffix(w.Reg.Name, "-80") && !strings.HasSuffix(w.Reg.Name, "-8443") {
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
