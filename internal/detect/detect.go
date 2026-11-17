// Package detect finds the container runtime behind a socket. A socket is
// known by how it answers, not by its path: the Engine API's /_ping answers
// Docker and Podman (Podman says Libpod), and what does not speak HTTP/1.1
// but answers containerd's gRPC version call is containerd. With --socket
// auto, the paths runtimes keep their sockets at are tried in turn - mount
// the host's socket into the registrator's container at its usual path and
// it is found.
package detect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/containerd"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/docker"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/safe"
)

// Candidates are the sockets tried with --socket auto, in order.
var Candidates = []string{
	"/var/run/docker.sock",
	"/run/docker.sock",
	"/run/podman/podman.sock",
	"/var/run/podman/podman.sock",
	"/run/user/*/podman/podman.sock", // rootless Podman
	"/run/containerd/containerd.sock",
	"/var/run/containerd/containerd.sock",
}

// Open returns the runtime at socket (config.SocketAuto: the first
// candidate that answers), which must be kind unless kind is
// config.RuntimeAuto, and the socket it answered on.
func Open(ctx context.Context, socket, kind, namespace string) (runtime.Runtime, string, error) {
	if socket != config.SocketAuto {
		rt, err := open(ctx, socket, kind, namespace)
		return rt, socket, err
	}
	var errs []error
	tried := 0
	for _, pattern := range Candidates {
		paths, _ := filepath.Glob(pattern)
		sort.Strings(paths)
		for _, p := range paths {
			fi, err := os.Stat(p)
			if err != nil || fi.Mode()&os.ModeSocket == 0 {
				continue
			}
			tried++
			rt, err := open(ctx, p, kind, namespace)
			if err == nil {
				return rt, p, nil
			}
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
		}
	}
	if tried == 0 {
		return nil, "", fmt.Errorf("no container runtime's socket in this container (looked for %v); mount the host's, e.g. -v /var/run/docker.sock:/var/run/docker.sock", Candidates)
	}
	return nil, "", errors.Join(errs...)
}

func open(ctx context.Context, socket, kind, namespace string) (runtime.Runtime, error) {
	network, addr, err := config.Endpoint(socket)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch kind {
	case config.RuntimeContainerd:
		return containerd.Open(pctx, addr, namespace)
	case config.RuntimeDocker, config.RuntimePodman:
		e, err := docker.Open(pctx, socket)
		if err != nil {
			return nil, err
		}
		if e.Kind() != kind {
			return nil, fmt.Errorf("the socket answers as %s (%s), not %s", e.Kind(), safe.Short(e.Version(), 60), kind)
		}
		return e, nil
	}
	e, engineErr := docker.Open(pctx, socket)
	if engineErr == nil {
		return e, nil
	}
	if network != "unix" {
		return nil, engineErr
	}
	cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
	defer ccancel()
	c, err := containerd.Open(cctx, addr, namespace)
	if err != nil {
		return nil, fmt.Errorf("neither the Engine API (%v) nor containerd (%v) answers", engineErr, err)
	}
	return c, nil
}
