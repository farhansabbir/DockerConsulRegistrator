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
	logger = config.GetConfig().Logger.With("logger", "container")
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
	// Load existing container cache from file

	// Placeholder for the main loop logic to monitor Docker containers
	logger.Info("Starting container monitoring loop...")
	WG.Go(func() {
		event_filters := filters.NewArgs(
			filters.Arg("type", "container"),
			filters.Arg("event", "running"),
		)
		containers, err := dockerClient.ContainerList(context.Background(), container.ListOptions{})
		if err != nil {
			logger.Error("Failed to list Docker containers", "error", err)
		} else {
			if len(containers) == 0 {
				logger.Info("No running containers found during initialization.")
			} else {
				for _, container := range containers {
					GetContainerQueue().AddContainerToCache(container.ID)
				}
			}
		}
		ReconcileContainerQueue()
		logger.Info("Final cached running containers: " + fmt.Sprint(len(GetContainerQueue().RunningContainers)))
		event_filters = filters.NewArgs(
			filters.Arg("type", "container"),
			filters.Arg("event", "start"),
			filters.Arg("event", "die"),
		)
		events, _ := dockerClient.Events(context.Background(), events.ListOptions{
			Filters: event_filters,
		})

		for event := range events {
			logger.Info("'"+string(event.Action)+"' Event:", "id", event.Actor.ID)
			if event.Action == "die" {
				if GetContainerQueue().RemoveContainerFromCache(event.Actor.ID) {
					logger.Info("Container removed from cache", "container_id", event.Actor.ID)
					continue
				} else {
					logger.Info("Container not found in cache during removal", "container_id", event.Actor.ID)
					continue
				}

			}
			GetContainerQueue().AddContainerToCache(event.Actor.ID)
		}
	})
}

func LookForContainers(ctx context.Context) {
	_init()
	_loop()
}
