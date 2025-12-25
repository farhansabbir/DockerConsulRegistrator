package data

import (
	"slices"
	"sync"
	"time"

	"github.com/docker/go-connections/nat"
)

var (
	mutex sync.Mutex
)

type RunningContainerQueue struct {
	RunningContainers []string `json:"-"`
	LastUpdated       int64    `json:"last_updated"`
}

type DeadContainerQueue struct {
	DeadContainers []string `json:"dead_containers"`
	LastUpdated    int64    `json:"last_updated"`
}

type ContainerDetails struct {
	ContainerID   string                `json:"container_id"`
	ContainerName string                `json:"container_name"`
	ServiceName   string                `json:"service_name"`
	Image         string                `json:"image"`
	Labels        []string              `json:"labels,omitempty"`
	PortMap       map[nat.Port][]string `json:"container_port_map,omitempty"`
	CreatedAt     int64                 `json:"created_at"`
}

func (status *RunningContainerQueue) AddContainerToCache(containerID string) {
	mutex.Lock()
	dt := time.Now().UnixMilli()
	defer mutex.Unlock()
	status.RunningContainers = append(status.RunningContainers, containerID)
	status.LastUpdated = dt
}

func (status *RunningContainerQueue) RemoveContainerFromCache(containerID string) bool {
	mutex.Lock()
	defer mutex.Unlock()
	if exists, idx := status.ContainerExistsInCache(containerID); exists {
		status.RunningContainers = append(status.RunningContainers[:idx], status.RunningContainers[idx+1:]...)
		status.LastUpdated = time.Now().UnixMilli()
		return true
	}
	return false
}

func (status *RunningContainerQueue) ContainerExistsInCache(containerID string) (bool, int) {
	mutex.Lock()
	defer mutex.Unlock()
	index := slices.Index(status.RunningContainers, containerID)
	return index != -1, index
}

func (status *RunningContainerQueue) GetRunningContainers() []string {
	mutex.Lock()
	defer mutex.Unlock()
	return status.RunningContainers
}
