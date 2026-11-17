// Package runtime is how the registrator sees a container runtime - Docker,
// Podman or containerd - whichever is behind the socket it was given: the
// running containers that carry a label, their published ports and health,
// and a stream of changes. Each runtime's client describes its containers in
// these terms; the registrator never sees a runtime's own types.
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

// ErrUnsupported is a question a runtime cannot answer, such as Swarm's
// routing-mesh ports on Podman.
var ErrUnsupported = errors.New("not supported by this runtime")

// Runtime is one container runtime.
type Runtime interface {
	// Kind is Docker, Podman or Containerd.
	Kind() string
	// Version is the runtime's own version, for the log.
	Version() string
	// Host describes the machine the runtime runs on.
	Host(ctx context.Context) (Host, error)
	// Containers returns the running containers that carry label (its key,
	// any value), paused ones included - Running says which.
	Containers(ctx context.Context, label string) ([]Container, error)
	// Watch calls opened once the runtime streams its changes, then changed
	// for each change to a container that carries label, until the stream
	// ends: an error, or nil when ctx is done.
	Watch(ctx context.Context, label string, opened func(), changed func(what string)) error
	// Ingress returns the ports a Swarm service publishes through the
	// routing mesh; ErrUnsupported where there is no Swarm, or this node
	// cannot ask.
	Ingress(ctx context.Context, serviceID string) ([]Port, error)
}

// Host is the machine a runtime runs on.
type Host struct {
	Name         string // its host name
	SwarmAddr    string // its address in a Swarm, "" outside one
	SwarmManager bool
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

// Port is a container port published on the host.
type Port struct {
	Proto    string // tcp, udp or sctp
	Target   int    // the container's port
	HostIP   string // "" or an unspecified address: all of the host's
	HostPort int
}

// The labels runtimes and their tools put on containers that the
// registrator reads to name services.
const (
	LabelSwarmService   = "com.docker.swarm.service.name"
	LabelSwarmServiceID = "com.docker.swarm.service.id"
	LabelComposeProject = "com.docker.compose.project"
	LabelComposeService = "com.docker.compose.service"
)
