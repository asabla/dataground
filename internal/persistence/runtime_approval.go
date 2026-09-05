package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
)

const InvocationRuntimeApprovalContract = "dataground.invocation-runtime-approval/v1"

var (
	ErrInvocationRuntimeApprovalInvalid           = errors.New("invocation runtime approval is invalid")
	ErrInvocationRuntimeApprovalMissing           = errors.New("invocation runtime approval is missing")
	ErrInvocationRuntimeApprovalConflict          = errors.New("invocation runtime approval conflicts with durable state")
	ErrInvocationRuntimeApprovalDeliveryAmbiguous = errors.New("invocation runtime approval delivery is ambiguous")
	approvalIDPattern                             = regexp.MustCompile(`^apr_[0-9a-z]{20,32}$`)
	approvalInvocationPattern                     = regexp.MustCompile(`^inv_[0-9a-z]{20,32}$`)
	approvalCorrelationPattern                    = regexp.MustCompile(`^cor_[0-9a-z]{20,32}$`)
)

type InvocationRuntimeApproval struct {
	Contract                string
	IsolationDomainID       string
	ID                      string
	OperationID             string
	InvocationID            string
	ServiceID               string
	RevisionID              string
	EffectID                string
	SourceSequence          uint64
	RequestedAction         string
	State                   string
	Version                 int64
	Decision                string
	EffectiveDecision       string
	ResolvedBy              string
	ResolutionCorrelationID string
	ResolvedAt              time.Time
	DeliveredAt             time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type InvocationRuntimeApprovalRequest struct {
	SourceSequence  uint64
	RequestedAction string
}

type InvocationRuntimeApprovalResolution struct {
	IsolationDomainID string
	InvocationID      string
	ApprovalID        string
	ExpectedVersion   int64
	Decision          string
	ActorID           string
	CorrelationID     string
}

type InvocationRuntimeApprovalEntryAuthorizer func(
	context.Context,
	InvocationRuntimeApproval,
) error

func (repository *Repository) RecordInvocationRuntimeApprovalRequest(
	ctx context.Context,
	claim OperationClaim,
	effect EffectRecord,
	target InvocationRuntimeTarget,
	request InvocationRuntimeApprovalRequest,
) (InvocationRuntimeApproval, error) {
	if repository == nil || repository.pool == nil || ctx == nil ||
		!validInvocationRuntimeClaim(claim) ||
		!validInvocationRuntimeAttempt(claim, effect) ||
		!invocationRuntimeTargetMatchesApproval(target, claim) ||
		request.SourceSequence == 0 ||
		!validInvocationRuntimeApprovalAction(request.RequestedAction) {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("begin invocation runtime approval request: %w", err)
	}
	defer tx.Rollback(ctx)
	invocationID, err := lockInvocationRuntimeClaim(ctx, tx, claim, repository.now())
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if invocationID != target.InvocationID {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	if err := verifyInvocationRuntimeEffect(ctx, tx, effect); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if err := verifyReservedInvocationRuntimeAttempt(ctx, tx, effect); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	id := identity.Derived(
		"apr",
		target.IsolationDomainID+":"+target.OperationID+":"+strconv.FormatUint(request.SourceSequence, 10),
	)
	existing, found, err := getInvocationRuntimeApproval(ctx, tx, target.IsolationDomainID, id, false)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if found {
		if existing.OperationID != target.OperationID ||
			existing.InvocationID != target.InvocationID ||
			existing.ServiceID != target.ServiceID ||
			existing.RevisionID != target.RevisionID ||
			existing.EffectID != effect.EffectID ||
			existing.SourceSequence != request.SourceSequence ||
			existing.RequestedAction != request.RequestedAction {
			return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
		}
		if err := repository.recordInvocationRuntimeApprovalEvent(ctx, tx, claim, target, existing); err != nil {
			return InvocationRuntimeApproval{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return InvocationRuntimeApproval{}, fmt.Errorf("commit invocation runtime approval replay: %w", err)
		}
		return existing, nil
	}
	now := repository.now()
	if _, err := tx.Exec(ctx, `
		INSERT INTO invocation_runtime_approvals (
			contract, isolation_domain_id, id, operation_id, invocation_id,
			service_id, revision_id, effect_id, source_sequence, requested_action,
			state, version, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'pending', 1, $11, $11)
	`, InvocationRuntimeApprovalContract, target.IsolationDomainID, id,
		target.OperationID, target.InvocationID, target.ServiceID, target.RevisionID,
		effect.EffectID, request.SourceSequence, request.RequestedAction, now); err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("persist invocation runtime approval request: %w", err)
	}
	approval, _, err := getInvocationRuntimeApproval(ctx, tx, target.IsolationDomainID, id, false)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if err := repository.recordInvocationRuntimeApprovalEvent(ctx, tx, claim, target, approval); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("commit invocation runtime approval request: %w", err)
	}
	return approval, nil
}

func (repository *Repository) GetInvocationRuntimeApproval(
	ctx context.Context,
	isolationDomainID string,
	approvalID string,
) (InvocationRuntimeApproval, error) {
	if repository == nil || repository.pool == nil || ctx == nil ||
		isolationDomainID == "" || !approvalIDPattern.MatchString(approvalID) {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalInvalid
	}
	value, found, err := getInvocationRuntimeApproval(
		ctx, repository.pool, isolationDomainID, approvalID, false,
	)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if !found {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalMissing
	}
	return value, nil
}

// GetInvocationApproval returns the provider-neutral public representation of
// an approval only when it belongs to the named invocation.
func (repository *Repository) GetInvocationApproval(
	ctx context.Context,
	isolationDomainID string,
	invocationID string,
	approvalID string,
) (domain.InvocationApproval, error) {
	if repository == nil || repository.pool == nil || ctx == nil ||
		isolationDomainID == "" ||
		!approvalInvocationPattern.MatchString(invocationID) ||
		!approvalIDPattern.MatchString(approvalID) {
		return domain.InvocationApproval{}, ErrInvocationRuntimeApprovalInvalid
	}
	value, found, err := getInvocationRuntimeApproval(
		ctx, repository.pool, isolationDomainID, approvalID, false,
	)
	if err != nil {
		return domain.InvocationApproval{}, err
	}
	if !found || value.InvocationID != invocationID {
		return domain.InvocationApproval{}, ErrInvocationRuntimeApprovalMissing
	}
	return publicInvocationApproval(value), nil
}

func (repository *Repository) ResolveInvocationRuntimeApproval(
	ctx context.Context,
	resolution InvocationRuntimeApprovalResolution,
) (InvocationRuntimeApproval, error) {
	if repository == nil || repository.pool == nil || ctx == nil ||
		!validInvocationRuntimeApprovalResolution(resolution) {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("begin invocation runtime approval resolution: %w", err)
	}
	defer tx.Rollback(ctx)
	approval, err := resolveInvocationRuntimeApproval(ctx, tx, resolution, repository.now(), nil)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("commit invocation runtime approval resolution: %w", err)
	}
	return approval, nil
}

func (repository *Repository) ResolveInvocationRuntimeApprovalCommand(
	ctx context.Context,
	idempotency Idempotency,
	resolution InvocationRuntimeApprovalResolution,
	authorize InvocationRuntimeApprovalEntryAuthorizer,
) (CommandResult, error) {
	if repository == nil || repository.pool == nil || ctx == nil || authorize == nil ||
		!validInvocationRuntimeApprovalResolution(resolution) ||
		idempotency.IsolationDomainID != resolution.IsolationDomainID {
		return CommandResult{}, ErrInvocationRuntimeApprovalInvalid
	}
	return repository.execute(ctx, idempotency, func(tx pgx.Tx, now time.Time) (int, any, error) {
		approval, err := resolveInvocationRuntimeApproval(ctx, tx, resolution, now, authorize)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, publicInvocationApproval(approval), nil
	})
}

func resolveInvocationRuntimeApproval(
	ctx context.Context,
	tx pgx.Tx,
	resolution InvocationRuntimeApprovalResolution,
	now time.Time,
	authorize InvocationRuntimeApprovalEntryAuthorizer,
) (InvocationRuntimeApproval, error) {
	approval, found, err := getInvocationRuntimeApproval(
		ctx, tx, resolution.IsolationDomainID, resolution.ApprovalID, true,
	)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if !found {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalMissing
	}
	if approval.InvocationID != resolution.InvocationID {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalMissing
	}
	candidate := approval
	candidate.Decision = resolution.Decision
	candidate.ResolvedBy = resolution.ActorID
	candidate.ResolutionCorrelationID = resolution.CorrelationID
	if approval.State != "pending" {
		if !sameInvocationRuntimeApprovalResolution(approval, resolution) {
			return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
		}
		if authorize != nil {
			if err := authorize(ctx, candidate); err != nil {
				return InvocationRuntimeApproval{}, err
			}
		}
		return approval, nil
	}
	if approval.Version != resolution.ExpectedVersion {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT true
		FROM invocation_execution_operations AS operation
		JOIN invocation_runtime_attempts AS attempt
		  ON attempt.isolation_domain_id = operation.isolation_domain_id
		 AND attempt.operation_id = operation.id
		WHERE operation.isolation_domain_id = $1
		  AND operation.id = $2
		  AND operation.invocation_id = $3
		  AND operation.observed_state = 'running'
		  AND operation.lease_owner IS NOT NULL
		  AND operation.lease_expires_at > clock_timestamp()
		  AND operation.deadline_at > clock_timestamp()
		  AND attempt.effect_id = $4
		  AND attempt.status = 'reserved'
	`, approval.IsolationDomainID, approval.OperationID,
		approval.InvocationID, approval.EffectID).Scan(&active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
		}
		return InvocationRuntimeApproval{}, fmt.Errorf("validate invocation runtime approval target: %w", err)
	}
	if !active {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	if authorize != nil {
		if err := authorize(ctx, candidate); err != nil {
			return InvocationRuntimeApproval{}, err
		}
	}
	result, err := tx.Exec(ctx, `
		UPDATE invocation_runtime_approvals
		SET state = 'resolved', version = version + 1, decision = $3,
		    resolved_by = $4, resolution_correlation_id = $5,
		    resolved_at = $6, updated_at = $6
		WHERE isolation_domain_id = $1 AND id = $2
		  AND state = 'pending' AND version = $7
	`, resolution.IsolationDomainID, resolution.ApprovalID,
		resolution.Decision, resolution.ActorID, resolution.CorrelationID,
		now, resolution.ExpectedVersion)
	if err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("resolve invocation runtime approval: %w", err)
	}
	if result.RowsAffected() != 1 {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, 'invocation-approval.resolve',
			'invocation-approval', $4, 'accepted', $5,
			jsonb_build_object('decision', $6::text, 'version', $7::bigint), $8
		)
	`, identity.New("aud"), approval.IsolationDomainID, resolution.ActorID,
		approval.ID, resolution.CorrelationID, resolution.Decision,
		resolution.ExpectedVersion+1, now); err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("audit invocation runtime approval resolution: %w", err)
	}
	if err := recordInvocationRuntimeApprovalResolutionEvent(
		ctx, tx, approval, resolution, now,
	); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	approval, _, err = getInvocationRuntimeApproval(
		ctx, tx, resolution.IsolationDomainID, resolution.ApprovalID, false,
	)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	return approval, nil
}

func (repository *Repository) BeginInvocationRuntimeApprovalDelivery(
	ctx context.Context,
	claim OperationClaim,
	effect EffectRecord,
	approvalID string,
	effectiveDecision string,
) (InvocationRuntimeApproval, error) {
	if repository == nil || repository.pool == nil || ctx == nil ||
		!validInvocationRuntimeClaim(claim) ||
		!validInvocationRuntimeAttempt(claim, effect) ||
		!approvalIDPattern.MatchString(approvalID) ||
		!validInvocationRuntimeApprovalDecision(effectiveDecision) {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("begin invocation runtime approval delivery: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := lockInvocationRuntimeClaim(ctx, tx, claim, repository.now()); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if err := verifyInvocationRuntimeEffect(ctx, tx, effect); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if err := verifyReservedInvocationRuntimeAttempt(ctx, tx, effect); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	approval, found, err := getInvocationRuntimeApproval(
		ctx, tx, claim.IsolationDomainID, approvalID, true,
	)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if !found {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalMissing
	}
	if approval.OperationID != claim.ID || approval.EffectID != effect.EffectID {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	if approval.State == "delivering" {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalDeliveryAmbiguous
	}
	if approval.State == "delivered" {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	if approval.State != "resolved" || approval.EffectiveDecision != "" {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	now := repository.now()
	result, err := tx.Exec(ctx, `
		UPDATE invocation_runtime_approvals
		SET state = 'delivering', version = version + 1,
		    effective_decision = $3, updated_at = $4
		WHERE isolation_domain_id = $1 AND id = $2 AND state = 'resolved'
	`, claim.IsolationDomainID, approvalID, effectiveDecision, now)
	if err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("reserve invocation runtime approval delivery: %w", err)
	}
	if result.RowsAffected() != 1 {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	approval, _, err = getInvocationRuntimeApproval(
		ctx, tx, claim.IsolationDomainID, approvalID, false,
	)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("commit invocation runtime approval delivery: %w", err)
	}
	return approval, nil
}

func (repository *Repository) CompleteInvocationRuntimeApprovalDelivery(
	ctx context.Context,
	claim OperationClaim,
	effect EffectRecord,
	approvalID string,
) (InvocationRuntimeApproval, error) {
	if repository == nil || repository.pool == nil || ctx == nil ||
		!validInvocationRuntimeClaim(claim) ||
		!validInvocationRuntimeAttempt(claim, effect) ||
		!approvalIDPattern.MatchString(approvalID) {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("begin invocation runtime approval completion: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := lockInvocationRuntimeClaim(ctx, tx, claim, repository.now()); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if err := verifyInvocationRuntimeEffect(ctx, tx, effect); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if err := verifyReservedInvocationRuntimeAttempt(ctx, tx, effect); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	approval, found, err := getInvocationRuntimeApproval(
		ctx, tx, claim.IsolationDomainID, approvalID, true,
	)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if !found {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalMissing
	}
	if approval.OperationID != claim.ID || approval.EffectID != effect.EffectID {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	if approval.State == "delivered" {
		if err := tx.Commit(ctx); err != nil {
			return InvocationRuntimeApproval{}, fmt.Errorf("commit invocation runtime approval delivery replay: %w", err)
		}
		return approval, nil
	}
	if approval.State != "delivering" || approval.EffectiveDecision == "" {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	now := repository.now()
	result, err := tx.Exec(ctx, `
		UPDATE invocation_runtime_approvals
		SET state = 'delivered', version = version + 1,
		    delivered_at = $3, updated_at = $3
		WHERE isolation_domain_id = $1 AND id = $2 AND state = 'delivering'
	`, claim.IsolationDomainID, approvalID, now)
	if err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("complete invocation runtime approval delivery: %w", err)
	}
	if result.RowsAffected() != 1 {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	approval, _, err = getInvocationRuntimeApproval(
		ctx, tx, claim.IsolationDomainID, approvalID, false,
	)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeApproval{}, fmt.Errorf("commit invocation runtime approval completion: %w", err)
	}
	return approval, nil
}

func verifyReservedInvocationRuntimeAttempt(
	ctx context.Context,
	querier invocationRuntimeApprovalQuerier,
	effect EffectRecord,
) error {
	var reserved bool
	err := querier.QueryRow(ctx, `
		SELECT true
		FROM invocation_runtime_attempts
		WHERE isolation_domain_id = $1
		  AND operation_id = $2
		  AND effect_id = $3
		  AND status = 'reserved'
	`, effect.IsolationDomainID, effect.OperationID, effect.EffectID).Scan(&reserved)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvocationRuntimeApprovalConflict
	}
	if err != nil {
		return fmt.Errorf("verify reserved invocation runtime attempt: %w", err)
	}
	if !reserved {
		return ErrInvocationRuntimeApprovalConflict
	}
	return nil
}

func (repository *Repository) recordInvocationRuntimeApprovalEvent(
	ctx context.Context, tx pgx.Tx, claim OperationClaim, target InvocationRuntimeTarget, approval InvocationRuntimeApproval,
) error {
	return repository.recordInvocationRuntimeInteractionEvent(ctx, tx, claim, target, approval.SourceSequence, "interaction.approval.requested", map[string]any{
		"approvalId": approval.ID, "action": approval.RequestedAction, "version": int64(1),
	})
}

func (repository *Repository) recordInvocationRuntimeInteractionEvent(
	ctx context.Context, tx pgx.Tx, claim OperationClaim, target InvocationRuntimeTarget, sourceSequence uint64, eventType string, payload map[string]any,
) error {
	encodedPayload, err := json.Marshal(payload)
	if err != nil || len(encodedPayload) > maximumInvocationRuntimeEventPayloadBytes {
		return ErrInvocationRuntimeEventInvalid
	}
	existing, found, err := getInvocationRuntimeEvent(
		ctx, tx, target.IsolationDomainID, target.InvocationID, sourceSequence,
	)
	if err != nil {
		return err
	}
	if found {
		encodedExisting, encodeErr := json.Marshal(existing.Payload)
		if encodeErr != nil {
			return fmt.Errorf("encode persisted invocation interaction event: %w", encodeErr)
		}
		if existing.Type != eventType ||
			!bytes.Equal(encodedExisting, encodedPayload) {
			return ErrInvocationRuntimeEventConflict
		}
		return nil
	}
	var sequence uint64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(sequence), 0) + 1
		FROM invocation_events
		WHERE isolation_domain_id = $1 AND invocation_id = $2
	`, target.IsolationDomainID, target.InvocationID).Scan(&sequence); err != nil {
		return fmt.Errorf("allocate invocation interaction event sequence: %w", err)
	}
	now := repository.now()
	value := domain.EventEnvelope{
		Source:            "runtime",
		SchemaVersion:     "dataground.event/v1",
		ID:                identity.Derived("evt", target.InvocationID+":runtime:"+strconv.FormatUint(sourceSequence, 10)),
		IsolationDomainID: target.IsolationDomainID,
		InvocationID:      target.InvocationID,
		Sequence:          sequence,
		Type:              eventType,
		OccurredAt:        now,
		RecordedAt:        now,
		CorrelationID:     target.CorrelationID,
		ActorID:           target.ActorID,
		ServiceID:         target.ServiceID,
		RevisionID:        target.RevisionID,
		Payload:           payload,
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
		value.ServiceID, value.RevisionID, encodedPayload, sourceSequence,
		claim.ID, claim.Command, claim.ObservedState, claim.LeaseOwner, claim.FencingToken,
	)
	if err != nil {
		return fmt.Errorf("persist invocation interaction event: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

type invocationRuntimeApprovalQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getInvocationRuntimeApproval(
	ctx context.Context,
	querier invocationRuntimeApprovalQuerier,
	isolationDomainID string,
	approvalID string,
	forUpdate bool,
) (InvocationRuntimeApproval, bool, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	var value InvocationRuntimeApproval
	var resolvedAt, deliveredAt *time.Time
	err := querier.QueryRow(ctx, `
		SELECT contract, isolation_domain_id, id, operation_id, invocation_id,
		       service_id, revision_id, effect_id, source_sequence, requested_action,
		       state, version, COALESCE(decision, ''), COALESCE(effective_decision, ''),
		       COALESCE(resolved_by, ''), COALESCE(resolution_correlation_id, ''),
		       resolved_at, delivered_at, created_at, updated_at
		FROM invocation_runtime_approvals
		WHERE isolation_domain_id = $1 AND id = $2`+suffix,
		isolationDomainID, approvalID).Scan(
		&value.Contract, &value.IsolationDomainID, &value.ID, &value.OperationID,
		&value.InvocationID, &value.ServiceID, &value.RevisionID, &value.EffectID,
		&value.SourceSequence, &value.RequestedAction, &value.State, &value.Version,
		&value.Decision, &value.EffectiveDecision, &value.ResolvedBy,
		&value.ResolutionCorrelationID, &resolvedAt, &deliveredAt,
		&value.CreatedAt, &value.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationRuntimeApproval{}, false, nil
	}
	if err != nil {
		return InvocationRuntimeApproval{}, false, fmt.Errorf("read invocation runtime approval: %w", err)
	}
	if resolvedAt != nil {
		value.ResolvedAt = *resolvedAt
	}
	if deliveredAt != nil {
		value.DeliveredAt = *deliveredAt
	}
	if value.Contract != InvocationRuntimeApprovalContract ||
		value.IsolationDomainID != isolationDomainID ||
		value.ID != approvalID ||
		value.OperationID == "" || value.InvocationID == "" ||
		value.ServiceID == "" || value.RevisionID == "" || value.EffectID == "" ||
		value.SourceSequence == 0 || !validInvocationRuntimeApprovalAction(value.RequestedAction) ||
		!validInvocationRuntimeApprovalState(value) {
		return InvocationRuntimeApproval{}, false, ErrInvocationRuntimeApprovalConflict
	}
	return value, true, nil
}

func validInvocationRuntimeApprovalState(value InvocationRuntimeApproval) bool {
	resolved := validInvocationRuntimeApprovalDecision(value.Decision) &&
		validInvocationRuntimeApprovalActor(value.ResolvedBy) &&
		approvalCorrelationPattern.MatchString(value.ResolutionCorrelationID) &&
		!value.ResolvedAt.IsZero()
	switch value.State {
	case "pending":
		return value.Version == 1 &&
			value.Decision == "" && value.EffectiveDecision == "" &&
			value.ResolvedBy == "" && value.ResolutionCorrelationID == "" &&
			value.ResolvedAt.IsZero() && value.DeliveredAt.IsZero()
	case "resolved":
		return value.Version == 2 && resolved &&
			value.EffectiveDecision == "" && value.DeliveredAt.IsZero()
	case "delivering":
		return value.Version == 3 && resolved &&
			validInvocationRuntimeApprovalDecision(value.EffectiveDecision) &&
			value.DeliveredAt.IsZero()
	case "delivered":
		return value.Version == 4 && resolved &&
			validInvocationRuntimeApprovalDecision(value.EffectiveDecision) &&
			!value.DeliveredAt.IsZero() && !value.DeliveredAt.Before(value.ResolvedAt)
	default:
		return false
	}
}

func invocationRuntimeTargetMatchesApproval(
	target InvocationRuntimeTarget,
	claim OperationClaim,
) bool {
	return target.IsolationDomainID == claim.IsolationDomainID &&
		target.OperationID == claim.ID &&
		target.InvocationID == claim.ResourceID &&
		target.ServiceID != "" && target.RevisionID != "" &&
		validInvocationRuntimeApprovalActor(target.ActorID) &&
		approvalCorrelationPattern.MatchString(target.CorrelationID)
}

func validInvocationRuntimeApprovalAction(value string) bool {
	return value == "process.execute" || value == "workspace.change"
}

func validInvocationRuntimeApprovalDecision(value string) bool {
	return value == "approve" || value == "deny"
}

func validInvocationRuntimeApprovalActor(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validInvocationRuntimeApprovalResolution(
	resolution InvocationRuntimeApprovalResolution,
) bool {
	return resolution.IsolationDomainID != "" &&
		approvalInvocationPattern.MatchString(resolution.InvocationID) &&
		approvalIDPattern.MatchString(resolution.ApprovalID) &&
		resolution.ExpectedVersion == 1 &&
		validInvocationRuntimeApprovalDecision(resolution.Decision) &&
		validInvocationRuntimeApprovalActor(resolution.ActorID) &&
		approvalCorrelationPattern.MatchString(resolution.CorrelationID)
}

func publicInvocationApproval(value InvocationRuntimeApproval) domain.InvocationApproval {
	result := domain.InvocationApproval{
		SchemaVersion:     domain.InvocationApprovalSchemaV1,
		ID:                value.ID,
		IsolationDomainID: value.IsolationDomainID,
		InvocationID:      value.InvocationID,
		RequestedAction:   value.RequestedAction,
		State:             value.State,
		Version:           value.Version,
		Decision:          value.Decision,
		ResolvedBy:        value.ResolvedBy,
		CreatedAt:         value.CreatedAt,
		UpdatedAt:         value.UpdatedAt,
	}
	if !value.ResolvedAt.IsZero() {
		resolvedAt := value.ResolvedAt
		result.ResolvedAt = &resolvedAt
	}
	return result
}

func recordInvocationRuntimeApprovalResolutionEvent(
	ctx context.Context,
	tx pgx.Tx,
	approval InvocationRuntimeApproval,
	resolution InvocationRuntimeApprovalResolution,
	now time.Time,
) error {
	payload, err := json.Marshal(map[string]any{
		"approvalId": approval.ID,
		"decision":   resolution.Decision,
		"version":    resolution.ExpectedVersion + 1,
	})
	if err != nil || len(payload) > maximumInvocationRuntimeEventPayloadBytes {
		return ErrInvocationRuntimeEventInvalid
	}
	var sequence uint64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(sequence), 0) + 1
		FROM invocation_events
		WHERE isolation_domain_id = $1 AND invocation_id = $2
	`, approval.IsolationDomainID, approval.InvocationID).Scan(&sequence); err != nil {
		return fmt.Errorf("allocate invocation approval resolution event sequence: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invocation_events (
			isolation_domain_id, invocation_id, id, sequence, schema_version,
			event_type, occurred_at, recorded_at, correlation_id, actor_id,
			service_id, revision_id, payload
		) VALUES (
			$1, $2, $3, $4, 'dataground.event/v1',
			'interaction.approval.resolved', $5, $5, $6, $7,
			$8, $9, $10
		)
	`, approval.IsolationDomainID, approval.InvocationID,
		identity.Derived("evt", approval.InvocationID+":"+approval.ID+":resolved:v1"),
		sequence, now, resolution.CorrelationID, resolution.ActorID,
		approval.ServiceID, approval.RevisionID, payload,
	); err != nil {
		return fmt.Errorf("persist invocation approval resolution event: %w", err)
	}
	return nil
}

func sameInvocationRuntimeApprovalResolution(
	approval InvocationRuntimeApproval,
	resolution InvocationRuntimeApprovalResolution,
) bool {
	return approval.Decision == resolution.Decision &&
		approval.ResolvedBy == resolution.ActorID &&
		approval.ResolutionCorrelationID == resolution.CorrelationID
}
