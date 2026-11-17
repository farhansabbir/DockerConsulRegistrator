// Package containerd is containerd as a runtime.Runtime, over its gRPC API on
// its Unix socket. containerd itself knows nothing of published ports - the
// tool that made the container did the publishing - so the ports come from
// the label nerdctl writes them into (nerdctl/ports). Containers made by
// other tools carry no ports and are not registered; the registrator says so.
//
// Everything containerd answers is checked like any other input: label
// values, port numbers and addresses before use.
package containerd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"time"

	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	eventsapi "github.com/containerd/containerd/api/services/events/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	versionapi "github.com/containerd/containerd/api/services/version/v1"
	"github.com/containerd/containerd/api/types/task"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/safe"
)

// The labels nerdctl writes.
const (
	LabelPorts = "nerdctl/ports"
	LabelName  = "nerdctl/name"
)

const (
	maxMessage   = 16 << 20
	maxPortsJSON = 64 << 10
)

// Runtime is one containerd namespace.
type Runtime struct {
	conn      *grpc.ClientConn
	namespace string
	version   string
	hostname  func() (string, error)
}

// Open connects to containerd's socket at path and asks its version; an
// error when what answers is not containerd.
func Open(ctx context.Context, path, namespace string) (*Runtime, error) {
	conn, err := grpc.NewClient("unix://"+path,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithAuthority("localhost"),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxMessage)))
	if err != nil {
		return nil, fmt.Errorf("containerd: %w", err)
	}
	r := &Runtime{conn: conn, namespace: namespace, hostname: os.Hostname}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v, err := versionapi.NewVersionClient(conn).Version(r.ns(cctx), &emptypb.Empty{})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("containerd: %s", safe.Short(err.Error(), 200))
	}
	r.version = "containerd " + safe.Short(v.Version, 40)
	return r, nil
}

// Close closes the connection.
func (r *Runtime) Close() error { return r.conn.Close() }

func (r *Runtime) ns(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "containerd-namespace", r.namespace)
}

// Kind is runtime.Containerd.
func (r *Runtime) Kind() string { return runtime.Containerd }

// Version is containerd's version.
func (r *Runtime) Version() string { return r.version }

// Host is this machine: containerd does not say, so the registrator's own
// host name - run its container with the host's (--uts host, or --hostname).
func (r *Runtime) Host(context.Context) (runtime.Host, error) {
	name, err := r.hostname()
	if err != nil {
		return runtime.Host{}, err
	}
	return runtime.Host{Name: name}, nil
}

// Containers returns the containers of the namespace that carry label, with
// their tasks' state.
func (r *Runtime) Containers(ctx context.Context, label string) ([]runtime.Container, error) {
	ctx = r.ns(ctx)
	list, err := containersapi.NewContainersClient(r.conn).List(ctx, &containersapi.ListContainersRequest{
		Filters: []string{fmt.Sprintf("labels.%q", label)},
	})
	if err != nil {
		return nil, fmt.Errorf("containerd: listing containers: %s", safe.Short(err.Error(), 200))
	}
	tasks, err := tasksapi.NewTasksClient(r.conn).List(ctx, &tasksapi.ListTasksRequest{})
	if err != nil {
		return nil, fmt.Errorf("containerd: listing tasks: %s", safe.Short(err.Error(), 200))
	}
	running := map[string]bool{}
	for _, t := range tasks.Tasks {
		id := t.ContainerID
		if id == "" {
			id = t.ID
		}
		if t.Status == task.Status_RUNNING {
			running[id] = true
		}
	}
	var out []runtime.Container
	for _, c := range list.Containers {
		if _, ok := c.Labels[label]; !ok {
			continue
		}
		rc := runtime.Container{ID: c.ID, Name: c.ID, Labels: c.Labels, Running: running[c.ID]}
		if n := c.Labels[LabelName]; n != "" {
			rc.Name = n
		}
		if s, ok := c.Labels[LabelPorts]; ok {
			rc.Ports = ports(s)
		}
		out = append(out, rc)
	}
	return out, nil
}

// nerdctlPort is one entry of nerdctl's ports label (CNI's port mapping).
type nerdctlPort struct {
	HostPort      int    `json:"HostPort"`
	ContainerPort int    `json:"ContainerPort"`
	Protocol      string `json:"Protocol"`
	HostIP        string `json:"HostIP"`
}

// ports reads nerdctl's ports label; entries that are not ports are left
// out.
func ports(s string) []runtime.Port {
	if len(s) > maxPortsJSON {
		return nil
	}
	var in []nerdctlPort
	if json.Unmarshal([]byte(s), &in) != nil {
		return nil
	}
	var out []runtime.Port
	for _, p := range in {
		proto := strings.ToLower(p.Protocol)
		if proto == "" {
			proto = "tcp"
		}
		if proto != "tcp" && proto != "udp" && proto != "sctp" {
			continue
		}
		if p.HostPort < 1 || p.HostPort > 65535 || p.ContainerPort < 1 || p.ContainerPort > 65535 {
			continue
		}
		if p.HostIP != "" {
			if _, err := netip.ParseAddr(p.HostIP); err != nil {
				continue
			}
		}
		out = append(out, runtime.Port{Proto: proto, Target: p.ContainerPort, HostIP: p.HostIP, HostPort: p.HostPort})
	}
	return out
}

// Watch streams the namespace's task and container events. Exec events -
// commands run in a container - change nothing and are left out.
func (r *Runtime) Watch(ctx context.Context, label string, opened func(), changed func(string)) error {
	ns := fmt.Sprintf("namespace==%q", r.namespace)
	stream, err := eventsapi.NewEventsClient(r.conn).Subscribe(ctx, &eventsapi.SubscribeRequest{
		Filters: []string{ns + `,topic~="^/tasks/"`, ns + `,topic~="^/containers/"`},
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("containerd: events: %s", safe.Short(err.Error(), 200))
	}
	opened()
	for {
		e, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return errors.New("containerd: the event stream ended")
			}
			return fmt.Errorf("containerd: events: %s", safe.Short(err.Error(), 200))
		}
		if strings.Contains(e.Topic, "/exec-") {
			continue
		}
		changed(safe.Short(e.Topic, 60))
	}
}

// Ingress: containerd has no Swarm.
func (r *Runtime) Ingress(context.Context, string) ([]runtime.Port, error) {
	return nil, runtime.ErrUnsupported
}
