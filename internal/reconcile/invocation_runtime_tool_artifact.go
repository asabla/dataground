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
func (driver *InvocationRuntimeDriver) recordCompletedRuntimeTool(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, event dgruntime.Event) error {
	output, err := dgruntime.ParseCompletedToolOutput(event.Payload)
	if err != nil || event.Sequence == 0 || event.Sequence > math.MaxInt64 {
		return dgruntime.ErrProtocol
	}
	content, err := json.Marshal(output)
	if err != nil {
		return err
	}
	record, err := driver.finalizeRuntimeArtifact(ctx, claim, effect, target, event.Sequence, "runtime-tool", "Completed tool output", "structured-output", "application/json", content)
	if err != nil {
		return err
	}
	reference := dgruntime.ToolArtifact{ArtifactID: record.ID, Digest: record.Digest, SizeBytes: record.SizeBytes, Status: output.Status, ContentCount: len(output.Content), Preview: dgruntime.ToolOutputPreview}
	_, err = driver.store.RecordInvocationRuntimeEvent(ctx, claim, persistence.InvocationRuntimeEvent{SourceSequence: event.Sequence, Type: dgruntime.ToolArtifactEvent, Payload: reference.Payload()})
	if err != nil {
		return err
	}
	return nil
}
