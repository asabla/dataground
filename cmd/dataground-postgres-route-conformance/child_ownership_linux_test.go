//go:build linux

package main

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestConfigureRouteChildOwnershipUsesFatalParentDeathSignal(t *testing.T) {
	command := exec.Command("true")
	if err := configureRouteChildOwnership(command); err != nil {
		t.Fatal(err)
	}
	if command.SysProcAttr == nil || command.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatal("route child is not bound to fatal supervisor loss")
	}
}
