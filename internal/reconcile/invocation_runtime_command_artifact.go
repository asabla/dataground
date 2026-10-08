package reconcile

import (
	"context"
	"math"

	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

// The native adapter supplies full snapshots. Only this governed boundary can
// replace their content with a catalog-bound public artifact reference.
func (driver *InvocationRuntimeDriver) recordCompletedRuntimeCommand(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, event dgruntime.Event) error {
	command, err := dgruntime.ParseCompletedCommand(event.Payload)
	if err != nil || event.Sequence == 0 || event.Sequence > math.MaxInt64 {
		return dgruntime.ErrProtocol
	}
	record, err := driver.finalizeRuntimeTextArtifact(ctx, claim, effect, target, event.Sequence, "runtime-command", "Completed command output", "log", command.Text)
	if err != nil {
		return err
	}
	reference := dgruntime.CommandArtifact{ArtifactID: record.ID, Digest: record.Digest, SizeBytes: record.SizeBytes, Status: command.Status, ExitCode: command.ExitCode, Preview: dgruntime.CommandPreview(command.Text)}
	_, err = driver.store.RecordInvocationRuntimeEvent(ctx, claim, persistence.InvocationRuntimeEvent{SourceSequence: event.Sequence, Type: dgruntime.CommandArtifactEvent, Payload: reference.Payload()})
	if err != nil {
		return err
	}
	return nil
}
