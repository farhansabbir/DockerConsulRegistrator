package main

import (
	"github.com/farhansabbir/DockerConsulRegistrator/internal/cli"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/container"
)

func main() {
	cli.ParseCmdline()
	container.LookForContainers()
}
