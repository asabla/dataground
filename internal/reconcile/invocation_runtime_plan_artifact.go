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
func (driver *InvocationRuntimeDriver) recordRuntimePlan(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, event dgruntime.Event) error {
	output, err := dgruntime.ParsePlanSnapshot(event.Payload)
	if err != nil || event.Sequence == 0 || event.Sequence > math.MaxInt64 {
		return dgruntime.ErrProtocol
	}
	content, err := json.Marshal(output)
	if err != nil {
		return err
	}
	record, err := driver.finalizeRuntimeArtifact(ctx, claim, effect, target, event.Sequence, "runtime-plan", "Plan update", "structured-output", "application/json", content)
	if err != nil {
		return err
	}
	completed, inProgress := output.Counts()
	reference := dgruntime.PlanArtifact{ArtifactID: record.ID, Digest: record.Digest, SizeBytes: record.SizeBytes, StepCount: len(output.Steps), CompletedCount: completed, InProgressCount: inProgress, Preview: dgruntime.PlanPreview}
	_, err = driver.store.RecordInvocationRuntimeEvent(ctx, claim, persistence.InvocationRuntimeEvent{SourceSequence: event.Sequence, Type: dgruntime.PlanArtifactEvent, Payload: reference.Payload()})
	if err != nil {
		return err
	}
	return nil
}
