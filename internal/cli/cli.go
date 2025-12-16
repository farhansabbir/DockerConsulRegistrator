package cli

import (
	"fmt"
	"os"

	"github.com/farhansabbir/docker_consul_registrator/internal/config"
	"github.com/spf13/pflag"
)

func ParseCmdline() *config.Config {
	pflag.Usage = func() {
		fmt.Println("Usage: " + os.Args[0] + " [options]")
		pflag.PrintDefaults()
	}

	dockerhost := pflag.StringP("docker", "d", "unix://var/run/docker.sock", "Docker socket")
	consulhost := pflag.StringP("consul", "c", "localhost:8500", "Consul host:port")
	tags := pflag.StringToStringP("tags", "t", nil, "Tags to listen to for registering containers (key=value). Can be specified multiple times.")
	shouldRegister := pflag.BoolP("register", "r", true, "Register all running containers on startup")

	pflag.Parse()
	return config.NewConfig(*dockerhost, *consulhost, *shouldRegister, *tags)
}
