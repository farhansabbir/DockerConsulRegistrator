package container

import (
	"context"
	"sync"
	"time"
)

var (
	mutex  sync.Mutex
	status *Status
)

type Status struct {
	runningContainers map[string]int `json:"-"`
	LastUpdated       int64          `json:"last_updated"`
}

type ContainerDetails struct {
	ContainerID   string   `json:"container_id"`
	ContainerName string   `json:"container_name"`
	ServiceName   string   `json:"service_name"`
	Image         string   `json:"image"`
	Tags          []string `json:"tags,omitempty"`
	ContainerPort int      `json:"container_port,omitempty"`
	ForwardedPort int      `json:"forwarded_port,omitempty"`
}

func NewStatus() *Status {
	once.Do(func() {
		status = &Status{
			runningContainers: make(map[string]int),
		}
	})

	return status
}

func (status *Status) AddContainerToCache(containerID string) {
	mutex.Lock()
	dt := time.Now().UnixMilli()
	defer mutex.Unlock()
	status.runningContainers[containerID] = int(dt)
	status.LastUpdated = dt
}

func (status *Status) RemoveContainerFromCache(containerID string) {
	mutex.Lock()
	defer mutex.Unlock()
	delete(status.runningContainers, containerID)
	status.LastUpdated = time.Now().UnixMilli()
}

func (status *Status) ContainerExistsInCache(containerID string) bool {
	mutex.Lock()
	defer mutex.Unlock()
	_, exists := status.runningContainers[containerID]
	return exists
}

func GetContainerDetails(containerID string) (*ContainerDetails, error) {
	mutex.Lock()
	defer mutex.Unlock()
	containerJSON, err := dockerClient.ContainerInspect(context.Background(), containerID)
	if err != nil {
		logger.Error("Failed to inspect container", "container_id", containerID, "error", err)
		return nil, err
	}
	details := &ContainerDetails{
		ContainerID:   containerJSON.ID,
		ContainerName: containerJSON.Name,
		Image:         containerJSON.Config.Image,
	}
	return details, nil
}
