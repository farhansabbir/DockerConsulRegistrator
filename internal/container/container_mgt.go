package container

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/go-connections/nat"
)

var (
	mutex      sync.Mutex
	containerQ *ContainerQueue
)

type ContainerQueue struct {
	runningContainers map[string]ContainerDetails `json:"-"`
	LastUpdated       int64                       `json:"last_updated"`
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

func GetContainerQueue() *ContainerQueue {
	once.Do(func() {
		containerQ = &ContainerQueue{
			runningContainers: make(map[string]ContainerDetails),
		}
	})
	return containerQ
}

func (status *ContainerQueue) AddContainerToCache(containerID string) {
	mutex.Lock()
	dt := time.Now().UnixMilli()
	defer mutex.Unlock()
	if containerdetails, err := GetContainerDetails(containerID); err == nil {
		status.runningContainers[containerID] = *containerdetails
		status.LastUpdated = dt
	} else {
		logger.Error("Could not add container to cache", "container_id", containerID, "error", err)
	}
}

func (status *ContainerQueue) RemoveContainerFromCache(containerID string) {
	mutex.Lock()
	defer mutex.Unlock()
	delete(status.runningContainers, containerID)
	status.LastUpdated = time.Now().UnixMilli()
}

func (status *ContainerQueue) ContainerExistsInCache(containerID string) bool {
	mutex.Lock()
	defer mutex.Unlock()
	_, exists := status.runningContainers[containerID]
	return exists
}

func (status *ContainerQueue) GetRunningContainers() map[string]ContainerDetails {
	mutex.Lock()
	defer mutex.Unlock()
	// Return a copy to avoid race conditions
	copyMap := make(map[string]ContainerDetails)
	for k, v := range status.runningContainers {
		copyMap[k] = v
	}
	return copyMap
}

func GetContainerDetails(containerID string) (*ContainerDetails, error) {
	// mutex.Lock()
	// defer mutex.Unlock()
	portMap := make(map[nat.Port][]string)
	containerJSON, err := dockerClient.ContainerInspect(context.Background(), containerID)
	servicename := ""

	if err != nil {
		logger.Error("Failed to inspect container", "container_id", containerID, "error", err)
		return nil, err
	}
	servicename = containerJSON.Name[1:] // Remove leading slash

	for port, bindings := range containerJSON.NetworkSettings.Ports {
		if len(bindings) > 0 {
			portbinding := []string{}
			for _, binding := range bindings {
				portbinding = append(portbinding, binding.HostIP+":"+binding.HostPort)
			}
			portMap[port] = portbinding
		} else {
			portMap[port] = []string{}
		}
	}
	details := &ContainerDetails{}

	if containerJSON.Config.Labels["com.docker.swarm.service.name"] != "" {
		servicename = containerJSON.Config.Labels["com.docker.swarm.service.name"]
		service_id := containerJSON.Config.Labels["com.docker.swarm.service.id"]
		swarm_svc, _, err := dockerClient.ServiceInspectWithRaw(context.Background(), service_id, swarm.ServiceInspectOptions{})
		if err != nil {
			logger.Error("Failed to inspect service", "service_id", service_id, "error", err)
		} else {
			for _, port := range swarm_svc.Endpoint.Ports {
				portKey := nat.Port(fmt.Sprintf("%d/%s", port.TargetPort, port.Protocol))
				portbinding := []string{fmt.Sprintf("0.0.0.0:%d", port.PublishedPort)}
				portMap[portKey] = append(portMap[portKey], portbinding[0])
			}
			details = &ContainerDetails{
				ContainerID:   containerJSON.ID,
				ContainerName: containerJSON.Name[1:], // Remove leading slash
				ServiceName:   servicename,
				Image:         containerJSON.Config.Image,
				PortMap:       portMap,
			}
		}

	} else {
		details = &ContainerDetails{
			ContainerID:   containerJSON.ID,
			ContainerName: containerJSON.Name[1:], // Remove leading slash
			ServiceName:   servicename,
			Image:         containerJSON.Config.Image,
			PortMap:       portMap,
		}
	}
	t, _ := time.Parse(time.RFC3339Nano, containerJSON.Created)
	details.CreatedAt = t.UnixMilli()
	details.Labels = []string{}
	for key, value := range containerJSON.Config.Labels {
		details.Labels = append(details.Labels, fmt.Sprintf("%s=%s", key, value))
	}
	return details, nil
}

func (details *ContainerDetails) String() string {
	js, _ := json.MarshalIndent(details, "", "  ")
	return string(js)
}
