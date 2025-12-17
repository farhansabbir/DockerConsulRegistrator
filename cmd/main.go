package main

import (
	"github.com/farhansabbir/docker_consul_registrator/internal/cli"
	"github.com/farhansabbir/docker_consul_registrator/internal/container"
)

func main() {

	cli.ParseCmdline()
	container.LookForContainers()
}
