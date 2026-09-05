package reconcile

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

var (
	ErrInvocationQuestionDenied = errors.New("invocation question answer denied")
	invocationQuestionIDPattern = regexp.MustCompile(`^qst_[0-9a-z]{20,32}$`)
)

// Policy sees bounded structural facts and the immutable question identity,
// never prompt text, labels, free-text answers, or native runtime handles.
type InvocationQuestionAuthorizationContext struct {
	ID                  string
	Version             int64
	Phase               string
	QuestionCount       int
	FreeTextCount       int
	SelectedOptionCount int
}

func (value InvocationQuestionAuthorizationContext) Valid() bool {
	return invocationQuestionIDPattern.MatchString(value.ID) && value.Version > 0 &&
		(value.Phase == persistence.InvocationQuestionEntry || value.Phase == persistence.InvocationQuestionEffect) &&
		value.QuestionCount >= 1 && value.QuestionCount <= 3 && value.FreeTextCount >= 0 && value.FreeTextCount <= value.QuestionCount &&
		value.SelectedOptionCount >= value.QuestionCount-value.FreeTextCount && value.SelectedOptionCount <= 4*(value.QuestionCount-value.FreeTextCount)
}

func (authorizer *InvocationAuthorizer) AuthorizeInvocationQuestion(ctx context.Context, question persistence.InvocationRuntimeQuestion, phase string) error {
	if authorizer == nil || ctx == nil || domain.ValidateQuestionPrompts(question.Prompts) != nil || domain.ValidateQuestionAnswers(question.Prompts, question.Answers) != nil {
		return ErrInvocationAuthorizationInvalid
	}
	request := invocationAuthorizationRequest(InvocationAuthorizationAnswer, question.IsolationDomainID, question.OperationID, question.InvocationID, question.ServiceID, question.RevisionID, question.AnsweredBy, question.AnswerCorrelationID)
	request.Question = &InvocationQuestionAuthorizationContext{ID: question.ID, Version: question.Version, Phase: phase, QuestionCount: len(question.Prompts)}
	for _, answer := range question.Answers {
		if answer.Text != nil {
			request.Question.FreeTextCount++
		}
		request.Question.SelectedOptionCount += len(answer.OptionIDs)
	}
	return authorizer.authorize(ctx, request, ErrInvocationQuestionDenied)
}

func validInvocationRuntimeQuestionMode(request dgruntime.StartRequest) bool {
	switch request.QuestionMode {
	case "", dgruntime.QuestionDisabled:
		return request.QuestionTimeout == 0
	case dgruntime.QuestionInteractive:
		// Millisecond precision is the exact policy contract; never round a wider
		// native timeout into a narrower authorization value.
		return request.QuestionTimeout > 0 && request.QuestionTimeout <= 15*time.Minute && request.QuestionTimeout%time.Millisecond == 0
	default:
		return false
	}
}
