package containerd

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	eventsapi "github.com/containerd/containerd/api/services/events/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	versionapi "github.com/containerd/containerd/api/services/version/v1"
	"github.com/containerd/containerd/api/types"
	"github.com/containerd/containerd/api/types/task"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/runtime"
)

// A containerd with two containers in namespace "default", one type per
// service (their methods share names).
type (
	versionFake struct {
		versionapi.UnimplementedVersionServer
	}
	containersFake struct {
		containersapi.UnimplementedContainersServer
	}
	tasksFake struct {
		tasksapi.UnimplementedTasksServer
	}
	eventsFake struct {
		eventsapi.UnimplementedEventsServer
	}
)

func namespace(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("containerd-namespace"); len(v) == 1 {
		return v[0]
	}
	return ""
}

func (versionFake) Version(ctx context.Context, _ *emptypb.Empty) (*versionapi.VersionResponse, error) {
	return &versionapi.VersionResponse{Version: "v2.1.4"}, nil
}

func (containersFake) List(ctx context.Context, req *containersapi.ListContainersRequest) (*containersapi.ListContainersResponse, error) {
	if namespace(ctx) != "default" {
		return nil, status.Error(codes.FailedPrecondition, "namespace is required")
	}
	if len(req.Filters) != 1 || req.Filters[0] != `labels."consul"` {
		return nil, status.Errorf(codes.InvalidArgument, "filters %q", req.Filters)
	}
	return &containersapi.ListContainersResponse{Containers: []*containersapi.Container{
		{ID: "web-1", Labels: map[string]string{"consul": "yes", LabelName: "web-1",
			LabelPorts: `[{"HostPort":8080,"ContainerPort":80,"Protocol":"tcp","HostIP":"0.0.0.0"},{"HostPort":0,"ContainerPort":1},{"HostPort":5353,"ContainerPort":53,"Protocol":"UDP","HostIP":"x"}]`}},
		{ID: "stopped", Labels: map[string]string{"consul": "yes"}},
	}}, nil
}

func (tasksFake) List(ctx context.Context, _ *tasksapi.ListTasksRequest) (*tasksapi.ListTasksResponse, error) {
	return &tasksapi.ListTasksResponse{Tasks: []*task.Process{
		{ID: "web-1", ContainerID: "web-1", Status: task.Status_RUNNING},
		{ID: "stopped", Status: task.Status_STOPPED},
	}}, nil
}

func (eventsFake) Subscribe(req *eventsapi.SubscribeRequest, s eventsapi.Events_SubscribeServer) error {
	if len(req.Filters) != 2 || !strings.HasPrefix(req.Filters[0], `namespace=="default",`) {
		return status.Errorf(codes.InvalidArgument, "filters %q", req.Filters)
	}
	for _, topic := range []string{"/tasks/start", "/tasks/exec-added", "/tasks/exit"} {
		if err := s.Send(&types.Envelope{Namespace: "default", Topic: topic}); err != nil {
			return err
		}
	}
	return nil
}

func serve(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dcr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "containerd.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	versionapi.RegisterVersionServer(s, versionFake{})
	containersapi.RegisterContainersServer(s, containersFake{})
	tasksapi.RegisterTasksServer(s, tasksFake{})
	eventsapi.RegisterEventsServer(s, eventsFake{})
	go func() { _ = s.Serve(l) }()
	t.Cleanup(s.Stop)
	return sock
}

func TestContainers(t *testing.T) {
	r, err := Open(context.Background(), serve(t), "default")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	r.hostname = func() (string, error) { return "docker1", nil }
	if r.Kind() != runtime.Containerd || r.Version() != "containerd v2.1.4" {
		t.Fatalf("%s %s", r.Kind(), r.Version())
	}
	cs, err := r.Containers(context.Background(), "consul")
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("containers: %+v", cs)
	}
	web := cs[0]
	if web.Name != "web-1" || !web.Running || fmt.Sprint(web.Ports) != "[{tcp 80 0.0.0.0 8080}]" {
		t.Fatalf("web-1 = %+v", web)
	}
	if cs[1].Running {
		t.Fatal("a stopped task counts as running")
	}
	if h, _ := r.Host(context.Background()); h.Name != "docker1" || h.SwarmAddr != "" {
		t.Fatalf("Host = %+v", h)
	}
	if _, err := r.Ingress(context.Background(), "x"); err != runtime.ErrUnsupported {
		t.Fatalf("Ingress = %v", err)
	}
}

func TestWatchLeavesExecsOut(t *testing.T) {
	r, err := Open(context.Background(), serve(t), "default")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	var got []string
	opened := false
	err = r.Watch(context.Background(), "consul", func() { opened = true }, func(s string) { got = append(got, s) })
	if err == nil || !opened {
		t.Fatalf("Watch = %v, opened %v", err, opened)
	}
	if strings.Join(got, ",") != "/tasks/start,/tasks/exit" {
		t.Fatalf("changes: %q", got)
	}
}

func TestOpenRefusesWhatIsNotContainerd(t *testing.T) {
	dir, _ := os.MkdirTemp("/tmp", "dcr")
	defer func() { _ = os.RemoveAll(dir) }()
	sock := filepath.Join(dir, "http.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
			_ = c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Open(ctx, sock, "default"); err == nil {
		t.Fatal("an HTTP server passed for containerd")
	}
}

func TestPortsLabel(t *testing.T) {
	for in, want := range map[string]string{
		`[{"HostPort":53,"ContainerPort":53,"Protocol":"udp"}]`: "[{udp 53  53}]",
		`[{"HostPort":70000,"ContainerPort":80}]`:               "[]",
		`not json`: "[]",
		`[{"HostPort":80,"ContainerPort":80,"Protocol":"icmp"}]`: "[]",
		`[{"HostPort":80,"ContainerPort":80,"HostIP":"::"}]`:     "[{tcp 80 :: 80}]",
		strings.Repeat(" ", maxPortsJSON+1):                      "[]",
	} {
		if got := fmt.Sprint(ports(in)); got != want {
			t.Errorf("ports(%.40q) = %s, want %s", in, got, want)
		}
	}
}
