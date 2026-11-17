package docker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
)

// Engine is a Docker or Podman daemon, as a runtime.Runtime.
type Engine struct {
	c       *Client
	kind    string
	version string
}

// Open connects to the Engine API at endpoint and finds out whether Docker or
// Podman answers.
func Open(ctx context.Context, endpoint string) (*Engine, error) {
	c, err := New(endpoint)
	if err != nil {
		return nil, err
	}
	p, err := c.Ping(ctx)
	if err != nil {
		return nil, err
	}
	e := &Engine{c: c, kind: kindOf(p), version: p.Server}
	if e.version == "" {
		e.version = "API " + p.APIVersion
	}
	return e, nil
}

// kindOf is Podman when the daemon says it is Libpod, else Docker.
func kindOf(p Ping) string {
	if p.LibpodVersion != "" || strings.HasPrefix(p.Server, "Libpod") {
		return runtime.Podman
	}
	return runtime.Docker
}

// Kind is runtime.Docker or runtime.Podman.
func (e *Engine) Kind() string { return e.kind }

// Version is what the daemon calls itself.
func (e *Engine) Version() string { return e.version }

// Host describes the daemon's machine.
func (e *Engine) Host(ctx context.Context) (runtime.Host, error) {
	i, err := e.c.Info(ctx)
	if err != nil {
		return runtime.Host{}, err
	}
	h := runtime.Host{Name: i.Name}
	if i.Swarm.LocalNodeState == "active" {
		h.SwarmAddr = i.Swarm.NodeAddr
		h.SwarmManager = i.Swarm.ControlAvailable
	}
	return h, nil
}

// Containers returns the running containers that carry label.
func (e *Engine) Containers(ctx context.Context, label string) ([]runtime.Container, error) {
	list, err := e.c.Containers(ctx, label)
	if err != nil {
		return nil, err
	}
	out := make([]runtime.Container, 0, len(list))
	for _, s := range list {
		c, err := e.c.Inspect(ctx, s.ID)
		if errors.Is(err, ErrNotFound) {
			continue // gone since the list
		}
		if err != nil {
			return nil, err
		}
		out = append(out, convert(c))
	}
	return out, nil
}

func convert(c Container) runtime.Container {
	rc := runtime.Container{
		ID:      c.ID,
		Name:    strings.TrimPrefix(c.Name, "/"),
		Labels:  c.Config.Labels,
		Running: c.State.Running && !c.State.Paused && !c.State.Restarting,
	}
	if h := c.State.Health; h != nil && h.Status != "" && h.Status != "none" {
		rh := &runtime.Health{Status: h.Status}
		if n := len(h.Log); n > 0 {
			rh.ExitCode, rh.Output = h.Log[n-1].ExitCode, h.Log[n-1].Output
		}
		rc.Health = rh
	}
	for key, binds := range c.NetworkSettings.Ports {
		target, proto, ok := portKey(key)
		if !ok {
			continue
		}
		for _, b := range binds {
			hp, err := strconv.Atoi(b.HostPort)
			if err != nil || hp < 1 || hp > 65535 {
				continue
			}
			rc.Ports = append(rc.Ports, runtime.Port{Proto: proto, Target: target, HostIP: b.HostIP, HostPort: hp})
		}
	}
	sort.Slice(rc.Ports, func(i, j int) bool {
		a, b := rc.Ports[i], rc.Ports[j]
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		if a.Proto != b.Proto {
			return a.Proto < b.Proto
		}
		if a.HostPort != b.HostPort {
			return a.HostPort < b.HostPort
		}
		return a.HostIP < b.HostIP
	})
	return rc
}

// portKey parses Docker's "80/tcp".
func portKey(k string) (port int, proto string, ok bool) {
	p, proto, found := strings.Cut(k, "/")
	if !found {
		return 0, "", false
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return 0, "", false
	}
	switch proto {
	case "tcp", "udp", "sctp":
		return n, proto, true
	}
	return 0, "", false
}

// Watch streams the changes to the containers that carry label. Health
// checks run as execs; their exec events are left out.
func (e *Engine) Watch(ctx context.Context, label string, opened func(), changed func(string)) error {
	return e.c.Events(ctx, label, opened, func(ev Event) {
		if strings.HasPrefix(ev.Action, "exec_") {
			return
		}
		changed(ev.Action)
	})
}

// Ingress returns the ports a Swarm service publishes through the routing
// mesh. Only a Docker manager can answer.
func (e *Engine) Ingress(ctx context.Context, serviceID string) ([]runtime.Port, error) {
	if e.kind != runtime.Docker {
		return nil, runtime.ErrUnsupported
	}
	svc, err := e.c.Service(ctx, serviceID)
	if err != nil {
		if strings.Contains(err.Error(), "not a swarm manager") {
			return nil, fmt.Errorf("%w: this node is a Swarm worker", runtime.ErrUnsupported)
		}
		return nil, err
	}
	var out []runtime.Port
	for _, p := range svc.Endpoint.Ports {
		if p.PublishMode != "" && p.PublishMode != "ingress" {
			continue // host mode: the task's container shows it
		}
		proto := strings.ToLower(p.Protocol)
		if proto == "" {
			proto = "tcp"
		}
		if p.PublishedPort < 1 || p.PublishedPort > 65535 || p.TargetPort < 1 || p.TargetPort > 65535 {
			continue
		}
		out = append(out, runtime.Port{Proto: proto, Target: p.TargetPort, HostPort: p.PublishedPort})
	}
	return out, nil
}
