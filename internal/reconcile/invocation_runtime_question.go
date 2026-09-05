package reconcile

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

const invocationQuestionPollInterval = 250 * time.Millisecond

var adapterQuestionIDPattern = regexp.MustCompile(`^question-[1-9][0-9]{0,19}$`)

type InvocationRuntimeQuestionStore interface {
	RecordInvocationRuntimeQuestionRequest(context.Context, persistence.OperationClaim, persistence.EffectRecord, persistence.InvocationRuntimeTarget, persistence.InvocationRuntimeQuestionRequest) (persistence.InvocationRuntimeQuestion, error)
	GetInvocationRuntimeQuestion(context.Context, string, string, string) (persistence.InvocationRuntimeQuestion, error)
	BeginInvocationRuntimeQuestionDelivery(context.Context, persistence.OperationClaim, persistence.EffectRecord, string, persistence.InvocationRuntimeQuestionAuthorizer) (persistence.InvocationRuntimeQuestion, error)
	CompleteInvocationRuntimeQuestionDelivery(context.Context, persistence.OperationClaim, persistence.EffectRecord, string) (persistence.InvocationRuntimeQuestion, error)
	CloseInvocationRuntimeQuestion(context.Context, persistence.OperationClaim, persistence.EffectRecord, string, string) (persistence.InvocationRuntimeQuestion, error)
}

type InvocationQuestionAuthorizer interface {
	AuthorizeInvocationQuestion(context.Context, persistence.InvocationRuntimeQuestion, string) error
}

// This state belongs to one live turn. Only the platform question record is
// durable; an adapter-local routing ID must never be persisted or reconstructed.
type invocationRuntimeQuestions struct {
	driver    *InvocationRuntimeDriver
	target    persistence.InvocationRuntimeTarget
	effect    persistence.EffectRecord
	turn      dgruntime.QuestionTurn
	pending   *persistence.InvocationRuntimeQuestion
	adapterID string
	enabled   bool
	timeout   time.Duration
}

func (questions *invocationRuntimeQuestions) record(ctx context.Context, claim persistence.OperationClaim, event dgruntime.Event, ended bool) (bool, error) {
	if !strings.HasPrefix(event.Type, "interaction.question.") {
		return false, nil
	}
	if !questions.enabled || questions.turn == nil || event.Type != "interaction.question.requested" {
		return true, dgruntime.ErrQuestionMode
	}
	if err := questions.driver.ready(ctx); err != nil {
		return true, err
	}
	if questions.pending != nil {
		pending, err := questions.turn.QuestionPending(ctx, questions.adapterID)
		if err != nil {
			return true, err
		}
		if pending {
			return true, persistence.ErrInvocationRuntimeQuestionConflict
		}
		if err := questions.close(ctx, claim, "runtime-request-cleared"); err != nil {
			return true, err
		}
	}
	id, idOK := event.Payload["questionId"].(string)
	prompts, promptsOK := event.Payload["questions"].([]domain.QuestionPrompt)
	expires, expiresOK := event.Payload["expiresAt"].(string)
	expiresAt, err := time.Parse(time.RFC3339Nano, expires)
	if !idOK || !adapterQuestionIDPattern.MatchString(id) || !promptsOK || domain.ValidateQuestionPrompts(prompts) != nil || !expiresOK || err != nil || len(event.Payload) != 3 || event.Sequence == 0 {
		return true, persistence.ErrInvocationRuntimeQuestionInvalid
	}
	if questions.timeout <= 0 || expiresAt.After(time.Now().Add(questions.timeout)) {
		return true, persistence.ErrInvocationRuntimeQuestionInvalid
	}
	if expiresAt.After(claim.DeadlineAt) {
		expiresAt = claim.DeadlineAt
	}
	// PostgreSQL stores microseconds. Round down before persistence and exact
	// verification so normalization can never extend native authority.
	expiresAt = expiresAt.UTC().Truncate(time.Microsecond)
	value, err := questions.driver.questionStore.RecordInvocationRuntimeQuestionRequest(ctx, claim, questions.effect, questions.target, persistence.InvocationRuntimeQuestionRequest{SourceSequence: event.Sequence, Prompts: prompts, ExpiresAt: expiresAt})
	if err != nil {
		return true, err
	}
	if value.Contract != persistence.InvocationRuntimeQuestionContract || !invocationQuestionIDPattern.MatchString(value.ID) || value.IsolationDomainID != questions.target.IsolationDomainID || value.OperationID != questions.target.OperationID || value.InvocationID != questions.target.InvocationID || value.ServiceID != questions.target.ServiceID || value.RevisionID != questions.target.RevisionID || value.EffectID != questions.effect.EffectID || value.SourceSequence != event.Sequence || value.CorrelationID != questions.target.CorrelationID || value.RequestedBy != questions.target.ActorID || !value.ExpiresAt.Equal(expiresAt) || !reflect.DeepEqual(value.Prompts, prompts) || value.State != "pending" || value.Version != 1 {
		return true, persistence.ErrInvocationRuntimeQuestionConflict
	}
	questions.pending = &value
	questions.adapterID = id
	// The store published the sanitized request atomically. Queued question
	// events observed after turn completion are retained and closed, never answered.
	if ended {
		return true, questions.close(ctx, claim, "runtime-ended")
	}
	return true, nil
}

func (questions *invocationRuntimeQuestions) close(ctx context.Context, claim persistence.OperationClaim, reason string) error {
	if questions.pending == nil {
		return nil
	}
	_, err := questions.driver.questionStore.CloseInvocationRuntimeQuestion(ctx, claim, questions.effect, questions.pending.ID, reason)
	if err == nil {
		questions.pending = nil
		questions.adapterID = ""
	}
	return err
}

func (questions *invocationRuntimeQuestions) poll(ctx context.Context, claim persistence.OperationClaim) error {
	if questions.pending == nil {
		return nil
	}
	active, err := questions.turn.QuestionPending(ctx, questions.adapterID)
	if err != nil {
		return err
	}
	if !active {
		return questions.close(ctx, claim, "runtime-request-cleared")
	}
	original := *questions.pending
	value, err := questions.driver.questionStore.GetInvocationRuntimeQuestion(ctx, original.IsolationDomainID, original.InvocationID, original.ID)
	if err != nil {
		return err
	}
	if !sameRuntimeQuestionRequest(value, original) {
		return persistence.ErrInvocationRuntimeQuestionConflict
	}
	switch value.State {
	case "pending":
		if value.Version != 1 {
			return persistence.ErrInvocationRuntimeQuestionConflict
		}
		return nil
	case "closed", "expired":
		return persistence.ErrInvocationRuntimeQuestionExpired
	case "delivering", "delivery_unknown", "delivered":
		return persistence.ErrInvocationRuntimeQuestionDeliveryAmbiguous
	case "answered":
		if value.Version != 2 || domain.ValidateQuestionAnswers(original.Prompts, value.Answers) != nil {
			return persistence.ErrInvocationRuntimeQuestionConflict
		}
	default:
		return persistence.ErrInvocationRuntimeQuestionConflict
	}
	if err := questions.driver.ready(ctx); err != nil {
		return err
	}
	accepted := value
	value, err = questions.driver.questionStore.BeginInvocationRuntimeQuestionDelivery(ctx, claim, questions.effect, value.ID, func(ctx context.Context, question persistence.InvocationRuntimeQuestion, phase string) error {
		if !sameRuntimeQuestionRequest(question, accepted) || question.Version != 2 || question.State != "answered" || phase != persistence.InvocationQuestionEffect {
			return persistence.ErrInvocationRuntimeQuestionConflict
		}
		if err := questions.driver.ready(ctx); err != nil {
			return err
		}
		return questions.driver.questionAuthorizer.AuthorizeInvocationQuestion(ctx, question, phase)
	})
	if err != nil {
		return err
	}
	if value.State != "delivering" || value.Version != 3 || !sameRuntimeQuestionRequest(value, accepted) || value.AnsweredBy != accepted.AnsweredBy || value.AnswerCorrelationID != accepted.AnswerCorrelationID || !value.AnsweredAt.Equal(accepted.AnsweredAt) || !reflect.DeepEqual(value.Answers, accepted.Answers) {
		return persistence.ErrInvocationRuntimeQuestionConflict
	}
	if err := questions.driver.ready(ctx); err != nil {
		return err
	}
	// Reservation is single-use. Any failure from here is closed as unknown by
	// the turn owner, or eventually by expiry if its original claim was lost.
	deadline := value.ExpiresAt
	if claim.LeaseExpiresAt.Before(deadline) {
		deadline = claim.LeaseExpiresAt
	}
	deliveryCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := questions.turn.AnswerQuestion(deliveryCtx, questions.adapterID, value.Answers); err != nil {
		return errors.Join(persistence.ErrInvocationRuntimeQuestionDeliveryAmbiguous, err)
	}
	if _, err := questions.driver.questionStore.CompleteInvocationRuntimeQuestionDelivery(ctx, claim, questions.effect, value.ID); err != nil {
		return errors.Join(persistence.ErrInvocationRuntimeQuestionDeliveryAmbiguous, err)
	}
	questions.pending = nil
	questions.adapterID = ""
	return nil
}

func sameRuntimeQuestionRequest(left, right persistence.InvocationRuntimeQuestion) bool {
	return left.Contract == right.Contract && left.ID == right.ID && left.IsolationDomainID == right.IsolationDomainID && left.OperationID == right.OperationID && left.InvocationID == right.InvocationID && left.ServiceID == right.ServiceID && left.RevisionID == right.RevisionID && left.EffectID == right.EffectID && left.SourceSequence == right.SourceSequence && left.CorrelationID == right.CorrelationID && left.RequestedBy == right.RequestedBy && left.ExpiresAt.Equal(right.ExpiresAt) && reflect.DeepEqual(left.Prompts, right.Prompts)
}
