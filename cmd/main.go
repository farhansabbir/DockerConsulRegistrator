package main

import (
	"fmt"

	"github.com/spf13/pflag"
)

func main() {
	dockerhost := pflag.StringP("docker", "d", "unix://var/run/docker.sock", "Docker socket")
	pflag.Parse()
	if pflag.NArg() > 0 {
		fmt.Println("Unexpected arguments:", pflag.Args())
	}
	pflag.Usage()
	fmt.Println(*dockerhost)
}
