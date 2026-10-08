package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

// The native adapter supplies full snapshots. Only this governed boundary can
// replace their content with a catalog-bound public artifact reference.
func (driver *InvocationRuntimeDriver) recordCompletedRuntimeMessage(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, event dgruntime.Event) (*dgruntime.MessageArtifact, error) {
	if event.Type == dgruntime.MessageArtifactEvent {
		return nil, dgruntime.ErrProtocol
	}
	if event.Type != dgruntime.MessageCompletedEvent {
		return nil, driver.recordRuntimeEvent(ctx, claim, event)
	}
	message, err := dgruntime.ParseCompletedMessage(event.Payload)
	if err != nil || event.Sequence == 0 || event.Sequence > math.MaxInt64 {
		return nil, dgruntime.ErrProtocol
	}
	encoded, err := json.Marshal(event.Payload)
	if err != nil {
		return nil, err
	}
	// Bound encoded payloads as well as UTF-8 bytes; JSON escaping can otherwise
	// exceed the journal or result limits even for a small raw snapshot.
	if len(message.Text) <= dgruntime.MaximumInlineMessageTextBytes && len(encoded) <= 128<<10 {
		return nil, driver.recordRuntimeEvent(ctx, claim, event)
	}
	if err := driver.ready(ctx); err != nil {
		return nil, err
	}
	request, err := driver.requests.BuildInvocationRuntimeRequest(target)
	if err != nil {
		return nil, err
	}
	if err := driver.validateInvocationRuntimeRequest(request); err != nil {
		return nil, err
	}
	if err := driver.authorizer.AuthorizeInvocationRuntime(ctx, target, request); err != nil {
		return nil, err
	}
	claim, err = driver.store.RenewLease(ctx, claim, driver.leaseDuration)
	if err != nil {
		return nil, err
	}
	if err := driver.ready(ctx); err != nil {
		return nil, err
	}
	content := []byte(message.Text)
	digest := sha256.Sum256(content)
	record := artifact.Record{
		SchemaVersion: artifact.InvocationArtifactSchemaV1, IsolationDomainID: claim.IsolationDomainID,
		ID:           identity.Derived("art", claim.IsolationDomainID+":"+target.InvocationID+":runtime-message:"+strconv.FormatUint(event.Sequence, 10)),
		InvocationID: target.InvocationID, OperationID: claim.ID, EffectID: effect.EffectID,
		Name: "Completed runtime message", Kind: "file", MediaType: "text/plain; charset=utf-8",
		SizeBytes: int64(len(content)), Digest: "sha256:" + hex.EncodeToString(digest[:]), Sensitive: true,
	}
	bound, err := driver.artifacts.Finalize(ctx, artifact.Finalization{Binding: artifact.Binding{
		Record: record, ActorID: claim.ActorID, CorrelationID: claim.CorrelationID,
		LeaseOwner: claim.LeaseOwner, FencingToken: claim.FencingToken, StateMachineVersion: claim.StateMachineVersion,
	}, Content: content})
	if err != nil {
		return nil, err
	}
	if bound.ID != record.ID || bound.Digest != record.Digest || bound.SizeBytes != record.SizeBytes {
		return nil, artifact.ErrInvocationArtifactConflict
	}
	if err := driver.ready(ctx); err != nil {
		return nil, err
	}
	reference := &dgruntime.MessageArtifact{ArtifactID: record.ID, Digest: record.Digest, SizeBytes: record.SizeBytes, Phase: message.Phase, Preview: dgruntime.MessagePreview(message.Text)}
	_, err = driver.store.RecordInvocationRuntimeEvent(ctx, claim, persistence.InvocationRuntimeEvent{SourceSequence: event.Sequence, Type: dgruntime.MessageArtifactEvent, Payload: reference.Payload()})
	if err != nil {
		return nil, err
	}
	return reference, nil
}
