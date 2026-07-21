//go:build !linux

package main

import (
	"errors"
	"os/exec"
)

func configureRouteChildOwnership(*exec.Cmd) error {
	return errors.New("PostgreSQL route child ownership requires Linux")
}
