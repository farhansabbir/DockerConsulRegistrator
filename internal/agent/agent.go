// Package agent runs the Consul agent bundled in the registrator's image as
// a child process: the registrator starts it, registers with it only, and
// stops it when it stops. The agent's anti-entropy keeps the servers'
// catalog in step with what is registered here, so the registrator never
// talks to the servers.
//
// What the registrator works out goes in as flags - node name, addresses,
// DNS domain - and the rest is the agent's own configuration (servers to
// join, gossip key, ACL tokens, TLS) in its configuration directories.
package agent

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Agent is how to start the agent.
type Agent struct {
	Bin        string
	ConfigDirs []string
	DataDir    string
	Node       string     // its node name
	Bind       netip.Addr // the address it gossips on and advertises
	IPv6       netip.Addr // the node's IPv6 address, for its AAAA answers
	Domain     string     // the DNS domain it serves; "consul" stays its alternative
	Stdout     io.Writer
	Stderr     io.Writer
}

// Args are the agent's command line.
func (a Agent) Args() []string {
	args := []string{"agent", "-data-dir=" + a.DataDir}
	for _, d := range a.ConfigDirs {
		args = append(args, "-config-dir="+d)
	}
	if a.Node != "" {
		args = append(args, "-node="+a.Node)
	}
	if a.Bind.IsValid() {
		args = append(args, "-bind="+a.Bind.String(), "-advertise="+a.Bind.String())
	}
	if a.IPv6.IsValid() {
		args = append(args, fmt.Sprintf("-hcl=advertise_addr_ipv6 = %q", a.IPv6.String()))
	}
	if a.Domain != "" && a.Domain != "consul" {
		// The domain is the agent's own: its PTR answers name services in it.
		args = append(args, "-domain="+a.Domain, "-alt-domain=consul")
	}
	return args
}

// Process is the running agent.
type Process struct {
	cmd  *exec.Cmd
	done chan struct{}
	once sync.Once
	err  error
}

// Start starts the agent.
func (a Agent) Start() (*Process, error) {
	fi, err := os.Stat(a.Bin)
	if err != nil {
		return nil, fmt.Errorf("the Consul agent: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("the Consul agent: %s is not an executable file", a.Bin)
	}
	cmd := exec.Command(a.Bin, a.Args()...)
	cmd.Stdout, cmd.Stderr = a.Stdout, a.Stderr
	cmd.SysProcAttr = sysProcAttr()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("the Consul agent: %w", err)
	}
	p := &Process{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// Pid is the agent's process ID.
func (p *Process) Pid() int { return p.cmd.Process.Pid }

// Done is closed when the agent has exited; Err then says how.
func (p *Process) Done() <-chan struct{} { return p.done }

// Err is how the agent exited, once Done is closed.
func (p *Process) Err() error {
	<-p.done
	if p.err == nil {
		return errors.New("it exited")
	}
	return p.err
}

// Signal sends the agent a signal - SIGHUP makes it read its configuration
// again.
func (p *Process) Signal(sig os.Signal) error { return p.cmd.Process.Signal(sig) }

// Stop asks the agent to leave and stop (SIGTERM), and kills it after
// timeout.
func (p *Process) Stop(timeout time.Duration) {
	p.once.Do(func() {
		select {
		case <-p.done:
			return
		default:
		}
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.done:
		case <-time.After(timeout):
			_ = p.cmd.Process.Kill()
			<-p.done
		}
	})
}
