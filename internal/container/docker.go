package container

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"

	"github.com/docker/docker/client"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
)

var (
	dockerClient *client.Client
	err          error
	logger       *slog.Logger
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
}

func LookForContainers() {
	_init()
	_loop()
}
