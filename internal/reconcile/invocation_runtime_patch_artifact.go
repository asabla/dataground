package reconcile

import (
	"context"
	"encoding/json"
	"math"

	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

// The native adapter supplies full snapshots. Only this governed boundary can
// replace their content with a catalog-bound public artifact reference.
func (driver *InvocationRuntimeDriver) recordCompletedRuntimePatch(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, event dgruntime.Event) error {
	patch, err := dgruntime.ParseCompletedPatch(event.Payload)
	if err != nil || event.Sequence == 0 || event.Sequence > math.MaxInt64 {
		return dgruntime.ErrProtocol
	}
	if len(patch.Changes) == 0 {
		return dgruntime.ErrProtocol
	}
	content, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	record, err := driver.finalizeRuntimeTextArtifact(ctx, claim, effect, target, event.Sequence, "runtime-patch", "Completed file patch", "structured-output", "application/json", string(content))
	if err != nil {
		return err
	}
	reference := dgruntime.PatchArtifact{ArtifactID: record.ID, Digest: record.Digest, SizeBytes: record.SizeBytes, Status: patch.Status, ChangeCount: len(patch.Changes), Preview: dgruntime.PatchPreview(len(patch.Changes))}
	_, err = driver.store.RecordInvocationRuntimeEvent(ctx, claim, persistence.InvocationRuntimeEvent{SourceSequence: event.Sequence, Type: dgruntime.PatchArtifactEvent, Payload: reference.Payload()})
	if err != nil {
		return err
	}
	return nil
}
