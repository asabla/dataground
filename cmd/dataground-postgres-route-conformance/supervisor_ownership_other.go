//go:build !linux

package main

import "errors"

func acquireRouteSupervisorOwnership(
	routeSupervisorConfig,
) (routeSupervisorOwnership, error) {
	return nil, errors.New("PostgreSQL route supervisor ownership requires Linux")
}
