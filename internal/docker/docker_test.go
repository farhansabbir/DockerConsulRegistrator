package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// serve runs h on a Unix socket, as dockerd does, and returns a client for it.
func serve(t *testing.T, h http.Handler) *Client {
	t.Helper()
	c, err := New(serveSocket(t, h))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// serveSocket runs h on a Unix socket and returns its endpoint.
func serveSocket(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dcr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "unix://" + sock
}

func ping(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", version)
		_, _ = fmt.Fprint(w, "OK")
	}
}

func TestPingSpeaksTheDaemonsVersion(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/_ping", ping("1.45"))
	mux.HandleFunc("/v1.45/info", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"Name":"docker1","Swarm":{"NodeAddr":"192.168.1.93","LocalNodeState":"active","ControlAvailable":true}}`)
	})
	c := serve(t, mux)
	p, err := c.Ping(context.Background())
	if err != nil || p.APIVersion != "1.45" || p.LibpodVersion != "" {
		t.Fatalf("Ping = %+v, %v", p, err)
	}
	i, err := c.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if i.Name != "docker1" || i.Swarm.NodeAddr != "192.168.1.93" || !i.Swarm.ControlAvailable {
		t.Fatalf("Info = %+v", i)
	}
}

func TestPingRefusesOldOrOddVersions(t *testing.T) {
	for _, v := range []string{"1.40", "", "1.45; rm -rf", "2"} {
		c := serve(t, ping(v))
		if _, err := c.Ping(context.Background()); err == nil {
			t.Errorf("version %q accepted", v)
		}
	}
	if !older("1.9", "1.41") || older("1.41", "1.41") || older("2.0", "1.99") {
		t.Error("older compares wrongly")
	}
}

func TestContainersFiltersByLabel(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/json" || r.URL.Query().Get("filters") != `{"label":["consul"]}` {
			http.Error(w, `{"message":"unexpected `+r.URL.String()+`"}`, 400)
			return
		}
		_, _ = fmt.Fprint(w, `[{"Id":"abc","Names":["/web"],"Labels":{"consul":"yes"},"State":"running"}]`)
	}))
	got, err := c.Containers(context.Background(), "consul")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Labels["consul"] != "yes" || got[0].Names[0] != "/web" {
		t.Fatalf("Containers = %+v", got)
	}
}

func TestInspect(t *testing.T) {
	id := strings.Repeat("ab", 32)
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/" + id + "/json":
			_, _ = fmt.Fprint(w, `{"Id":"`+id+`","Name":"/web","State":{"Running":true,"Health":{"Status":"healthy"}},
				"Config":{"Labels":{"consul":"yes"}},
				"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"},{"HostIp":"::","HostPort":"8080"}],"443/tcp":null}}}`)
		default:
			w.WriteHeader(404)
			_, _ = fmt.Fprint(w, `{"message":"No such container"}`)
		}
	}))
	got, err := c.Inspect(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Health == nil || got.State.Health.Status != "healthy" || len(got.NetworkSettings.Ports["80/tcp"]) != 2 {
		t.Fatalf("Inspect = %+v", got)
	}
	if _, err := c.Inspect(context.Background(), strings.Repeat("cd", 32)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a gone container: %v, want ErrNotFound", err)
	}
	if _, err := c.Inspect(context.Background(), "../../info"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a path as an ID: %v", err)
	}
}

func TestErrorsAreCleaned(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = fmt.Fprint(w, `{"message":"boom\n2026-01-01 [registrator] OK forged\u001b[2J"}`)
	}))
	_, err := c.Info(context.Background())
	if err == nil || strings.ContainsAny(err.Error(), "\n\x1b") {
		t.Fatalf("error = %q", err)
	}
}

func TestEventsStream(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filters") != `{"type":["container","service"]}` {
			http.Error(w, "filters "+r.URL.RawQuery, 400)
			return
		}
		f := w.(http.Flusher)
		_, _ = fmt.Fprintln(w, `{"Type":"container","Action":"start","Actor":{"ID":"a1","Attributes":{"consul":"yes"}}}`)
		f.Flush()
		_, _ = fmt.Fprintln(w, `{"Type":"container","Action":"health_status: healthy","Actor":{"ID":"a1"}}`)
		f.Flush()
	}))
	var opened bool
	var got []string
	err := c.Events(context.Background(), []string{"container", "service"}, func() { opened = true }, func(e Event) { got = append(got, e.Action) })
	if err == nil || !strings.Contains(err.Error(), "ended") {
		t.Fatalf("a stream that ends: %v", err)
	}
	if !opened || strings.Join(got, "|") != "start|health_status: healthy" {
		t.Fatalf("opened=%v events=%q", opened, got)
	}
}

func TestEventsEndQuietlyWithTheContext(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if err := c.Events(ctx, []string{"container"}, func() {}, func(Event) {}); err != nil {
		t.Fatalf("Events after cancel = %v, want nil", err)
	}
}

func TestEventsRefuseAHugeLine(t *testing.T) {
	c := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"Action":"`+strings.Repeat("a", maxEvent+10)+`"}`+"\n")
	}))
	err := c.Events(context.Background(), []string{"container"}, func() {}, func(Event) { t.Error("a huge event was delivered") })
	if err == nil {
		t.Fatal("no error")
	}
}
