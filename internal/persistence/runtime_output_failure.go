package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/identity"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

type InvocationRuntimeOutputFailure struct {
	Artifact artifact.Finalization
	Result   map[string]any
}

// PrepareInvocationRuntimeOutputFailure freezes the already journaled completed
// result before object storage is touched. Sequence zero resumes only a frozen
// intent; it never grants permission to start another native turn.
func (repository *Repository) PrepareInvocationRuntimeOutputFailure(ctx context.Context, claim OperationClaim, effect EffectRecord, sequence uint64) (InvocationRuntimeOutputFailure, error) {
	if !validInvocationRuntimeAttempt(claim, effect) {
		return InvocationRuntimeOutputFailure{}, ErrInvocationRuntimeAttemptInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationRuntimeOutputFailure{}, err
	}
	defer tx.Rollback(ctx)
	invocationID, err := lockInvocationRuntimeClaim(ctx, tx, claim, repository.now())
	if err != nil {
		return InvocationRuntimeOutputFailure{}, err
	}
	if err := verifyInvocationRuntimeEffect(ctx, tx, effect); err != nil {
		return InvocationRuntimeOutputFailure{}, err
	}
	attempt, err := getInvocationRuntimeAttempt(ctx, tx, claim.IsolationDomainID, claim.ID)
	if err != nil {
		return InvocationRuntimeOutputFailure{}, err
	}
	if attempt.EffectID != effect.EffectID {
		return InvocationRuntimeOutputFailure{}, ErrInvocationRuntimeAttemptConflict
	}
	switch attempt.Status {
	case "reserved":
		if sequence == 0 || attempt.LeaseOwner != claim.LeaseOwner || attempt.FencingToken != claim.FencingToken {
			return InvocationRuntimeOutputFailure{}, ErrInvocationRuntimeAttemptAmbiguous
		}
	case "output_invalid":
		frozen, ok := attempt.Result["sourceSequence"].(string)
		if !ok {
			return InvocationRuntimeOutputFailure{}, ErrInvocationRuntimeAttemptConflict
		}
		frozenSequence, err := strconv.ParseUint(frozen, 10, 63)
		if err != nil || frozenSequence == 0 || (sequence != 0 && sequence != frozenSequence) {
			return InvocationRuntimeOutputFailure{}, ErrInvocationRuntimeAttemptConflict
		}
		sequence = frozenSequence
	default:
		return InvocationRuntimeOutputFailure{}, ErrInvocationRuntimeAttemptConflict
	}
	event, found, err := getInvocationRuntimeEvent(ctx, tx, claim.IsolationDomainID, invocationID, sequence)
	if err != nil {
		return InvocationRuntimeOutputFailure{}, err
	}
	message, parseErr := dgruntime.ParseCompletedMessage(event.Payload)
	if !found || event.Type != dgruntime.MessageCompletedEvent || parseErr != nil || message.Phase == "commentary" {
		return InvocationRuntimeOutputFailure{}, ErrInvocationRuntimeAttemptInvalid
	}
	var complete bool
	err = tx.QueryRow(ctx, `SELECT
        EXISTS (SELECT 1 FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2
            AND source_kind='runtime' AND event_type='lifecycle.succeeded' AND source_sequence > $3)
        AND NOT EXISTS (SELECT 1 FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2
            AND source_kind='runtime' AND (event_type IN ('lifecycle.failed','lifecycle.cancelled')
                OR (event_type='output.message.completed' AND payload->>'phase' <> 'commentary' AND source_sequence > $3)))`, claim.IsolationDomainID, invocationID, sequence).Scan(&complete)
	if err != nil {
		return InvocationRuntimeOutputFailure{}, err
	}
	if !complete {
		return InvocationRuntimeOutputFailure{}, ErrInvocationRuntimeAttemptInvalid
	}
	content := []byte(message.Text)
	digest := sha256.Sum256(content)
	// Declared artifact identifiers cannot contain a colon, so this name cannot
	// collide with an artifact exported from the runtime workspace.
	record := artifact.Record{
		SchemaVersion:     artifact.InvocationArtifactSchemaV1,
		IsolationDomainID: claim.IsolationDomainID,
		ID:                identity.Derived("art", claim.IsolationDomainID+":"+invocationID+":runtime-result:invalid"),
		InvocationID:      invocationID,
		OperationID:       claim.ID,
		EffectID:          effect.EffectID,
		Name:              "Invalid runtime result",
		Kind:              "file",
		MediaType:         "text/plain; charset=utf-8",
		SizeBytes:         int64(len(content)),
		Digest:            "sha256:" + hex.EncodeToString(digest[:]),
		Sensitive:         true,
	}
	result := map[string]any{
		"code": "RUNTIME_OUTPUT_INVALID", "status": "failed",
		"sourceSequence": strconv.FormatUint(sequence, 10),
		"artifactId":     record.ID, "artifactDigest": record.Digest, "sizeBytes": record.SizeBytes,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return InvocationRuntimeOutputFailure{}, err
	}
	updated, err := tx.Exec(ctx, `UPDATE invocation_runtime_attempts AS attempt
        SET status='output_invalid', result=$6, lease_owner=$4, fencing_token=$5, updated_at=clock_timestamp()
        FROM invocation_execution_operations AS operation
        WHERE attempt.isolation_domain_id=$1 AND attempt.operation_id=$2 AND attempt.effect_id=$3
          AND ((attempt.status='reserved' AND attempt.lease_owner=$4 AND attempt.fencing_token=$5)
               OR (attempt.status='output_invalid' AND attempt.result=$6::jsonb))
          AND operation.isolation_domain_id=attempt.isolation_domain_id AND operation.id=attempt.operation_id
          AND operation.command=$7 AND operation.observed_state='running'
          AND operation.lease_owner=$4 AND operation.lease_token=$5
          AND operation.lease_expires_at > clock_timestamp() AND operation.deadline_at > clock_timestamp()`,
		claim.IsolationDomainID, claim.ID, effect.EffectID, claim.LeaseOwner, claim.FencingToken, encoded, claim.Command)
	if err != nil {
		return InvocationRuntimeOutputFailure{}, fmt.Errorf("prepare runtime output retention: %w", err)
	}
	if updated.RowsAffected() != 1 {
		return InvocationRuntimeOutputFailure{}, errors.Join(ErrLeaseLost, ErrInvocationRuntimeAttemptConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeOutputFailure{}, err
	}
	return InvocationRuntimeOutputFailure{
		Result: result,
		Artifact: artifact.Finalization{
			Binding: artifact.Binding{
				Record: record, ActorID: claim.ActorID, CorrelationID: claim.CorrelationID,
				LeaseOwner: claim.LeaseOwner, FencingToken: claim.FencingToken,
				StateMachineVersion: claim.StateMachineVersion,
			},
			Content: content,
		},
	}, nil
}
