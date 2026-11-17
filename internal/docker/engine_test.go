package docker

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
)

func TestOpenTellsDockerFromPodman(t *testing.T) {
	for want, headers := range map[string]map[string]string{
		runtime.Docker: {"Api-Version": "1.45", "Server": "Docker/26.1.5 (linux)"},
		runtime.Podman: {"Api-Version": "1.41", "Libpod-Api-Version": "5.4.0", "Server": "Libpod/5.4.0 (linux)"},
	} {
		endpoint := serveSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range headers {
				w.Header().Set(k, v)
			}
			_, _ = fmt.Fprint(w, "OK")
		}))
		e, err := Open(context.Background(), endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind() != want || e.Version() != headers["Server"] {
			t.Errorf("headers %v: %s %q, want %s", headers, e.Kind(), e.Version(), want)
		}
	}
}

func TestConvert(t *testing.T) {
	var c Container
	c.ID = strings.Repeat("a", 64)
	c.Name = "/web"
	c.State.Running = true
	c.State.Health = &Health{Status: "unhealthy", Log: []struct {
		ExitCode int
		Output   string
	}{{0, "ok"}, {1, "connection refused"}}}
	c.NetworkSettings.Ports = map[string][]PortBinding{
		"80/tcp":    {{HostIP: "0.0.0.0", HostPort: "8080"}, {HostIP: "::", HostPort: "8080"}},
		"53/udp":    {{HostIP: "", HostPort: "5353"}},
		"443/tcp":   nil, // exposed, not published
		"9/tcp":     {{HostIP: "0.0.0.0", HostPort: ""}},
		"bad":       {{HostPort: "1"}},
		"70000/tcp": {{HostPort: "1"}},
	}
	got := convert(c)
	if got.Name != "web" || !got.Running {
		t.Fatalf("convert = %+v", got)
	}
	if got.Health == nil || got.Health.Status != "unhealthy" || got.Health.ExitCode != 1 || got.Health.Output != "connection refused" {
		t.Fatalf("health = %+v", got.Health)
	}
	want := []runtime.Port{
		{Proto: "udp", Target: 53, HostIP: "", HostPort: 5353},
		{Proto: "tcp", Target: 80, HostIP: "0.0.0.0", HostPort: 8080},
		{Proto: "tcp", Target: 80, HostIP: "::", HostPort: 8080},
	}
	if fmt.Sprint(got.Ports) != fmt.Sprint(want) {
		t.Fatalf("ports = %v\nwant    %v", got.Ports, want)
	}
	c.State.Paused = true
	if convert(c).Running {
		t.Fatal("a paused container counts as running")
	}
}

func TestWatchFiltersHere(t *testing.T) {
	endpoint := serveSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_ping":
			w.Header().Set("Api-Version", "1.45")
		default:
			for _, line := range []string{
				`{"Type":"container","Action":"start","Actor":{"Attributes":{"consul":"yes"}}}`,
				`{"Type":"container","Action":"start","Actor":{"Attributes":{"name":"unrelated"}}}`,
				`{"Type":"container","Action":"exec_start: wget","Actor":{"Attributes":{"consul":"yes"}}}`,
				`{"Type":"container","Action":"die","Actor":{"Attributes":{"com.docker.swarm.service.id":"s1"}}}`,
				`{"Type":"service","Action":"update","Actor":{"ID":"s1"}}`,
			} {
				_, _ = fmt.Fprintln(w, line)
			}
		}
	}))
	e, err := Open(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	_ = e.Watch(context.Background(), []string{"consul", "com.docker.swarm.service.id"}, func() {}, func(s string) { got = append(got, s) })
	if strings.Join(got, "|") != "start|die|service update" {
		t.Fatalf("changes: %q", got)
	}
}

func TestSwarmServices(t *testing.T) {
	endpoint := serveSocket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_ping":
			w.Header().Set("Api-Version", "1.45")
		case "/v1.45/services":
			_, _ = fmt.Fprint(w, `[{"ID":"svc1","Spec":{"Name":"web-svc","Labels":{"consul":"yes"},"TaskTemplate":{"ContainerSpec":{"Labels":{"a":"b"}}}},
				"Endpoint":{"Ports":[{"Protocol":"tcp","TargetPort":80,"PublishedPort":8181,"PublishMode":"ingress"},
				{"Protocol":"udp","TargetPort":53,"PublishedPort":5353,"PublishMode":"host"},{"TargetPort":0,"PublishedPort":1}]}}]`)
		}
	}))
	e, err := Open(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.SwarmServices(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("SwarmServices = %+v, %v", got, err)
	}
	s := got[0]
	if s.Name != "web-svc" || s.Labels["consul"] != "yes" || s.ContainerLabels["a"] != "b" ||
		fmt.Sprint(s.Ports) != "[{tcp 80  8181 ingress} {udp 53  5353 host}]" {
		t.Fatalf("service = %+v", s)
	}
}
