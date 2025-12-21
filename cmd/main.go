package main

import (
	"github.com/farhansabbir/DockerConsulRegistrator/controller"
	"github.com/farhansabbir/DockerConsulRegistrator/internal/cli"
)

func main() {
	cli.ParseCmdline()
	controller.Start()

}
