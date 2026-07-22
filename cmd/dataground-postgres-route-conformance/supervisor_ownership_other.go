//go:build !linux

package main

import "errors"

func acquireRouteSupervisorOwnership(
	routeSupervisorConfig,
) (routeOwnership, error) {
	return nil, errors.New("PostgreSQL route supervisor ownership requires Linux")
}

func acquireRouteManagerOwnership(
	routeSupervisorConfig,
) (routeOwnership, error) {
	return nil, errors.New("PostgreSQL route manager ownership requires Linux")
}
