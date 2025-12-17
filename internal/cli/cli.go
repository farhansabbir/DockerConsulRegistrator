package cli

import (
	"fmt"
	"os"

	"github.com/farhansabbir/docker_consul_registrator/internal/config"
	"github.com/spf13/pflag"
)

var (
	DockerHost    *string
	ConsulHost    *string
	ListenForTags *map[string]string
	DoNotRegister bool
)

func ParseCmdline() {
	pflag.Usage = func() {
		fmt.Println("Usage: " + os.Args[0] + " [options]")
		pflag.PrintDefaults()
	}

	DockerHost = pflag.StringP("docker", "d", "unix://var/run/docker.sock", "Docker socket")
	ConsulHost = pflag.StringP("consul", "c", "localhost:8500", "Consul host:port")
	ListenForTags = pflag.StringToStringP("tags", "t", nil, "Tags to listen to for registering containers (consul_registrator=true is default).")
	pflag.BoolVarP(&DoNotRegister, "ignore", "i", false, "Ignore and do not register containers. Only listen for tags.")

	pflag.Parse()
	tags := *ListenForTags
	if tags == nil {
		tags = make(map[string]string)
		tags["consul_registrator"] = "true"
		*ListenForTags = tags
	}
	config.NewConfig(*DockerHost, *ConsulHost, DoNotRegister, *ListenForTags)
}
