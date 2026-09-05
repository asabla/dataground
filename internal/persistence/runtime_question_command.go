package persistence

import (
	"context"
	"net/http"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (repository *Repository) GetInvocationQuestion(ctx context.Context, scope, invocationID, id string) (domain.InvocationQuestion, error) {
	value, err := repository.GetInvocationRuntimeQuestion(ctx, scope, invocationID, id)
	if err != nil {
		return domain.InvocationQuestion{}, err
	}
	return publicInvocationQuestion(value), nil
}

func (repository *Repository) AnswerInvocationRuntimeQuestionCommand(ctx context.Context, idempotency Idempotency, answer InvocationRuntimeQuestionAnswer, authorize InvocationRuntimeQuestionAuthorizer) (CommandResult, error) {
	if repository == nil || repository.pool == nil || ctx == nil || authorize == nil || !validInvocationRuntimeQuestionAnswer(answer) || idempotency.IsolationDomainID != answer.IsolationDomainID {
		return CommandResult{}, ErrInvocationRuntimeQuestionInvalid
	}
	result, err := repository.execute(ctx, idempotency, func(tx pgx.Tx, _ time.Time) (int, any, error) {
		value, err := answerInvocationRuntimeQuestion(ctx, tx, answer, authorize)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusOK, publicInvocationQuestion(value), nil
	})
	if err != nil {
		return CommandResult{}, err
	}
	if result.Replayed {
		// Receipt replay is still an authorized read. Recheck the exact stored
		// answer and current policy without repeating its mutation or delivery.
		value, err := repository.GetInvocationRuntimeQuestion(ctx, answer.IsolationDomainID, answer.InvocationID, answer.QuestionID)
		if err != nil {
			return CommandResult{}, err
		}
		if answer.ExpectedVersion != 1 || value.AnsweredBy != answer.ActorID || !sameQuestionJSON(value.Answers, answer.Answers) {
			return CommandResult{}, ErrInvocationRuntimeQuestionConflict
		}
		// Attribute the new evaluation to this request while preserving the
		// original answer correlation used for native delivery.
		value.AnswerCorrelationID = answer.CorrelationID
		if err := authorize(ctx, value, InvocationQuestionEntry); err != nil {
			return CommandResult{}, err
		}
	}
	return result, nil
}

func publicInvocationQuestion(value InvocationRuntimeQuestion) domain.InvocationQuestion {
	result := domain.InvocationQuestion{
		SchemaVersion: domain.InvocationQuestionSchemaV1, ID: value.ID, IsolationDomainID: value.IsolationDomainID, InvocationID: value.InvocationID, ServiceID: value.ServiceID, RevisionID: value.RevisionID,
		Questions: value.Prompts, State: value.State, Version: value.Version, ExpiresAt: value.ExpiresAt, AnsweredBy: value.AnsweredBy, CloseReason: value.CloseReason, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
	if !value.AnsweredAt.IsZero() {
		answered := value.AnsweredAt
		result.AnsweredAt = &answered
	}
	if !value.ClosedAt.IsZero() {
		closed := value.ClosedAt
		result.ClosedAt = &closed
	}
	return result
}

func validInvocationRuntimeQuestionAnswer(answer InvocationRuntimeQuestionAnswer) bool {
	return questionScopePattern.MatchString(answer.IsolationDomainID) && approvalInvocationPattern.MatchString(answer.InvocationID) && questionIDPattern.MatchString(answer.QuestionID) && answer.ExpectedVersion >= 1 && validInvocationRuntimeApprovalActor(answer.ActorID) && approvalCorrelationPattern.MatchString(answer.CorrelationID)
}
