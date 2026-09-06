package persistence

import (
	"context"
	"strconv"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
)

// CloseInvocationRuntimeApproval retires a request cleared by its live runtime.
// Once delivery has begun, closure records uncertainty rather than permission
// to send the decision again.
func (repository *Repository) CloseInvocationRuntimeApproval(ctx context.Context, claim OperationClaim, effect EffectRecord, id, reason string) (InvocationRuntimeApproval, error) {
	if repository == nil || repository.pool == nil || ctx == nil || !approvalIDPattern.MatchString(id) || !validInvocationRuntimeClaim(claim) || !validInvocationRuntimeAttempt(claim, effect) || (reason != "runtime-request-cleared" && reason != "runtime-ended" && reason != "cancelled") {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockRuntimeInteractionAttempt(ctx, tx, claim, effect); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	value, found, err := getInvocationRuntimeApproval(ctx, tx, claim.IsolationDomainID, id, true)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if !found {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalMissing
	}
	if value.Contract != InvocationRuntimeApprovalContract || value.OperationID != claim.ID || value.InvocationID != claim.ResourceID || value.EffectID != effect.EffectID {
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	value, err = closeRuntimeApproval(ctx, tx, value, reason, claim.ActorID, claim.CorrelationID)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	return value, nil
}

// ExpireInvocationRuntimeApprovals makes expiration durable even after a worker
// loses its lease. Each bounded, isolation-scoped batch commits evidence and
// outbox records with the state transitions. Locked approvals await a later pass.
func (repository *Repository) ExpireInvocationRuntimeApprovals(ctx context.Context, scope, actor, correlation string, limit int) (int, error) {
	if repository == nil || repository.pool == nil || ctx == nil || !invocationPolicyWithdrawalDomainPattern.MatchString(scope) || !validInvocationRuntimeApprovalActor(actor) || !approvalCorrelationPattern.MatchString(correlation) || limit < 1 || limit > 100 {
		return 0, ErrInvocationRuntimeApprovalInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id FROM invocation_runtime_approvals WHERE isolation_domain_id=$1 AND contract='dataground.invocation-runtime-approval/v2' AND state IN ('pending','resolved','delivering') AND expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED`, scope, limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		value, _, err := getInvocationRuntimeApproval(ctx, tx, scope, id, false)
		if err != nil {
			return 0, err
		}
		if _, err := closeRuntimeApproval(ctx, tx, value, "expired", actor, correlation); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func closeRuntimeApproval(ctx context.Context, tx pgx.Tx, value InvocationRuntimeApproval, reason, actor, correlation string) (InvocationRuntimeApproval, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if !value.ExpiresAt.After(now) {
		reason = "expired"
	}
	state := "closed"
	switch value.State {
	case "pending", "resolved":
		if reason == "expired" {
			state = "expired"
		}
	case "delivering":
		state = "delivery_unknown"
	case "closed", "expired", "delivery_unknown", "delivered":
		return value, nil
	default:
		return InvocationRuntimeApproval{}, ErrInvocationRuntimeApprovalConflict
	}
	_, err := tx.Exec(ctx, `UPDATE invocation_runtime_approvals SET state=$3,version=version+1,closed_at=$5,close_reason=$4,updated_at=$5 WHERE isolation_domain_id=$1 AND id=$2`, value.IsolationDomainID, value.ID, state, reason, now)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	value, _, err = getInvocationRuntimeApproval(ctx, tx, value.IsolationDomainID, value.ID, false)
	if err != nil {
		return InvocationRuntimeApproval{}, err
	}
	if err := recordRuntimeApprovalClosure(ctx, tx, value, actor, correlation); err != nil {
		return InvocationRuntimeApproval{}, err
	}
	return value, nil
}

func validApprovalCloseReason(reason string) bool {
	return reason == "expired" || reason == "runtime-request-cleared" || reason == "runtime-ended" || reason == "cancelled"
}

func recordRuntimeApprovalClosure(ctx context.Context, tx pgx.Tx, value InvocationRuntimeApproval, actor, correlation string) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_records (id,isolation_domain_id,actor_id,action,resource_type,resource_id,outcome,correlation_id,operation_id,safe_metadata,occurred_at)
 VALUES ($1,$2,$3,$4,'invocation-approval',$5,'accepted',$6,$7,jsonb_build_object('state',$8::text,'version',$9::bigint,'reason',$10::text),$11)`, identity.New("aud"), value.IsolationDomainID, actor, "invocation-approval."+value.State, value.ID, correlation, value.OperationID, value.State, value.Version, value.CloseReason, value.UpdatedAt)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events (id,isolation_domain_id,aggregate_type,aggregate_id,event_type,payload,correlation_id,available_at,created_at)
 VALUES ($1,$2,'invocation-approval',$3,$4,jsonb_build_object('approvalId',$3::text,'state',$5::text,'version',$6::bigint),$7,$8,$8)`, identity.Derived("out", value.IsolationDomainID+":"+value.ID+":"+strconv.FormatInt(value.Version, 10)), value.IsolationDomainID, value.ID, "invocation-approval."+value.State, value.State, value.Version, correlation, value.UpdatedAt)
	return err
}
