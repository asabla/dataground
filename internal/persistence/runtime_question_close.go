package persistence

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// CloseInvocationRuntimeQuestion retires a request cleared by its live runtime.
// Once delivery has begun, closure records uncertainty rather than permission
// to send the answer again.
func (repository *Repository) CloseInvocationRuntimeQuestion(ctx context.Context, claim OperationClaim, effect EffectRecord, id, reason string) (InvocationRuntimeQuestion, error) {
	if repository == nil || repository.pool == nil || ctx == nil || !questionIDPattern.MatchString(id) || !validInvocationRuntimeClaim(claim) || !validInvocationRuntimeAttempt(claim, effect) || (reason != "runtime-request-cleared" && reason != "runtime-ended" && reason != "cancelled") {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockRuntimeQuestionAttempt(ctx, tx, claim, effect); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	value, found, err := getInvocationRuntimeQuestion(ctx, tx, claim.IsolationDomainID, id, true)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if !found {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionMissing
	}
	if value.OperationID != claim.ID || value.InvocationID != claim.ResourceID || value.EffectID != effect.EffectID {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionConflict
	}
	value, err = closeRuntimeQuestion(ctx, tx, value, reason, value.RequestedBy, value.CorrelationID)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	return value, nil
}

// ExpireInvocationRuntimeQuestions makes expiration durable even after a worker
// loses its lease. Each bounded, isolation-scoped batch commits evidence and
// outbox records with the state transitions. Locked questions await a later pass.
func (repository *Repository) ExpireInvocationRuntimeQuestions(ctx context.Context, scope, actor, correlation string, limit int) (int, error) {
	if repository == nil || repository.pool == nil || ctx == nil || !questionScopePattern.MatchString(scope) || !validInvocationRuntimeApprovalActor(actor) || !approvalCorrelationPattern.MatchString(correlation) || limit < 1 || limit > 100 {
		return 0, ErrInvocationRuntimeQuestionInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id FROM invocation_runtime_questions WHERE isolation_domain_id=$1 AND state IN ('pending','answered','delivering') AND expires_at<=clock_timestamp() ORDER BY expires_at,id LIMIT $2 FOR UPDATE SKIP LOCKED`, scope, limit)
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
		value, _, err := getInvocationRuntimeQuestion(ctx, tx, scope, id, false)
		if err != nil {
			return 0, err
		}
		if _, err := closeRuntimeQuestion(ctx, tx, value, "expired", actor, correlation); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func closeRuntimeQuestion(ctx context.Context, tx pgx.Tx, value InvocationRuntimeQuestion, reason, actor, correlation string) (InvocationRuntimeQuestion, error) {
	state := "closed"
	switch value.State {
	case "pending", "answered":
		if reason == "expired" {
			state = "expired"
		}
	case "delivering":
		state = "delivery_unknown"
	case "closed", "expired", "delivery_unknown", "delivered":
		return value, nil
	default:
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionConflict
	}
	_, err := tx.Exec(ctx, `UPDATE invocation_runtime_questions SET state=$3,version=version+1,closed_at=clock_timestamp(),close_reason=$4,updated_at=clock_timestamp() WHERE isolation_domain_id=$1 AND id=$2`, value.IsolationDomainID, value.ID, state, reason)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	value, _, err = getInvocationRuntimeQuestion(ctx, tx, value.IsolationDomainID, value.ID, false)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if err := recordRuntimeQuestionChange(ctx, tx, value, state, actor, correlation, value.UpdatedAt); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	return value, nil
}
