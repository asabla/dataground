package reconcile

import (
	"context"
	"math"

	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func (driver *InvocationRuntimeDriver) recordCompletedRuntimeImage(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, event dgruntime.Event) error {
	if event.Sequence == 0 || event.Sequence > math.MaxInt64 {
		return dgruntime.ErrProtocol
	}
	content, err := dgruntime.ParseCompletedImage(event.Payload)
	if err != nil {
		return err
	}
	record, err := driver.finalizeRuntimeArtifact(ctx, claim, effect, target, event.Sequence, "runtime-image", "Generated image", "file", content.MediaType, content.Bytes)
	if err != nil {
		return err
	}
	reference := dgruntime.ImageArtifact{ArtifactID: record.ID, Digest: record.Digest, SizeBytes: record.SizeBytes, MediaType: content.MediaType, Width: content.Width, Height: content.Height, Preview: dgruntime.ImagePreview}
	_, err = driver.store.RecordInvocationRuntimeEvent(ctx, claim, persistence.InvocationRuntimeEvent{SourceSequence: event.Sequence, Type: dgruntime.ImageArtifactEvent, Payload: reference.Payload()})
	return err
}
