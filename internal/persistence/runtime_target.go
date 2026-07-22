package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	invocationlifecycle "github.com/asabla/dataground/internal/lifecycle/invocation"
	"github.com/jackc/pgx/v5"
)

const maximumInvocationRuntimeEventPayloadBytes = 256 << 10

var (
	ErrInvocationRuntimeTargetMissing = errors.New("invocation runtime target is missing")
	ErrInvocationRuntimeEventInvalid  = errors.New("invocation runtime event is invalid")
	ErrInvocationRuntimeEventConflict = errors.New("invocation runtime event conflicts with persisted event")
	runtimeEventTypePattern           = regexp.MustCompile(`^[a-z][a-z0-9]*(?:\.[a-z0-9]+)+$`)
)

type InvocationRuntimeTarget struct {
	IsolationDomainID   string
	OperationID         string
	InvocationID        string
	ServiceID           string
	RevisionID          string
	ActorID             string
	CorrelationID       string
	StateMachineVersion int
	Input               map[string]any
	RuntimeProfile      string
	OutputSchema        map[string]any
}

type InvocationRuntimeEvent struct {
	SourceSequence uint64
	Type           string
	Payload        map[string]any
}

// GetInvocationRuntimeTarget resolves the immutable invocation and revision
// inputs for one version-2 runtime effect. The target is unavailable until the
// exact admission effect is durably successful and the operation is running.
func (repository *Repository) GetInvocationRuntimeTarget(
	ctx context.Context,
	isolationDomainID string,
	operationID string,
) (InvocationRuntimeTarget, error) {
	return getInvocationRuntimeTarget(ctx, repository.pool, isolationDomainID, operationID)
}

// GetClaimedInvocationRuntimeTarget resolves the runtime handoff only for the
// exact active invocation claim. Cancellation and replacement workers fence
// stale readers through the invocation row lock and operation lease.
func (repository *Repository) GetClaimedInvocationRuntimeTarget(
	ctx context.Context,
	claim OperationClaim,
) (InvocationRuntimeTarget, error) {
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationRuntimeTarget{}, fmt.Errorf("begin claimed invocation runtime target: %w", err)
	}
	defer tx.Rollback(ctx)

	invocationID, err := lockInvocationRuntimeClaim(ctx, tx, claim, repository.now())
	if err != nil {
		return InvocationRuntimeTarget{}, err
	}
	target, err := getInvocationRuntimeTarget(
		ctx,
		tx,
		claim.IsolationDomainID,
		claim.ID,
	)
	if err != nil {
		return InvocationRuntimeTarget{}, err
	}
	if target.InvocationID != invocationID {
		return InvocationRuntimeTarget{}, ErrInvocationRuntimeTargetMissing
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeTarget{}, fmt.Errorf("commit claimed invocation runtime target: %w", err)
	}
	return target, nil
}

func getInvocationRuntimeTarget(
	ctx context.Context,
	querier operationQuerier,
	isolationDomainID string,
	operationID string,
) (InvocationRuntimeTarget, error) {
	var target InvocationRuntimeTarget
	var encodedInput, encodedOutputSchema []byte
	err := querier.QueryRow(ctx, `
		SELECT operation.isolation_domain_id, operation.id, operation.invocation_id,
		       invocation.service_id, invocation.revision_id,
		       COALESCE(operation.effect_actor_id, operation.actor_id),
		       COALESCE(operation.effect_correlation_id, operation.correlation_id),
		       operation.state_machine_version, invocation.input,
		       revision.runtime_profile, revision.output_schema
		FROM invocation_execution_operations AS operation
		JOIN invocations AS invocation
		  ON invocation.isolation_domain_id = operation.isolation_domain_id
		 AND invocation.id = operation.invocation_id
		JOIN service_revisions AS revision
		  ON revision.isolation_domain_id = invocation.isolation_domain_id
		 AND revision.id = invocation.revision_id
		 AND revision.service_id = invocation.service_id
		JOIN external_effects AS admission_effect
		  ON admission_effect.isolation_domain_id = operation.isolation_domain_id
		 AND admission_effect.operation_kind = 'invocation-execution'
		 AND admission_effect.operation_id = operation.id
		 AND admission_effect.phase = 'start-invocation'
		 AND admission_effect.status = 'succeeded'
		WHERE operation.isolation_domain_id = $1
		  AND operation.id = $2
		  AND operation.observed_state = 'running'
		  AND operation.state_machine_version = $3
		  AND (
		    operation.command = 'invoke'
		    OR (
		      operation.command = 'repair'
		      AND operation.effect_actor_id IS NOT NULL
		      AND operation.effect_correlation_id IS NOT NULL
		    )
		  )
	`, isolationDomainID, operationID, invocationlifecycle.StateMachineVersion).Scan(
		&target.IsolationDomainID,
		&target.OperationID,
		&target.InvocationID,
		&target.ServiceID,
		&target.RevisionID,
		&target.ActorID,
		&target.CorrelationID,
		&target.StateMachineVersion,
		&encodedInput,
		&target.RuntimeProfile,
		&encodedOutputSchema,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationRuntimeTarget{}, ErrInvocationRuntimeTargetMissing
	}
	if err != nil {
		return InvocationRuntimeTarget{}, err
	}
	if err := json.Unmarshal(encodedInput, &target.Input); err != nil {
		return InvocationRuntimeTarget{}, fmt.Errorf("decode invocation runtime input: %w", err)
	}
	if len(encodedOutputSchema) > 0 {
		if err := json.Unmarshal(encodedOutputSchema, &target.OutputSchema); err != nil {
			return InvocationRuntimeTarget{}, fmt.Errorf("decode invocation runtime output schema: %w", err)
		}
	}
	if target.IsolationDomainID != isolationDomainID ||
		target.OperationID != operationID ||
		target.InvocationID == "" ||
		target.ServiceID == "" ||
		target.RevisionID == "" ||
		target.ActorID == "" ||
		target.CorrelationID == "" ||
		target.StateMachineVersion != invocationlifecycle.StateMachineVersion ||
		target.RuntimeProfile == "" ||
		target.Input == nil {
		return InvocationRuntimeTarget{}, ErrInvocationRuntimeTargetMissing
	}
	return target, nil
}

// RecordInvocationRuntimeEvent appends one normalized adapter event to the
// public invocation journal. Runtime source sequence is an internal
// idempotency key; the journal allocates its own monotonic public sequence.
func (repository *Repository) RecordInvocationRuntimeEvent(
	ctx context.Context,
	claim OperationClaim,
	event InvocationRuntimeEvent,
) (domain.EventEnvelope, error) {
	if !validInvocationRuntimeClaim(claim) {
		return domain.EventEnvelope{}, ErrLeaseLost
	}
	if event.SourceSequence == 0 ||
		event.Type == "" ||
		len(event.Type) > 128 ||
		!runtimeEventTypePattern.MatchString(event.Type) ||
		event.Payload == nil {
		return domain.EventEnvelope{}, ErrInvocationRuntimeEventInvalid
	}
	encodedPayload, err := json.Marshal(event.Payload)
	if err != nil || len(encodedPayload) > maximumInvocationRuntimeEventPayloadBytes {
		return domain.EventEnvelope{}, ErrInvocationRuntimeEventInvalid
	}

	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return domain.EventEnvelope{}, fmt.Errorf("begin invocation runtime event: %w", err)
	}
	defer tx.Rollback(ctx)

	invocationID, err := lockInvocationRuntimeClaim(ctx, tx, claim, repository.now())
	if err != nil {
		return domain.EventEnvelope{}, err
	}

	target, err := getInvocationRuntimeTarget(
		ctx,
		tx,
		claim.IsolationDomainID,
		claim.ID,
	)
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	if target.InvocationID != invocationID {
		return domain.EventEnvelope{}, ErrInvocationRuntimeTargetMissing
	}

	existing, found, err := getInvocationRuntimeEvent(
		ctx,
		tx,
		target.IsolationDomainID,
		target.InvocationID,
		event.SourceSequence,
	)
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	if found {
		encodedExisting, encodeErr := json.Marshal(existing.Payload)
		if encodeErr != nil {
			return domain.EventEnvelope{}, fmt.Errorf("encode persisted invocation runtime event: %w", encodeErr)
		}
		if existing.Type != event.Type || !bytes.Equal(encodedExisting, encodedPayload) {
			return domain.EventEnvelope{}, ErrInvocationRuntimeEventConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.EventEnvelope{}, fmt.Errorf("commit invocation runtime event replay: %w", err)
		}
		return existing, nil
	}

	var sequence uint64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(sequence), 0) + 1
		FROM invocation_events
		WHERE isolation_domain_id = $1 AND invocation_id = $2
	`, target.IsolationDomainID, target.InvocationID).Scan(&sequence); err != nil {
		return domain.EventEnvelope{}, fmt.Errorf("allocate invocation runtime event sequence: %w", err)
	}

	now := repository.now()
	value := domain.EventEnvelope{
		SchemaVersion:     "dataground.event/v1",
		ID:                identity.Derived("evt", target.InvocationID+":runtime:"+strconv.FormatUint(event.SourceSequence, 10)),
		IsolationDomainID: target.IsolationDomainID,
		InvocationID:      target.InvocationID,
		Sequence:          sequence,
		Type:              event.Type,
		OccurredAt:        now,
		RecordedAt:        now,
		CorrelationID:     target.CorrelationID,
		ActorID:           target.ActorID,
		ServiceID:         target.ServiceID,
		RevisionID:        target.RevisionID,
		Payload:           event.Payload,
	}
	result, err := tx.Exec(ctx, `
		INSERT INTO invocation_events (
			isolation_domain_id, invocation_id, id, sequence, schema_version,
			event_type, occurred_at, recorded_at, correlation_id, actor_id,
			service_id, revision_id, payload, source_kind, source_sequence
		)
		SELECT $1, $2, $3, $4, $5,
		       $6, $7, $8, $9, $10,
		       $11, $12, $13, 'runtime', $14
		FROM invocation_execution_operations AS operation
		WHERE operation.isolation_domain_id = $1
		  AND operation.id = $15
		  AND operation.command = $16
		  AND operation.observed_state = $17
		  AND operation.lease_owner = $18
		  AND operation.lease_token = $19
		  AND operation.lease_expires_at > clock_timestamp()
		  AND operation.deadline_at > clock_timestamp()
	`, value.IsolationDomainID, value.InvocationID, value.ID, value.Sequence, value.SchemaVersion,
		value.Type, value.OccurredAt, value.RecordedAt, value.CorrelationID, value.ActorID,
		value.ServiceID, value.RevisionID, encodedPayload, event.SourceSequence,
		claim.ID, claim.Command, claim.ObservedState, claim.LeaseOwner, claim.FencingToken,
	)
	if err != nil {
		return domain.EventEnvelope{}, fmt.Errorf("persist invocation runtime event: %w", err)
	}
	if result.RowsAffected() != 1 {
		return domain.EventEnvelope{}, ErrLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.EventEnvelope{}, fmt.Errorf("commit invocation runtime event: %w", err)
	}
	return value, nil
}

func lockInvocationRuntimeClaim(
	ctx context.Context,
	tx pgx.Tx,
	claim OperationClaim,
	now time.Time,
) (string, error) {
	if !validInvocationRuntimeClaim(claim) {
		return "", ErrLeaseLost
	}
	var invocationID string
	err := tx.QueryRow(ctx, `
		SELECT invocation.id
		FROM invocations AS invocation
		JOIN invocation_execution_operations AS operation
		  ON operation.isolation_domain_id = invocation.isolation_domain_id
		 AND operation.invocation_id = invocation.id
		WHERE operation.isolation_domain_id = $1
		  AND operation.id = $2
		  AND operation.command = $3
		  AND operation.observed_state = $4
		  AND operation.state_machine_version = $5
		  AND operation.lease_owner = $6
		  AND operation.lease_token = $7
		  AND operation.lease_expires_at > $8
		  AND operation.deadline_at > $8
		FOR UPDATE OF invocation
	`,
		claim.IsolationDomainID,
		claim.ID,
		claim.Command,
		claim.ObservedState,
		claim.StateMachineVersion,
		claim.LeaseOwner,
		claim.FencingToken,
		now,
	).Scan(&invocationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLeaseLost
	}
	if err != nil {
		return "", fmt.Errorf("lock invocation runtime claim: %w", err)
	}
	return invocationID, nil
}

func validInvocationRuntimeClaim(claim OperationClaim) bool {
	return claim.Kind == OperationKindInvocation &&
		claim.IsolationDomainID != "" &&
		claim.ID != "" &&
		(claim.Command == "invoke" || claim.Command == "repair") &&
		claim.ObservedState == "running" &&
		claim.StateMachineVersion == invocationlifecycle.StateMachineVersion &&
		claim.LeaseOwner != "" &&
		claim.FencingToken > 0
}

func getInvocationRuntimeEvent(
	ctx context.Context,
	querier operationQuerier,
	isolationDomainID string,
	invocationID string,
	sourceSequence uint64,
) (domain.EventEnvelope, bool, error) {
	var value domain.EventEnvelope
	var encodedPayload, encodedExtensions []byte
	err := querier.QueryRow(ctx, `
		SELECT schema_version, id, isolation_domain_id, invocation_id, sequence,
		       event_type, occurred_at, recorded_at, correlation_id, actor_id,
		       service_id, revision_id, payload, extensions
		FROM invocation_events
		WHERE isolation_domain_id = $1
		  AND invocation_id = $2
		  AND source_kind = 'runtime'
		  AND source_sequence = $3
	`, isolationDomainID, invocationID, sourceSequence).Scan(
		&value.SchemaVersion,
		&value.ID,
		&value.IsolationDomainID,
		&value.InvocationID,
		&value.Sequence,
		&value.Type,
		&value.OccurredAt,
		&value.RecordedAt,
		&value.CorrelationID,
		&value.ActorID,
		&value.ServiceID,
		&value.RevisionID,
		&encodedPayload,
		&encodedExtensions,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EventEnvelope{}, false, nil
	}
	if err != nil {
		return domain.EventEnvelope{}, false, fmt.Errorf("read invocation runtime event: %w", err)
	}
	if err := json.Unmarshal(encodedPayload, &value.Payload); err != nil {
		return domain.EventEnvelope{}, false, fmt.Errorf("decode invocation runtime event payload: %w", err)
	}
	if len(encodedExtensions) > 0 {
		if err := json.Unmarshal(encodedExtensions, &value.Extensions); err != nil {
			return domain.EventEnvelope{}, false, fmt.Errorf("decode invocation runtime event extensions: %w", err)
		}
	}
	return value, true, nil
}
