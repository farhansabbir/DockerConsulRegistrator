// Command docker-consul-registrator registers the published ports of the
// running containers labelled consul=yes with the local Consul agent, and
// deregisters them when the containers stop.
package main

import (
	"os"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/cli"
)

// version is set when building: -ldflags "-X main.version=1.0.0".
var version = "dev"

func main() {
	os.Exit(cli.Main(os.Args[1:], version, os.Stdout, os.Stderr))
}
