package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/lifecycle/invocation"
	"github.com/asabla/dataground/internal/lifecycle/publication"
	"github.com/jackc/pgx/v5"
)

const (
	OperationKindPublication = "service-publication"
	OperationKindInvocation  = "invocation-execution"
)

var ErrLeaseLost = errors.New("operation lease was lost or fenced")

type OperationFailureReason string

const (
	OperationFailureDeadline      OperationFailureReason = "deadline-exceeded"
	OperationFailureEffectDenied  OperationFailureReason = "effect-denied"
	OperationFailureEffectInvalid OperationFailureReason = "effect-invalid"
	OperationFailureRuntime       OperationFailureReason = "runtime-failed"
)

type OperationClaim struct {
	Kind                string
	IsolationDomainID   string
	ID                  string
	ResourceID          string
	Command             string
	ObservedState       string
	StateMachineVersion int
	Attempt             int
	LeaseOwner          string
	FencingToken        int64
	DeadlineAt          time.Time
	LeaseExpiresAt      time.Time
	CorrelationID       string
	ActorID             string
}

type EffectRecord struct {
	IsolationDomainID string
	EffectID          string
	OperationKind     string
	OperationID       string
	Phase             string
	RequestDigest     [32]byte
	Status            string
	Attempt           int
	Observation       map[string]any
}

// ClaimNext leases one due operation. The two explicit operation tables retain
// their domain semantics; this method only shares the bounded leasing primitive.
func (repository *Repository) ClaimNext(
	ctx context.Context,
	kind string,
	workerID string,
	leaseDuration time.Duration,
) (*OperationClaim, error) {
	table, resourceColumn, terminalStates, err := operationTable(kind)
	if err != nil {
		return nil, err
	}
	now := repository.now()
	query := fmt.Sprintf(`
		WITH per_domain AS (
			SELECT DISTINCT ON (isolation_domain_id)
			       isolation_domain_id, id, due_at, updated_at
			FROM %s
			WHERE observed_state <> ALL($1)
			  AND (command <> 'repair' OR (effect_actor_id IS NOT NULL AND effect_correlation_id IS NOT NULL))
			  AND due_at <= $2
			  AND (lease_expires_at IS NULL OR lease_expires_at <= $2)
			ORDER BY isolation_domain_id, due_at, updated_at, id
		), candidate AS (
			SELECT isolation_domain_id, id
			FROM per_domain
			ORDER BY due_at, updated_at, isolation_domain_id, id
			LIMIT 1
		), claimed AS (
			UPDATE %s AS operation
			SET lease_owner = $3,
			    lease_token = operation.lease_token + 1,
			    lease_expires_at = CASE
			      WHEN operation.deadline_at > $2 THEN LEAST($4, operation.deadline_at)
			      ELSE $4
			    END,
			    attempt = operation.attempt + 1,
			    updated_at = $2
			FROM candidate
			WHERE operation.isolation_domain_id = candidate.isolation_domain_id
			  AND operation.id = candidate.id
			  AND (operation.lease_expires_at IS NULL OR operation.lease_expires_at <= $2)
			RETURNING operation.isolation_domain_id, operation.id, operation.%s,
			          operation.command, operation.observed_state, operation.state_machine_version,
			          operation.attempt,
			          operation.lease_token, operation.lease_expires_at, operation.deadline_at,
			          COALESCE(operation.effect_correlation_id, operation.correlation_id),
			          COALESCE(operation.effect_actor_id, operation.actor_id)
		)
		SELECT * FROM claimed
	`, table, table, resourceColumn)
	var claim OperationClaim
	claim.Kind = kind
	claim.LeaseOwner = workerID
	err = repository.pool.QueryRow(ctx, query, terminalStates, now, workerID, now.Add(leaseDuration)).Scan(
		&claim.IsolationDomainID,
		&claim.ID,
		&claim.ResourceID,
		&claim.Command,
		&claim.ObservedState,
		&claim.StateMachineVersion,
		&claim.Attempt,
		&claim.FencingToken,
		&claim.LeaseExpiresAt,
		&claim.DeadlineAt,
		&claim.CorrelationID,
		&claim.ActorID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim %s operation: %w", kind, err)
	}
	return &claim, nil
}

// RenewLease extends one active claim without changing its fencing token. The
// lease never crosses the durable operation deadline, and an expired, replaced,
// terminal, or otherwise changed claim cannot be revived.
func (repository *Repository) RenewLease(
	ctx context.Context,
	claim OperationClaim,
	leaseDuration time.Duration,
) (OperationClaim, error) {
	if claim.IsolationDomainID == "" ||
		claim.ID == "" ||
		claim.Command == "" ||
		claim.ObservedState == "" ||
		claim.LeaseOwner == "" ||
		claim.FencingToken <= 0 ||
		leaseDuration <= 0 {
		return OperationClaim{}, errors.New("complete operation claim and positive lease duration are required")
	}
	table, _, _, err := operationTable(claim.Kind)
	if err != nil {
		return OperationClaim{}, err
	}
	now := repository.now()
	if !claim.DeadlineAt.After(now) {
		return OperationClaim{}, ErrLeaseLost
	}
	expiresAt := now.Add(leaseDuration)
	if expiresAt.After(claim.DeadlineAt) {
		expiresAt = claim.DeadlineAt
	}
	query := fmt.Sprintf(`
		UPDATE %s
		SET lease_expires_at = LEAST(deadline_at, GREATEST(lease_expires_at, $8)),
		    updated_at = $7
		WHERE isolation_domain_id = $1 AND id = $2
		  AND command = $3 AND observed_state = $4
		  AND lease_owner = $5 AND lease_token = $6
		  AND lease_expires_at > $7 AND deadline_at > $7
		RETURNING lease_expires_at
	`, table)
	err = repository.pool.QueryRow(
		ctx,
		query,
		claim.IsolationDomainID,
		claim.ID,
		claim.Command,
		claim.ObservedState,
		claim.LeaseOwner,
		claim.FencingToken,
		now,
		expiresAt,
	).Scan(&claim.LeaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperationClaim{}, ErrLeaseLost
	}
	if err != nil {
		return OperationClaim{}, fmt.Errorf("renew %s operation lease: %w", claim.Kind, err)
	}
	return claim, nil
}

// Advance performs a fenced state transition and writes its outbox and audit
// records in the same transaction. A stale worker can never commit a result.
func (repository *Repository) Advance(
	ctx context.Context,
	claim OperationClaim,
	nextState string,
	terminalResult map[string]any,
) error {
	if nextState == "failed" {
		return repository.Fail(ctx, claim, OperationFailureDeadline)
	}
	return repository.advance(ctx, claim, nextState, terminalResult, nil)
}

// Fail terminates a leased operation with one of the bounded, safe failure
// reasons understood by the durable control plane.
func (repository *Repository) Fail(
	ctx context.Context,
	claim OperationClaim,
	reason OperationFailureReason,
) error {
	failure, err := operationFailure(reason, claim.CorrelationID)
	if err != nil {
		return err
	}
	return repository.advance(ctx, claim, "failed", nil, &failure)
}

func (repository *Repository) advance(
	ctx context.Context,
	claim OperationClaim,
	nextState string,
	terminalResult map[string]any,
	failure *domain.APIError,
) error {
	if err := validateTransition(claim.Kind, claim.ObservedState, nextState); err != nil {
		return err
	}
	if (nextState == "failed") != (failure != nil) {
		return errors.New("failed transitions require exactly one bounded failure reason")
	}
	table, _, _, err := operationTable(claim.Kind)
	if err != nil {
		return err
	}
	now := repository.now()
	encodedResult, err := marshalNullable(terminalResult)
	if err != nil {
		return fmt.Errorf("encode terminal result: %w", err)
	}
	var encodedError []byte
	if failure != nil {
		encodedError, err = json.Marshal(failure)
		if err != nil {
			return fmt.Errorf("encode terminal error: %w", err)
		}
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin operation transition: %w", err)
	}
	defer tx.Rollback(ctx)
	query := fmt.Sprintf(`
		UPDATE %s
		SET observed_state = $6,
		    generation = generation + 1,
		    terminal_result = COALESCE($7, terminal_result),
		    error_classification = CASE
		      WHEN $6 = 'failed' THEN 'terminal'
		      WHEN $6 = 'cancelled' THEN 'cancelled'
		      ELSE error_classification
		    END,
		    error = COALESCE($8, error),
		    due_at = $9,
		    lease_owner = NULL,
		    lease_expires_at = NULL,
		    last_transition_at = $9,
		    updated_at = $9
		WHERE isolation_domain_id = $1 AND id = $2
		  AND observed_state = $3
		  AND lease_owner = $4 AND lease_token = $5
		  AND lease_expires_at > $9
	`, table)
	result, err := tx.Exec(ctx, query,
		claim.IsolationDomainID, claim.ID, claim.ObservedState,
		claim.LeaseOwner, claim.FencingToken, nextState, encodedResult, encodedError, now,
	)
	if err != nil {
		return fmt.Errorf("advance %s operation: %w", claim.Kind, err)
	}
	if result.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	if err := repository.updateResourceForTransition(ctx, tx, claim, nextState, terminalResult, now); err != nil {
		return err
	}
	outcome := transitionOutcome(nextState)
	if err := writeOutboxAndAudit(
		ctx, tx, claim.IsolationDomainID, claim.Kind, claim.ID,
		claim.Kind+"."+nextState, claim.ActorID, claim.CorrelationID,
		outcome, claim.ID, now,
	); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit operation transition: %w", err)
	}
	return nil
}

func operationFailure(reason OperationFailureReason, correlationID string) (domain.APIError, error) {
	var code, message string
	switch reason {
	case OperationFailureDeadline:
		code = "OPERATION_DEADLINE_EXCEEDED"
		message = "The durable operation deadline was exceeded."
	case OperationFailureEffectDenied:
		code = "OPERATION_EFFECT_DENIED"
		message = "The operation was denied before its external effect could be applied."
	case OperationFailureEffectInvalid:
		code = "OPERATION_EFFECT_INVALID"
		message = "The operation could not safely apply its external effect."
	case OperationFailureRuntime:
		code = "OPERATION_RUNTIME_FAILED"
		message = "The runtime completed with a terminal failure."
	default:
		return domain.APIError{}, fmt.Errorf("operation failure reason %q is invalid", reason)
	}
	return domain.APIError{
		Code: code, Message: message, CorrelationID: correlationID, Retryable: false,
	}, nil
}

// ScheduleRetry releases a valid lease and persists the next durable due time.
func (repository *Repository) ScheduleRetry(
	ctx context.Context,
	claim OperationClaim,
	classification string,
	errorCode string,
	dueAt time.Time,
) error {
	table, _, _, err := operationTable(claim.Kind)
	if err != nil {
		return err
	}
	if classification != "retryable" && classification != "unknown" {
		return fmt.Errorf("classification %q cannot schedule a retry", classification)
	}
	now := repository.now()
	encodedError, err := json.Marshal(domain.APIError{
		Code:          errorCode,
		Message:       "The operation will be reconciled again.",
		CorrelationID: claim.CorrelationID,
		Retryable:     true,
	})
	if err != nil {
		return fmt.Errorf("encode retry error: %w", err)
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin retry transition: %w", err)
	}
	defer tx.Rollback(ctx)
	query := fmt.Sprintf(`
		UPDATE %s
		SET error_classification = $6, error = $7, due_at = $8,
		    lease_owner = NULL, lease_expires_at = NULL, updated_at = $9
		WHERE isolation_domain_id = $1 AND id = $2
		  AND observed_state = $3
		  AND lease_owner = $4 AND lease_token = $5
		  AND lease_expires_at > $9
	`, table)
	result, err := tx.Exec(ctx, query,
		claim.IsolationDomainID, claim.ID, claim.ObservedState,
		claim.LeaseOwner, claim.FencingToken, classification, encodedError, dueAt, now,
	)
	if err != nil {
		return fmt.Errorf("schedule %s retry: %w", claim.Kind, err)
	}
	if result.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	if err := writeOutboxAndAudit(
		ctx, tx, claim.IsolationDomainID, claim.Kind, claim.ID,
		claim.Kind+".retry-scheduled", claim.ActorID, claim.CorrelationID,
		"accepted", claim.ID, now,
	); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit retry transition: %w", err)
	}
	return nil
}

func (repository *Repository) PrepareEffect(
	ctx context.Context,
	claim OperationClaim,
	phase string,
	requestDigest [32]byte,
) (EffectRecord, error) {
	effectID := identity.Derived("eff", claim.IsolationDomainID+":"+claim.Kind+":"+claim.ID+":"+phase)
	now := repository.now()
	table, _, _, err := operationTable(claim.Kind)
	if err != nil {
		return EffectRecord{}, err
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return EffectRecord{}, fmt.Errorf("begin effect preparation: %w", err)
	}
	defer tx.Rollback(ctx)
	var active int
	leaseQuery := fmt.Sprintf(`
		SELECT 1 FROM %s
		WHERE isolation_domain_id = $1 AND id = $2
		  AND command = $3 AND observed_state = $4
		  AND lease_owner = $5 AND lease_token = $6
		  AND lease_expires_at > $7
	`, table)
	if err := tx.QueryRow(ctx, leaseQuery,
		claim.IsolationDomainID, claim.ID, claim.Command, claim.ObservedState,
		claim.LeaseOwner, claim.FencingToken, now,
	).Scan(&active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EffectRecord{}, ErrLeaseLost
		}
		return EffectRecord{}, fmt.Errorf("verify effect lease: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO external_effects (
			isolation_domain_id, effect_id, operation_kind, operation_id, phase,
			request_digest, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'prepared', $7, $7)
		ON CONFLICT (isolation_domain_id, effect_id) DO NOTHING
	`, claim.IsolationDomainID, effectID, claim.Kind, claim.ID, phase, requestDigest[:], now)
	if err != nil {
		return EffectRecord{}, fmt.Errorf("prepare external effect: %w", err)
	}
	effect, err := getEffect(ctx, tx, claim.IsolationDomainID, effectID)
	if err != nil {
		return EffectRecord{}, err
	}
	if effect.RequestDigest != requestDigest {
		return EffectRecord{}, &DomainError{Code: "EXTERNAL_EFFECT_CONFLICT", Message: "External effect identifier was reused with a different request."}
	}
	if err := tx.Commit(ctx); err != nil {
		return EffectRecord{}, fmt.Errorf("commit effect preparation: %w", err)
	}
	return effect, nil
}

func (repository *Repository) GetEffect(ctx context.Context, isolationDomainID, effectID string) (EffectRecord, error) {
	return getEffect(ctx, repository.pool, isolationDomainID, effectID)
}

func getEffect(ctx context.Context, querier operationQuerier, isolationDomainID, effectID string) (EffectRecord, error) {
	var effect EffectRecord
	var observation, requestDigest []byte
	err := querier.QueryRow(ctx, `
		SELECT isolation_domain_id, effect_id, operation_kind, operation_id,
		       phase, request_digest, status, attempt, observation
		FROM external_effects
		WHERE isolation_domain_id = $1 AND effect_id = $2
	`, isolationDomainID, effectID).Scan(
		&effect.IsolationDomainID, &effect.EffectID, &effect.OperationKind,
		&effect.OperationID, &effect.Phase, &requestDigest, &effect.Status, &effect.Attempt, &observation,
	)
	if err != nil {
		return EffectRecord{}, err
	}
	if len(observation) > 0 {
		if err := json.Unmarshal(observation, &effect.Observation); err != nil {
			return EffectRecord{}, fmt.Errorf("decode effect observation: %w", err)
		}
	}
	copy(effect.RequestDigest[:], requestDigest)
	return effect, nil
}

func (repository *Repository) RecordEffect(
	ctx context.Context,
	effect EffectRecord,
	status string,
	observation map[string]any,
	errorCode string,
) error {
	if status != "unknown" && status != "succeeded" && status != "failed" {
		return fmt.Errorf("invalid effect status %q", status)
	}
	encodedObservation, err := marshalNullable(observation)
	if err != nil {
		return fmt.Errorf("encode effect observation: %w", err)
	}
	result, err := repository.pool.Exec(ctx, `
		UPDATE external_effects
		SET status = $3, observation = $4, last_error_code = NULLIF($5, ''),
		    attempt = attempt + 1, updated_at = $6
		WHERE isolation_domain_id = $1 AND effect_id = $2
	`, effect.IsolationDomainID, effect.EffectID, status, encodedObservation, errorCode, repository.now())
	if err != nil {
		return fmt.Errorf("record external effect: %w", err)
	}
	if result.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

// RepairOperation requeues only a failed operation. The original request actor
// remains immutable while the repair actor becomes the principal for later effects.
func (repository *Repository) RepairOperation(
	ctx context.Context,
	kind string,
	isolationDomainID string,
	operationID string,
	actorID string,
	reason string,
	deduplicationID string,
	newDeadline time.Time,
) error {
	if actorID == "" || reason == "" || deduplicationID == "" || !newDeadline.After(repository.now()) {
		return errors.New("repair requires actor, reason, deduplication ID, and a future deadline")
	}
	table, _, _, err := operationTable(kind)
	if err != nil {
		return err
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin repair: %w", err)
	}
	defer tx.Rollback(ctx)
	now := repository.now()
	digest := sha256.Sum256([]byte(kind + ":" + operationID + ":" + reason + ":" + newDeadline.Format(time.RFC3339Nano)))
	result, err := tx.Exec(ctx, `
		INSERT INTO inbox_records (
			isolation_domain_id, source_kind, deduplication_id,
			payload_digest, actor_id, processed_at, created_at
		) VALUES ($1, 'command', $2, $3, $4, $5, $5)
		ON CONFLICT DO NOTHING
	`, isolationDomainID, deduplicationID, digest[:], actorID, now)
	if err != nil {
		return fmt.Errorf("deduplicate repair: %w", err)
	}
	if result.RowsAffected() == 0 {
		var existingDigest []byte
		var existingActorID *string
		if err := tx.QueryRow(ctx, `
			SELECT payload_digest, actor_id FROM inbox_records
			WHERE isolation_domain_id = $1 AND source_kind = 'command' AND deduplication_id = $2
		`, isolationDomainID, deduplicationID).Scan(&existingDigest, &existingActorID); err != nil {
			return fmt.Errorf("read repair replay: %w", err)
		}
		if !bytes.Equal(existingDigest, digest[:]) || existingActorID == nil || *existingActorID != actorID {
			return &DomainError{Code: "IDEMPOTENCY_KEY_REUSED", Message: "Repair deduplication ID was reused with different content."}
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit repair replay: %w", err)
		}
		return nil
	}
	query := fmt.Sprintf(`
		UPDATE %s
		SET command = 'repair', observed_state = 'queued',
		    generation = generation + 1, due_at = $3, deadline_at = $4,
		    effect_actor_id = $5, effect_correlation_id = $6,
		    error_classification = NULL, error = NULL, terminal_result = NULL,
		    lease_owner = NULL, lease_expires_at = NULL,
		    last_transition_at = $3, updated_at = $3
		WHERE isolation_domain_id = $1 AND id = $2 AND observed_state = 'failed'
	`, table)
	result, err = tx.Exec(ctx, query, isolationDomainID, operationID, now, newDeadline, actorID, deduplicationID)
	if err != nil {
		return fmt.Errorf("requeue failed operation: %w", err)
	}
	if result.RowsAffected() != 1 {
		return &DomainError{Code: "OPERATION_NOT_REPAIRABLE", Message: "Only a failed operation can be repaired."}
	}
	if err := writeOutboxAndAudit(
		ctx, tx, isolationDomainID, kind, operationID, kind+".repair-accepted",
		actorID, deduplicationID, "accepted", operationID, now,
	); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit repair: %w", err)
	}
	return nil
}

func (repository *Repository) updateResourceForTransition(
	ctx context.Context,
	tx pgx.Tx,
	claim OperationClaim,
	nextState string,
	terminalResult map[string]any,
	now time.Time,
) error {
	switch claim.Kind {
	case OperationKindPublication:
		if nextState != string(publication.StatePublished) {
			return nil
		}
		_, err := tx.Exec(ctx, `
			UPDATE service_revisions
			SET state = 'published', published_at = $3,
			    generation = generation + 1, version = version + 1, updated_at = $3
			WHERE isolation_domain_id = $1 AND id = $2 AND state = 'draft'
		`, claim.IsolationDomainID, claim.ResourceID, now)
		if err != nil {
			return fmt.Errorf("publish service revision: %w", err)
		}
	case OperationKindInvocation:
		resourceState := invocationResourceState(nextState)
		if resourceState == "" {
			return nil
		}
		encodedResult, err := marshalNullable(terminalResult)
		if err != nil {
			return fmt.Errorf("encode invocation result: %w", err)
		}
		_, err = tx.Exec(ctx, `
			UPDATE invocations
			SET state = $3, result = COALESCE($4, result),
			    completed_at = CASE WHEN $3 IN ('succeeded', 'failed', 'cancelled') THEN $5 ELSE completed_at END,
			    generation = generation + 1, version = version + 1, updated_at = $5
			WHERE isolation_domain_id = $1 AND id = $2
		`, claim.IsolationDomainID, claim.ResourceID, resourceState, encodedResult, now)
		if err != nil {
			return fmt.Errorf("update invocation state: %w", err)
		}
		invocationValue, err := getInvocationForUpdate(ctx, tx, claim.IsolationDomainID, claim.ResourceID)
		if err != nil {
			return err
		}
		if err := writeInvocationEvent(
			ctx, tx, invocationValue, "lifecycle."+resourceState, claim.ActorID,
			map[string]any{"state": resourceState}, now,
		); err != nil {
			return err
		}
	}
	return nil
}

func operationTable(kind string) (table string, resourceColumn string, terminalStates []string, err error) {
	switch kind {
	case OperationKindPublication:
		return "service_publication_operations", "revision_id", []string{"published", "failed", "cancelled"}, nil
	case OperationKindInvocation:
		return "invocation_execution_operations", "invocation_id", []string{"succeeded", "failed", "cancelled", "waiting"}, nil
	default:
		return "", "", nil, fmt.Errorf("unsupported operation kind %q", kind)
	}
}

func validateTransition(kind, from, to string) error {
	switch kind {
	case OperationKindPublication:
		return publication.ValidateTransition(publication.State(from), publication.State(to))
	case OperationKindInvocation:
		return invocation.ValidateTransition(invocation.State(from), invocation.State(to))
	default:
		return fmt.Errorf("unsupported operation kind %q", kind)
	}
}

func transitionOutcome(state string) string {
	switch state {
	case "published", "succeeded":
		return "succeeded"
	case "failed":
		return "failed"
	case "cancelled":
		return "cancelled"
	default:
		return "accepted"
	}
}

func invocationResourceState(operationState string) string {
	switch operationState {
	case "starting", "running", "observing":
		return "running"
	case "waiting":
		return "waiting"
	case "cancelling":
		return "cancelling"
	case "succeeded", "failed", "cancelled":
		return operationState
	default:
		return ""
	}
}
