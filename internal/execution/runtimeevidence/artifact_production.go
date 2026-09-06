package runtimeevidence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

// Produce the fixture through the native runtime before observing its bytes
// through the provider export boundary. A successful model reply alone cannot
// establish either that a command ran or that an artifact was exported.
func (state *openShellProbeState) produceArtifact(ctx context.Context, request ProbeRequest) ([sha256.Size]byte, error) {
	start := lockedRequest(fmt.Sprintf(
		"Use exec_command to run this exact command with default permissions, then finish: umask 077; printf '%%s\\n' '{\"runId\":\"%s\",\"status\":\"passed\"}' > %s",
		request.RunID, runtimeArtifactPath(request.RunID),
	))
	start.WorkingDir = "/sandbox"
	start.SandboxMode = dgruntime.SandboxWorkspaceWrite
	turn, closeTurn, err := state.runtime.start(ctx, request, start)
	if err != nil {
		return [sha256.Size]byte{}, ErrOpenShellProbeObservation
	}
	defer func() { _ = closeTurn() }()
	events, err := observeTurn(ctx, turn, nil, "lifecycle.succeeded")
	if err != nil || ctx.Err() != nil || turn.Wait(ctx) != nil || !validNormalizedEvents(events) ||
		!hasEvent(events, "activity.process.started", "command") ||
		!hasEvent(events, "activity.process.completed", "command") ||
		closeTurn() != nil {
		return [sha256.Size]byte{}, ErrOpenShellProbeObservation
	}
	encoded, err := json.Marshal(events)
	defer clear(encoded)
	if err != nil || len(encoded) > maxCodexProbeEventBytes {
		return [sha256.Size]byte{}, ErrOpenShellProbeObservation
	}
	return sha256.Sum256(encoded), nil
}
