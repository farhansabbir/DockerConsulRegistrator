//go:build !linux

package agent

import "syscall"

func sysProcAttr() *syscall.SysProcAttr { return nil }
