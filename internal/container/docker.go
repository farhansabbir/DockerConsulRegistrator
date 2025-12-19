package container

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
)

var (
	dockerClient *client.Client
	err          error
	logger       *slog.Logger
	once         sync.Once
	WG           *sync.WaitGroup = &sync.WaitGroup{}
	SwarmMode    bool            = false
)

func _init() {
	logger = config.GetConfig().Logger
	logger.Info("Initializing Docker client from environment...")

	dockerClient, err = client.NewClientWithOpts(
		client.WithHostFromEnv(),
		client.WithAPIVersionNegotiation())
	if err != nil {
		logger.Error("Failed to create Docker client", "error", err)
		panic(err)
	}
	config.
		GetConfig().
		SetDockerClient(dockerClient)
	stat, err := dockerClient.Ping(context.Background())
	if err != nil {
		logger.Error("Failed to ping Docker daemon", "error", err)
		os.Exit(1)
	}
	js, _ := json.MarshalIndent(stat, "", "  ")
	logger.Info(string(js))
	info, err := dockerClient.Info(context.Background())
	if err != nil {
		logger.Error("Failed to get Docker info", "error", err)
		os.Exit(1)
	}
	if info.Swarm.LocalNodeState == "active" {
		SwarmMode = true
	}
	logger.Info("Docker Swarm mode", "enabled", SwarmMode)
}

func _loop() {
	cache_file, err := os.Open(config.CACHE_FILE)
	if err != nil {
		logger.Warn("Could not open cache file", "error", err)
		cache_file, err = os.Create(config.CACHE_FILE)
		if err != nil {
			logger.Error("Could not create cache file", "error", err)
			panic(err)
		}
		logger.Info("Created new cache file", "file", config.CACHE_FILE)
	}
	defer cache_file.Close()
	// Placeholder for the main loop logic to monitor Docker containers
	logger.Info("Starting container monitoring loop...")
	WG.Add(1)
	go func() {
		defer WG.Done()
		event_filters := filters.NewArgs()
		event_filters.Add("type", "container")
		event_filters.Add("event", "running")
		containers, err := dockerClient.ContainerList(context.Background(), container.ListOptions{})
		if err != nil {
			logger.Error("Failed to list Docker containers", "error", err)
		} else {
			logger.Info("Currently running containers")
			for _, container := range containers {
				// logger.Info("Container", "ID", container.ID, "Names", container.Names, "Image", container.Image, "State", container.State, "Status", container.Labels)
				fmt.Println(GetContainerDetails(container.ID))
			}
		}

		event_filters = filters.NewArgs()
		event_filters.Add("type", "container")
		event_filters.Add("event", "start")
		event_filters.Add("event", "stop")
		events, _ := dockerClient.Events(context.Background(), events.ListOptions{
			Filters: event_filters,
		})

		for event := range events {
			logger.Info("Received Docker event", "action", event.Action, "id", event.Actor.ID)
			if event.Action == "stop" {
				fmt.Println("Container stopped:", event.Actor.ID)
				continue
			}
			fmt.Println(GetContainerDetails(event.Actor.ID))
			// fmt.Println("Container started:", details)
			// Handle the event (e.g., update cache, register/deregister container)
		}
		fmt.Println("Out!!!")
	}()

}

func LookForContainers() {
	_init()
	_loop()
	WG.Wait()
}
