package container

import (
	"context"
	"encoding/json"
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
		event_filters.Add("event", "start")
		event_filters.Add("event", "stop")
		event_filters.Add("event", "die")
		event_filters.Add("event", "running")
		cont, err := dockerClient.ContainerList(context.Background(), container.ListOptions{})
		if err != nil {
			logger.Error("Failed to list Docker containers", "error", err)
		} else {
			logger.Info("Currently running containers")
			for _, c := range cont {
				logger.Info("Container", "ID", c.ID, "Names", c.Names, "Image", c.Image, "State", c.State, "Status", c.Status)
			}
		}
		evt, _ := dockerClient.Events(context.Background(), events.ListOptions{
			Filters: event_filters,
		})

		for event := range evt {
			logger.Info("Received Docker event", "action", event.Action, "id", event.Actor.ID)
			// Handle the event (e.g., update cache, register/deregister container)
		}

	}()

}

func LookForContainers() {
	_init()
	_loop()
	WG.Wait()
}
