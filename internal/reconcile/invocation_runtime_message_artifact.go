package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

// The native adapter supplies full snapshots. Only this governed boundary can
// replace their content with a catalog-bound public artifact reference.
func (driver *InvocationRuntimeDriver) recordRuntimeOutput(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, event dgruntime.Event) (*dgruntime.MessageArtifact, error) {
	if event.Type == dgruntime.MessageArtifactEvent || event.Type == dgruntime.CommandArtifactEvent || event.Type == dgruntime.PatchArtifactEvent || event.Type == dgruntime.ImageArtifactEvent || event.Type == dgruntime.ToolArtifactEvent {
		return nil, dgruntime.ErrProtocol
	}
	if event.Type == dgruntime.ToolCompletedEvent {
		return nil, driver.recordCompletedRuntimeTool(ctx, claim, effect, target, event)
	}
	if event.Type == dgruntime.ImageCompletedEvent {
		return nil, driver.recordCompletedRuntimeImage(ctx, claim, effect, target, event)
	}
	if event.Type == dgruntime.PatchCompletedEvent {
		return nil, driver.recordCompletedRuntimePatch(ctx, claim, effect, target, event)
	}
	if event.Type == dgruntime.CommandCompletedEvent {
		return nil, driver.recordCompletedRuntimeCommand(ctx, claim, effect, target, event)
	}
	if event.Type == "output.text.delta" {
		text, ok := event.Payload["text"].(string)
		if !ok || len(event.Payload) != 1 || !utf8.ValidString(text) || len(text) > dgruntime.MaximumInlineMessageTextBytes {
			return nil, dgruntime.ErrProtocol
		}
		event.Payload = map[string]any{"text": dgruntime.MessageDeltaPreview(text)}
		return nil, driver.recordRuntimeEvent(ctx, claim, event)
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
	if len(message.Text) <= dgruntime.MaximumInlineMessageTextBytes && len(encoded) <= 128<<10 && !messageContainsNUL(message.Text, target.OutputSchema != nil) {
		return nil, driver.recordRuntimeEvent(ctx, claim, event)
	}
	record, err := driver.finalizeRuntimeArtifact(ctx, claim, effect, target, event.Sequence, "runtime-message", "Completed runtime message", "file", "text/plain; charset=utf-8", []byte(message.Text))
	if err != nil {
		return nil, err
	}
	reference := &dgruntime.MessageArtifact{ArtifactID: record.ID, Digest: record.Digest, SizeBytes: record.SizeBytes, Phase: message.Phase, Preview: dgruntime.MessagePreview(message.Text)}
	_, err = driver.store.RecordInvocationRuntimeEvent(ctx, claim, persistence.InvocationRuntimeEvent{SourceSequence: event.Sequence, Type: dgruntime.MessageArtifactEvent, Payload: reference.Payload()})
	if err != nil {
		return nil, err
	}
	return reference, nil
}

func (driver *InvocationRuntimeDriver) finalizeRuntimeArtifact(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, sequence uint64, namespace, name, kind, mediaType string, content []byte) (artifact.Record, error) {
	if err := driver.ready(ctx); err != nil {
		return artifact.Record{}, err
	}
	request, err := driver.requests.BuildInvocationRuntimeRequest(target)
	if err != nil {
		return artifact.Record{}, err
	}
	if err := driver.validateInvocationRuntimeRequest(request); err != nil {
		return artifact.Record{}, err
	}
	if err := driver.authorizer.AuthorizeInvocationRuntime(ctx, target, request); err != nil {
		return artifact.Record{}, err
	}
	claim, err = driver.store.RenewLease(ctx, claim, driver.leaseDuration)
	if err != nil {
		return artifact.Record{}, err
	}
	if err := driver.ready(ctx); err != nil {
		return artifact.Record{}, err
	}
	digest := sha256.Sum256(content)
	record := artifact.Record{
		SchemaVersion: artifact.InvocationArtifactSchemaV1, IsolationDomainID: claim.IsolationDomainID,
		ID:           identity.Derived("art", claim.IsolationDomainID+":"+target.InvocationID+":"+namespace+":"+strconv.FormatUint(sequence, 10)),
		InvocationID: target.InvocationID, OperationID: claim.ID, EffectID: effect.EffectID,
		Name: name, Kind: kind, MediaType: mediaType,
		SizeBytes: int64(len(content)), Digest: "sha256:" + hex.EncodeToString(digest[:]), Sensitive: true,
	}
	bound, err := driver.artifacts.Finalize(ctx, artifact.Finalization{Binding: artifact.Binding{
		Record: record, ActorID: claim.ActorID, CorrelationID: claim.CorrelationID,
		LeaseOwner: claim.LeaseOwner, FencingToken: claim.FencingToken, StateMachineVersion: claim.StateMachineVersion,
	}, Content: content})
	if err != nil {
		return artifact.Record{}, err
	}
	if bound.ID != record.ID || bound.Digest != record.Digest || bound.SizeBytes != record.SizeBytes {
		return artifact.Record{}, artifact.ErrInvocationArtifactConflict
	}
	if err := driver.ready(ctx); err != nil {
		return artifact.Record{}, err
	}
	return record, nil
}

// A structured result can decode a JSON escape into NUL even when its source
// text is safe for JSONB. Inspect string tokens, including keys, without changing
// the original snapshot or bypassing the later output-schema validation.
func messageContainsNUL(text string, structured bool) bool {
	if strings.ContainsRune(text, 0) {
		return true
	}
	if !structured {
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		if value, ok := token.(string); ok && strings.ContainsRune(value, 0) {
			return true
		}
	}
}
