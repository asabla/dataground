package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (repository *Repository) AnswerInvocationRuntimeQuestion(ctx context.Context, answer InvocationRuntimeQuestionAnswer, authorize InvocationRuntimeQuestionAuthorizer) (InvocationRuntimeQuestion, error) {
	if repository == nil || repository.pool == nil || ctx == nil || authorize == nil || !validInvocationRuntimeQuestionAnswer(answer) {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	defer tx.Rollback(ctx)
	value, err := answerInvocationRuntimeQuestion(ctx, tx, answer, authorize)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	return value, nil
}

func answerInvocationRuntimeQuestion(ctx context.Context, tx pgx.Tx, answer InvocationRuntimeQuestionAnswer, authorize InvocationRuntimeQuestionAuthorizer) (InvocationRuntimeQuestion, error) {
	// Match runtime recording and cancellation's invocation-first lock order.
	var exists bool
	err := tx.QueryRow(ctx, `SELECT true FROM invocations WHERE isolation_domain_id=$1 AND id=$2 FOR UPDATE`, answer.IsolationDomainID, answer.InvocationID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionMissing
	}
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	value, found, err := getInvocationRuntimeQuestion(ctx, tx, answer.IsolationDomainID, answer.QuestionID, true)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if !found || value.InvocationID != answer.InvocationID {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionMissing
	}
	if domain.ValidateQuestionAnswers(value.Prompts, answer.Answers) != nil {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionInvalid
	}
	if len(value.Answers) > 0 {
		if value.AnsweredBy != answer.ActorID || answer.ExpectedVersion != 1 || !sameQuestionJSON(value.Answers, answer.Answers) {
			return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionConflict
		}
		replay := value
		replay.AnswerCorrelationID = answer.CorrelationID
		if err := authorize(ctx, replay, InvocationQuestionEntry); err != nil {
			return InvocationRuntimeQuestion{}, err
		}
		return value, nil
	}
	if value.State != "pending" || value.Version != answer.ExpectedVersion {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionConflict
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if !now.Before(value.ExpiresAt) {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionExpired
	}
	if err := activeRuntimeQuestion(ctx, tx, value); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	encoded, _ := json.Marshal(answer.Answers)
	candidate := value
	// Give authorization an owned answer snapshot; the stored bytes are frozen.
	if json.Unmarshal(encoded, &candidate.Answers) != nil {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionInvalid
	}
	candidate.AnsweredBy = answer.ActorID
	candidate.AnswerCorrelationID = answer.CorrelationID
	if err := authorize(ctx, candidate, InvocationQuestionEntry); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	// Recheck the time-sensitive boundary after authorization completes.
	if err := activeRuntimeQuestion(ctx, tx, value); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	result, err := tx.Exec(ctx, `UPDATE invocation_runtime_questions AS question SET state='answered',version=version+1,answers=$3,answered_by=$4,answer_correlation_id=$5,answered_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE isolation_domain_id=$1 AND id=$2 AND state='pending' AND version=$6 AND expires_at>clock_timestamp() AND `+runtimeQuestionLiveAttemptSQL, value.IsolationDomainID, value.ID, encoded, answer.ActorID, answer.CorrelationID, answer.ExpectedVersion)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if result.RowsAffected() != 1 {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionExpired
	}
	value, _, err = getInvocationRuntimeQuestion(ctx, tx, value.IsolationDomainID, value.ID, false)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if err := recordRuntimeQuestionChange(ctx, tx, value, "answered", answer.ActorID, answer.CorrelationID, value.UpdatedAt); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	return value, nil
}

func activeRuntimeQuestion(ctx context.Context, tx pgx.Tx, value InvocationRuntimeQuestion) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT true FROM invocation_execution_operations operation JOIN invocation_runtime_attempts attempt
 ON attempt.isolation_domain_id=operation.isolation_domain_id AND attempt.operation_id=operation.id
 WHERE operation.isolation_domain_id=$1 AND operation.id=$2 AND operation.invocation_id=$3
 AND operation.observed_state='running' AND operation.lease_owner IS NOT NULL AND operation.lease_expires_at>clock_timestamp() AND operation.deadline_at>clock_timestamp()
 AND operation.command IN ('invoke','repair')
 AND attempt.effect_id=$4 AND attempt.status='reserved' AND attempt.lease_owner=operation.lease_owner AND attempt.fencing_token=operation.lease_token
 FOR UPDATE OF operation NOWAIT`, value.IsolationDomainID, value.OperationID, value.InvocationID, value.EffectID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || questionLockUnavailable(err) {
		return ErrInvocationRuntimeQuestionConflict
	}
	if err != nil {
		return err
	}
	if !active {
		return ErrInvocationRuntimeQuestionConflict
	}
	return nil
}

func (repository *Repository) BeginInvocationRuntimeQuestionDelivery(ctx context.Context, claim OperationClaim, effect EffectRecord, id string, authorize InvocationRuntimeQuestionAuthorizer) (InvocationRuntimeQuestion, error) {
	if repository == nil || repository.pool == nil || ctx == nil || authorize == nil || !questionIDPattern.MatchString(id) || !validInvocationRuntimeClaim(claim) || !validInvocationRuntimeAttempt(claim, effect) {
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
	if value.State == "delivering" || value.State == "delivery_unknown" {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionDeliveryAmbiguous
	}
	if value.State != "answered" {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionConflict
	}
	if err := authorize(ctx, value, InvocationQuestionEffect); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if err := lockRuntimeQuestionAttempt(ctx, tx, claim, effect); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	result, err := tx.Exec(ctx, `UPDATE invocation_runtime_questions AS question SET state='delivering',version=version+1,delivery_started_at=clock_timestamp(),updated_at=clock_timestamp()
 WHERE isolation_domain_id=$1 AND id=$2 AND state='answered' AND expires_at>clock_timestamp() AND `+runtimeQuestionLiveAttemptSQL, value.IsolationDomainID, value.ID)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if result.RowsAffected() != 1 {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionExpired
	}
	value, _, err = getInvocationRuntimeQuestion(ctx, tx, value.IsolationDomainID, value.ID, false)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if err := recordRuntimeQuestionChange(ctx, tx, value, "delivery-started", value.AnsweredBy, value.AnswerCorrelationID, value.UpdatedAt); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	return value, nil
}

func (repository *Repository) CompleteInvocationRuntimeQuestionDelivery(ctx context.Context, claim OperationClaim, effect EffectRecord, id string) (InvocationRuntimeQuestion, error) {
	if repository == nil || repository.pool == nil || ctx == nil || !questionIDPattern.MatchString(id) || !validInvocationRuntimeClaim(claim) || !validInvocationRuntimeAttempt(claim, effect) {
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
	if value.State == "delivered" {
		return value, nil
	}
	if value.State != "delivering" {
		return InvocationRuntimeQuestion{}, ErrInvocationRuntimeQuestionConflict
	}
	result, err := tx.Exec(ctx, `UPDATE invocation_runtime_questions AS question SET state='delivered',version=version+1,delivered_at=clock_timestamp(),updated_at=clock_timestamp() WHERE isolation_domain_id=$1 AND id=$2 AND `+runtimeQuestionLiveAttemptSQL, value.IsolationDomainID, value.ID)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if result.RowsAffected() != 1 {
		return InvocationRuntimeQuestion{}, ErrLeaseLost
	}
	value, _, err = getInvocationRuntimeQuestion(ctx, tx, value.IsolationDomainID, value.ID, false)
	if err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if err := recordRuntimeQuestionChange(ctx, tx, value, "delivered", value.AnsweredBy, value.AnswerCorrelationID, value.UpdatedAt); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeQuestion{}, err
	}
	return value, nil
}

// Re-evaluate time at the write itself; operation locks alone cannot keep a
// previously live lease or deadline from expiring during authorization.
const runtimeQuestionLiveAttemptSQL = `EXISTS (
 SELECT 1 FROM invocation_execution_operations operation
 JOIN invocation_runtime_attempts attempt ON attempt.isolation_domain_id=operation.isolation_domain_id AND attempt.operation_id=operation.id
 WHERE operation.isolation_domain_id=question.isolation_domain_id AND operation.id=question.operation_id
 AND operation.invocation_id=question.invocation_id AND operation.command IN ('invoke','repair') AND operation.observed_state='running'
 AND operation.lease_owner IS NOT NULL AND operation.lease_expires_at>clock_timestamp() AND operation.deadline_at>clock_timestamp()
 AND attempt.effect_id=question.effect_id AND attempt.status='reserved'
 AND attempt.lease_owner=operation.lease_owner AND attempt.fencing_token=operation.lease_token)`
