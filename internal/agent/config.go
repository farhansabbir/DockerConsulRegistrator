package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Settings is the agent's configuration the registrator writes, from its
// own settings and secrets - so that the container needs nothing mounted.
type Settings struct {
	Datacenter string
	Join       []string
	Encrypt    string // the gossip key
	AgentToken string // with it, ACLs are on
	DNSToken   string // the agent's default token: what anonymous DNS may read
	// Defaults adds what an agent in this container needs anyway - DNS and
	// HTTP on loopback, healthy instances only in DNS, leaving on stop -
	// when nothing else configures it.
	Defaults bool
}

// file is the agent's configuration in Consul's JSON form.
type file struct {
	Datacenter       string     `json:"datacenter,omitempty"`
	RetryJoin        []string   `json:"retry_join,omitempty"`
	ClientAddr       string     `json:"client_addr,omitempty"`
	LeaveOnTerminate *bool      `json:"leave_on_terminate,omitempty"`
	DNSConfig        *dnsConfig `json:"dns_config,omitempty"`
	Ports            *ports     `json:"ports,omitempty"`
	Encrypt          string     `json:"encrypt,omitempty"`
	ACL              *acl       `json:"acl,omitempty"`
}

type dnsConfig struct {
	OnlyPassing bool `json:"only_passing"`
}

type ports struct {
	GRPC    int `json:"grpc"`
	GRPCTLS int `json:"grpc_tls"`
}

type acl struct {
	Enabled                bool   `json:"enabled"`
	DefaultPolicy          string `json:"default_policy"`
	EnableTokenPersistence bool   `json:"enable_token_persistence"`
	Tokens                 tokens `json:"tokens"`
}

type tokens struct {
	Agent   string `json:"agent"`
	Default string `json:"default,omitempty"`
}

// WriteConfig writes the agent's configuration into dir - registrator.json
// from s, and local.json from local (CONSUL_LOCAL_CONFIG), when there is
// one - each readable by its owner only: they hold secrets. It returns the
// files to give the agent, in the order to read them.
func WriteConfig(dir string, s Settings, local string) ([]string, error) {
	f := file{Datacenter: s.Datacenter, RetryJoin: s.Join, Encrypt: s.Encrypt}
	if s.Defaults {
		leave := true
		f.ClientAddr = "127.0.0.1"
		f.LeaveOnTerminate = &leave
		f.DNSConfig = &dnsConfig{OnlyPassing: true}
		f.Ports = &ports{GRPC: -1, GRPCTLS: -1}
	}
	if s.AgentToken != "" {
		f.ACL = &acl{Enabled: true, DefaultPolicy: "deny", EnableTokenPersistence: true,
			Tokens: tokens{Agent: s.AgentToken, Default: s.DNSToken}}
	}
	var files []string
	if len(f.RetryJoin) > 0 || f.Datacenter != "" || f.Encrypt != "" || f.ACL != nil || s.Defaults {
		b, err := json.MarshalIndent(f, "", "  ")
		if err != nil {
			return nil, err
		}
		p := filepath.Join(dir, "registrator.json")
		if err := writePrivate(p, b); err != nil {
			return nil, err
		}
		files = append(files, p)
	}
	if strings.TrimSpace(local) != "" {
		p := filepath.Join(dir, "local.json")
		if err := writePrivate(p, []byte(local)); err != nil {
			return nil, err
		}
		files = append(files, p)
	}
	return files, nil
}

func writePrivate(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("the Consul agent's configuration: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("the Consul agent's configuration: %w", err)
	}
	return f.Close()
}

// secretVars are the environment variables the agent's process does not get:
// they hold secrets meant for the registrator, already in its configuration.
var secretVars = []string{"CONSUL_ENCRYPT", "CONSUL_AGENT_TOKEN", "CONSUL_DNS_TOKEN", "CONSUL_HTTP_TOKEN", "CONSUL_LOCAL_CONFIG"}

// Environ is environ without the registrator's secrets.
func Environ(environ []string) []string {
	out := make([]string, 0, len(environ))
next:
	for _, kv := range environ {
		for _, s := range secretVars {
			if strings.HasPrefix(kv, s+"=") {
				continue next
			}
		}
		out = append(out, kv)
	}
	return out
}
