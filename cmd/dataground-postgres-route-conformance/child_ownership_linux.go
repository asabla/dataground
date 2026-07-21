//go:build linux

package main

import (
	"os/exec"
	"syscall"
)

func configureRouteChildOwnership(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	return nil
}
