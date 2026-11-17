package detect

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	versionapi "github.com/containerd/containerd/api/services/version/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
)

func socketDir(t *testing.T) string {
	dir, err := os.MkdirTemp("/tmp", "dcr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func engine(t *testing.T, path string, headers map[string]string) {
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		_, _ = fmt.Fprint(w, "OK")
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
}

type version struct {
	versionapi.UnimplementedVersionServer
}

func (version) Version(context.Context, *emptypb.Empty) (*versionapi.VersionResponse, error) {
	return &versionapi.VersionResponse{Version: "v2.1.4"}, nil
}

func containerdAt(t *testing.T, path string) {
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	versionapi.RegisterVersionServer(s, version{})
	go func() { _ = s.Serve(l) }()
	t.Cleanup(s.Stop)
}

func TestEachSocketIsKnownByHowItAnswers(t *testing.T) {
	dir := socketDir(t)
	dockerSock := filepath.Join(dir, "a.sock")
	podmanSock := filepath.Join(dir, "b.sock")
	containerdSock := filepath.Join(dir, "c.sock")
	engine(t, dockerSock, map[string]string{"Api-Version": "1.45", "Server": "Docker/26.1.5 (linux)"})
	engine(t, podmanSock, map[string]string{"Api-Version": "1.41", "Libpod-Api-Version": "5.4.0", "Server": "Libpod/5.4.0 (linux)"})
	containerdAt(t, containerdSock)
	for sock, want := range map[string]string{dockerSock: runtime.Docker, podmanSock: runtime.Podman, containerdSock: runtime.Containerd} {
		rt, at, err := Open(context.Background(), sock, config.RuntimeAuto, "default")
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		if rt.Kind() != want || at != sock {
			t.Errorf("%s answered as %s", want, rt.Kind())
		}
	}
	if _, _, err := Open(context.Background(), podmanSock, config.RuntimeDocker, "default"); err == nil {
		t.Error("--runtime docker took a Podman socket")
	}
}

func TestAutoTriesTheCandidates(t *testing.T) {
	dir := socketDir(t)
	old := Candidates
	t.Cleanup(func() { Candidates = old })
	missing := filepath.Join(dir, "missing.sock")
	notASocket := filepath.Join(dir, "file.sock")
	if err := os.WriteFile(notASocket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	containerdSock := filepath.Join(dir, "containerd.sock")
	containerdAt(t, containerdSock)
	Candidates = []string{missing, notASocket, filepath.Join(dir, "*", "nothing.sock"), containerdSock}
	rt, at, err := Open(context.Background(), config.SocketAuto, config.RuntimeAuto, "default")
	if err != nil {
		t.Fatal(err)
	}
	if rt.Kind() != runtime.Containerd || at != containerdSock {
		t.Fatalf("found %s at %s", rt.Kind(), at)
	}
	Candidates = []string{missing}
	if _, _, err := Open(context.Background(), config.SocketAuto, config.RuntimeAuto, "default"); err == nil {
		t.Fatal("no socket: no error")
	}
}
