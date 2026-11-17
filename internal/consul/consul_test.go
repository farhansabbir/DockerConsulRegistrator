package consul

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheTokenComesFromTheFileAndFollowsIt(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("X-Consul-Token"))
		_, _ = fmt.Fprint(w, `{"Config":{"NodeName":"docker1"},"Member":{"Addr":"192.168.1.93"}}`)
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("first-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{Addr: srv.URL, TokenFile: file})
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Self(context.Background())
	if err != nil || s.Member.Addr != "192.168.1.93" {
		t.Fatalf("Self = %+v, %v", s, err)
	}
	later := time.Now().Add(time.Second)
	if err := os.WriteFile(file, []byte("second-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Self(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "first-token,second-token" {
		t.Fatalf("tokens sent: %q", got)
	}
}

func TestTokenFilesRefused(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"empty":     "\n",
		"two":       "a b",
		"control":   "tok\x1ben",
		"too large": strings.Repeat("a", maxTokenFile+1),
	} {
		f := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(Options{Addr: "127.0.0.1:8500", TokenFile: f}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := New(Options{Addr: "127.0.0.1:8500", TokenFile: dir}); err == nil {
		t.Error("a directory as the token file: accepted")
	}
}

func TestRegisterAndDeregister(t *testing.T) {
	var paths []string
	var reg Registration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		if r.URL.Path == "/v1/agent/service/register" {
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, &reg); err != nil {
				t.Error(err)
			}
		}
	}))
	defer srv.Close()
	c, err := New(Options{Addr: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	r := Registration{ID: "dcr-abc-8080-tcp", Name: "web", Port: 8080, Address: "192.168.1.93",
		Check: &Check{Name: "tcp", TCP: "192.168.1.93:8080", Interval: "10s", Timeout: "2s"}}
	if err := c.Register(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err := c.Deregister(context.Background(), "dcr/../x"); err != nil {
		t.Fatal(err)
	}
	if err := c.UpdateTTL(context.Background(), CheckID("dcr-abc-8080-tcp"), "passing", "healthy"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"PUT /v1/agent/service/register?replace-existing-checks=true",
		"PUT /v1/agent/service/deregister/dcr%2F..%2Fx",
		"PUT /v1/agent/check/update/service:dcr-abc-8080-tcp",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(paths, "\n"), strings.Join(want, "\n"))
	}
	if reg.Name != "web" || reg.Check == nil || reg.Check.TCP != "192.168.1.93:8080" {
		t.Fatalf("registered %+v", reg)
	}
}

func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agent/self":
			http.Error(w, "Permission denied: token lacks agent:read\x1b[2J", http.StatusForbidden)
		default:
			http.Error(w, "Unknown check", 404)
		}
	}))
	defer srv.Close()
	c, _ := New(Options{Addr: srv.URL})
	_, err := c.Self(context.Background())
	if !errors.Is(err, ErrDenied) || strings.ContainsRune(err.Error(), 0x1b) {
		t.Fatalf("403: %q", err)
	}
	if err := c.UpdateTTL(context.Background(), "service:x", "passing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
}
