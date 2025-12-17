package container

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/farhansabbir/docker_consul_registrator/internal/config"
)

var (
	dockerClient *client.Client
	err          error
)

func _init() {
	dockerClient, err = client.NewClientWithOpts(
		client.WithHostFromEnv(),
		client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}
	config.
		GetConfig().
		SetDockerClient(dockerClient)
}

func LookForContainers() {
	_init()
	if dockerClient == nil {
		fmt.Println("Docker client is not initialized")
		return
	}
	images, err := dockerClient.ImageList(context.Background(), image.ListOptions{})
	if err != nil {
		fmt.Println("Error listing Docker images:", err)
		return
	}
	fmt.Println("Found", len(images), "Docker images")
	for _, img := range images {
		fmt.Println("Image ID:", img.ID)

	}

}
