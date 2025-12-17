package main

import (
	"fmt"

	"github.com/farhansabbir/docker_consul_registrator/internal/cli"
	"github.com/farhansabbir/docker_consul_registrator/internal/config"
)

func main() {

	cli.ParseCmdline()
	fmt.Println(config.GetConfig())
}
