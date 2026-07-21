package main

import (
	"errors"
	"os"
)

func validateRouteChildOwnership(expectedSupervisorPID int) error {
	if expectedSupervisorPID == 0 {
		return nil
	}
	if expectedSupervisorPID <= 1 || os.Getppid() != expectedSupervisorPID {
		return errors.New("PostgreSQL route conformance child ownership is invalid")
	}
	return nil
}
