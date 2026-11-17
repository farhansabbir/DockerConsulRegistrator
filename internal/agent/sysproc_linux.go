package agent

import "syscall"

// sysProcAttr ends the agent with the registrator, even when the registrator
// dies without stopping it.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
