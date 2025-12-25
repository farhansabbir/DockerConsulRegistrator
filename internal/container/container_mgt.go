package container

import (
	"context"
	"encoding/gob"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/go-connections/nat"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/data"
)

var (
	mutex      sync.Mutex
	containerQ *data.RunningContainerQueue
)

func GetContainerQueue() *data.RunningContainerQueue {
	once.Do(func() {
		containerQ = &data.RunningContainerQueue{
			RunningContainers: make([]string, 0),
			LastUpdated:       time.Now().UnixMilli(),
		}
	})
	return containerQ
}

func ReconcileContainerQueue() {
	mutex.Lock()
	defer mutex.Unlock()
	tempq := &data.RunningContainerQueue{}
	fileData, err := os.OpenFile(config.CACHE_FILE, os.O_RDONLY, 0660)
	if err != nil {
		logger.Error("Could not open cache file", "error", err)
		return
	}
	defer fileData.Close()
	decoder := gob.NewDecoder(fileData)
	err = decoder.Decode(&tempq)
	if err != nil {
		logger.Error("Could not decode cache file", "error", err)
		return
	}
	logger.Info("Beginning to reconcile container cache loaded from file", "total_containers", len(tempq.RunningContainers))
	for containerID := range tempq.RunningContainers {
		fmt.Println(containerID)
	}
}

func GetContainerDetails(containerID string) (*data.ContainerDetails, error) {
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
	details := &data.ContainerDetails{}

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
			details = &data.ContainerDetails{
				ContainerID:   containerJSON.ID,
				ContainerName: containerJSON.Name[1:], // Remove leading slash
				ServiceName:   servicename,
				Image:         containerJSON.Config.Image,
				PortMap:       portMap,
			}
		}

	} else {
		details = &data.ContainerDetails{
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
