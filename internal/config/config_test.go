package config

import (
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Agent = true
	if err := c.Validate(); err == nil {
		t.Fatal("--agent with nothing to join: accepted")
	}
	c.Join = []string{"10.0.0.11"}
	if err := c.Validate(); err != nil {
		t.Fatalf("--agent --join: %v", err)
	}
	c.Join, c.AgentConfigFound = nil, true
	if err := c.Validate(); err != nil {
		t.Fatalf("--agent with a mounted configuration: %v", err)
	}
}

func TestValidateRefuses(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"socket relative":      func(c *Config) { c.Socket = "unix://var/run/docker.sock" },
		"socket scheme":        func(c *Config) { c.Socket = "http://docker:2375" },
		"socket no port":       func(c *Config) { c.Socket = "tcp://docker" },
		"socket bare relative": func(c *Config) { c.Socket = "docker.sock" },
		"containerd over tcp":  func(c *Config) { c.Socket = "tcp://h:1"; c.Runtime = RuntimeContainerd },
		"runtime":              func(c *Config) { c.Runtime = "cri-o" },
		"namespace":            func(c *Config) { c.ContainerdNamespace = "k8s.io/x" },
		"domain dot":           func(c *Config) { c.Domain = "dmarts.app." },
		"domain upper":         func(c *Config) { c.Domain = "Dmarts.app" },
		"domain char":          func(c *Config) { c.Domain = "dmarts_app" },
		"consul path":          func(c *Config) { c.Consul = "http://127.0.0.1:8500/v1" },
		"consul scheme":        func(c *Config) { c.Consul = "ftp://127.0.0.1:8500" },
		"consul port":          func(c *Config) { c.Consul = "127.0.0.1:0" },
		"consul user":          func(c *Config) { c.Consul = "http://u:p@127.0.0.1:8500" },
		"consul host":          func(c *Config) { c.Consul = "bad_host:8500" },
		"cert without key":     func(c *Config) { c.Consul = "https://127.0.0.1:8501"; c.CertFile = "/c" },
		"tls over http":        func(c *Config) { c.CAFile = "/ca" },
		"tag space":            func(c *Config) { c.Tags = []string{"a b"} },
		"address":              func(c *Config) { c.Addresses = []string{"0.0.0.0"} },
		"address name":         func(c *Config) { c.Addresses = []string{"docker1"} },
		"address loopback":     func(c *Config) { c.Addresses = []string{"127.0.0.1"} },
		"two ipv4":             func(c *Config) { c.Addresses = []string{"192.168.1.93", "192.168.1.94"} },
		"check":                func(c *Config) { c.Check = "http" },
		"interval":             func(c *Config) { c.CheckInterval = 500 * time.Millisecond },
		"deregister too soon":  func(c *Config) { c.DeregisterAfter = 30 * time.Second },
		"resync":               func(c *Config) { c.Resync = time.Second },
		"node without agent":   func(c *Config) { c.AgentNode = "docker1" },
		"agent remote consul":  func(c *Config) { c.Agent = true; c.AgentConfigFound = true; c.Consul = "192.168.1.92:8500" },
		"agent node":           func(c *Config) { c.Agent = true; c.AgentConfigFound = true; c.AgentNode = "Docker_1" },
		"agent bind loopback":  func(c *Config) { c.Agent = true; c.AgentConfigFound = true; c.AgentBind = "127.0.0.1" },
		"agent relative bin":   func(c *Config) { c.Agent = true; c.AgentConfigFound = true; c.AgentBin = "consul" },
		"agent relative dir":   func(c *Config) { c.Agent = true; c.AgentConfigFound = true; c.AgentConfigDirs = []string{"config"} },
	} {
		c := Default()
		change(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestValidateAccepts(t *testing.T) {
	for name, change := range map[string]func(*Config){
		"tcp socket": func(c *Config) { c.Socket = "tcp://socket-proxy:2375" },
		"bare path":  func(c *Config) { c.Socket = "/run/podman/podman.sock"; c.Runtime = RuntimePodman },
		"containerd": func(c *Config) {
			c.Socket = "unix:///run/containerd/containerd.sock"
			c.Runtime = RuntimeContainerd
			c.ContainerdNamespace = "k8s.io"
		},
		"domain": func(c *Config) { c.Domain = "dmarts.app" },
		"https consul": func(c *Config) {
			c.Consul = "https://localhost:8501"
			c.CAFile = "/ca.pem"
			c.CertFile = "/c"
			c.KeyFile = "/k"
		},
		"ipv6 consul":   func(c *Config) { c.Consul = "[::1]:8500" },
		"tags":          func(c *Config) { c.Tags = []string{"prod", "traefik.enable=true"} },
		"addresses":     func(c *Config) { c.Addresses = []string{"192.168.1.93", "2001:db8::93"} },
		"no deregister": func(c *Config) { c.DeregisterAfter = 0 },
		"agent settings": func(c *Config) {
			c.Agent = true
			c.Join = []string{"s1"}
			c.AgentNode = "docker1"
			c.AgentBind = "192.168.1.93"
		},
	} {
		c := Default()
		change(&c)
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTheLabelAndTheAgentSettings(t *testing.T) {
	for _, ok := range []string{"consul=yes", "app.dmarts/register=true", "dcr_x=1"} {
		if _, _, err := ParseLabel(ok); err != nil {
			t.Errorf("ParseLabel(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"consul", "=yes", "consul=", "a b=c", "consul=y es", "-x=1"} {
		if _, _, err := ParseLabel(bad); err == nil {
			t.Errorf("ParseLabel(%q) accepted", bad)
		}
	}
	c := Default()
	c.Agent, c.Join, c.AgentToken = true, []string{"s1"}, "a"
	if err := c.Validate(); err == nil {
		t.Error("an agent token without the registrator's: accepted")
	}
	c.Token = "r"
	if err := c.Validate(); err != nil {
		t.Errorf("both tokens: %v", err)
	}
	c = Default()
	c.Join = []string{"s1"}
	if err := c.Validate(); err == nil {
		t.Error("--join without --agent: accepted")
	}
	if Name("join") != "CONSUL_RETRY_JOIN (--join)" || Name("agent-bin") != "--agent-bin" {
		t.Errorf("Name: %s / %s", Name("join"), Name("agent-bin"))
	}
}
