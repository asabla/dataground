//go:build !linux

package runtimeevidence

import (
	"context"
	"time"
)

func checkLoadedGatewayConfiguration(context.Context, int, *runtimeTopologyWorkspace, string, time.Time) error {
	return ErrDockerTopologyConfiguration
}
