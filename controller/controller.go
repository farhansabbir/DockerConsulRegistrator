package controller

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/config"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/container"
)

var (
	wg     *sync.WaitGroup = &sync.WaitGroup{}
	logger *slog.Logger
)

func StartController() {
	logger = config.GetConfig().Logger.With("logger", "controller")
	// 1. Create a context that listens for Ctrl+C (SIGINT) or SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 2. Start Docker Monitoring (Pass the context!)
	wg.Add(1)
	go func() {
		defer wg.Done()
		container.LookForContainers(ctx) // Update this function to accept ctx
	}()

	// 3. Start Queue Processor
	wg.Add(1)
	go func() {
		defer wg.Done()
		processQueue(ctx) // Your other background operation
	}()

	fmt.Println("Controller is running. Press Ctrl+C to stop.")

	// 4. Wait for the context to be canceled (when a signal is received)
	<-ctx.Done()

	fmt.Println("\nShutdown signal received. Cleaning up...")

	// 5. Wait for goroutines to finish their current work
	wg.Wait()
	fmt.Println("Controller stopped gracefully.")
}

func processQueue(ctx context.Context) {
	q := container.GetContainerQueue()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("Queue processor stopping...")
			return
		case <-time.After(config.RefreshIntervalSeconds * time.Second):
			// Check the queue and take actions here
			logger.Info("Cache last updated", "duration", fmt.Sprintf("%v", time.Since(time.UnixMilli(q.LastUpdated))), "total_containers", len(q.GetRunningContainers()))
		}
	}
}
