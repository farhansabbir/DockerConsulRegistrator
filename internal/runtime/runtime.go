// Package runtime is how the registrator sees a container runtime - Docker,
// Podman or containerd - whichever is behind the socket it was given: the
// containers that carry a label, their published ports and health, a
// Swarm's services where there is one, and a stream of changes. Each
// runtime's client describes its containers in these terms; the registrator
// never sees a runtime's own types.
package runtime

import (
	"context"
	"errors"
)

// The runtimes.
const (
	Docker     = "docker"
	Podman     = "podman"
	Containerd = "containerd"
)

// ErrUnsupported is a question a runtime cannot answer, such as a Swarm's
// services on Podman, or on a Swarm worker.
var ErrUnsupported = errors.New("not supported here")

// Runtime is one container runtime.
type Runtime interface {
	// Kind is Docker, Podman or Containerd.
	Kind() string
	// Version is the runtime's own version, for the log.
	Version() string
	// Host describes the machine the runtime runs on.
	Host(ctx context.Context) (Host, error)
	// Containers returns the containers that carry any of labels (keys,
	// any value), paused ones included - Running says which.
	Containers(ctx context.Context, labels ...string) ([]Container, error)
	// Watch calls opened once the runtime streams its changes, then changed
	// for each change to a container that carries any of labels - and to a
	// Swarm service, where there is a Swarm - until the stream ends: an
	// error, or nil when ctx is done.
	Watch(ctx context.Context, labels []string, opened func(), changed func(what string)) error
	// SwarmServices returns the Swarm's services. Only a Swarm manager can
	// answer; ErrUnsupported elsewhere.
	SwarmServices(ctx context.Context) ([]SwarmService, error)
}

// Host is the machine a runtime runs on.
type Host struct {
	Name         string // its host name
	SwarmAddr    string // its address in a Swarm, "" outside one
	SwarmManager bool
	SwarmCluster string // the Swarm's ID, known to managers only
}

// Container is a container as the registrator needs it.
type Container struct {
	ID      string
	Name    string
	Labels  map[string]string
	Running bool    // running, and not paused or restarting
	Health  *Health // nil: no health check
	Ports   []Port  // where it is published on the host
}

// Health is a container's own health check, as its runtime last ran it.
type Health struct {
	Status   string // starting, healthy or unhealthy
	ExitCode int
	Output   string
}

// Port is a published port.
type Port struct {
	Proto    string // tcp, udp or sctp
	Target   int    // the container's port
	HostIP   string // "" or an unspecified address: all of the host's
	HostPort int
	Mode     string // a Swarm service's: "ingress" (the routing mesh) or "host"
}

// SwarmService is a Swarm service as a manager sees it.
type SwarmService struct {
	ID              string
	Name            string
	Labels          map[string]string // the service's own labels
	ContainerLabels map[string]string // the labels its tasks' containers get
	Ports           []Port            // what it publishes; Mode says how
}

// The labels runtimes put on containers that the registrator reads.
const (
	LabelSwarmService   = "com.docker.swarm.service.name"
	LabelSwarmServiceID = "com.docker.swarm.service.id"
)
