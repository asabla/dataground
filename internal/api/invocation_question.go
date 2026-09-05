package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

type durableInvocationQuestionStore interface {
	GetInvocationQuestion(context.Context, string, string, string) (domain.InvocationQuestion, error)
	AnswerInvocationRuntimeQuestionCommand(context.Context, persistence.Idempotency, persistence.InvocationRuntimeQuestionAnswer, persistence.InvocationRuntimeQuestionAuthorizer) (persistence.CommandResult, error)
}

type answerInvocationQuestionRequest struct {
	ExpectedVersion int64                   `json:"expectedVersion"`
	Answers         []domain.QuestionAnswer `json:"answers"`
}

func (server *DurableServer) getInvocationQuestion(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	domainID, apiError := isolationDomain(request)
	if apiError != nil {
		writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: *apiError})
		return
	}
	if server.questions == nil {
		server.writeCommandError(response, invocationQuestionCommandError(errors.New("question store unavailable")), authenticatedCorrelationID(request))
		return
	}
	value, err := server.questions.GetInvocationQuestion(request.Context(), domainID, request.PathValue("invocationId"), request.PathValue("questionId"))
	if err != nil {
		server.writeCommandError(response, invocationQuestionCommandError(err), authenticatedCorrelationID(request))
		return
	}
	writeJSON(response, http.StatusOK, value)
}

func (server *DurableServer) answerInvocationQuestion(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	server.mutate(response, request, func(domainID, actorID, correlationID string, body []byte) (persistence.CommandResult, error) {
		input, apiError := decodeBody[answerInvocationQuestionRequest](body)
		if apiError != nil {
			return encodedError(http.StatusBadRequest, *apiError)
		}
		if !validQuestionAnswerRequest(input, body) {
			return encodedResult(invalidField("answers", "Supply one to three answers and expectedVersion 1 within the bounded request size."))
		}
		if server.questions == nil || server.questionAuthorizer == nil {
			return persistence.CommandResult{}, invocationQuestionCommandError(errors.New("question store unavailable"))
		}
		result, err := server.questions.AnswerInvocationRuntimeQuestionCommand(request.Context(), commandIdempotency(request, domainID, actorID, body), persistence.InvocationRuntimeQuestionAnswer{
			IsolationDomainID: domainID, InvocationID: request.PathValue("invocationId"), QuestionID: request.PathValue("questionId"), ExpectedVersion: input.ExpectedVersion, Answers: input.Answers, ActorID: actorID, CorrelationID: correlationID,
		}, server.questionAuthorizer)
		if err != nil {
			return persistence.CommandResult{}, invocationQuestionCommandError(err)
		}
		return result, nil
	})
}

func validQuestionAnswerRequest(input answerInvocationQuestionRequest, body []byte) bool {
	return len(body) <= 32768 && utf8.Valid(body) && validQuestionAnswerJSON(body) && input.ExpectedVersion == 1 && len(input.Answers) >= 1 && len(input.Answers) <= 3
}

func invocationQuestionCommandError(err error) error {
	var problem *persistence.DomainError
	if errors.As(err, &problem) {
		return problem
	}
	switch {
	case errors.Is(err, persistence.ErrInvocationRuntimeQuestionMissing):
		return &persistence.DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Invocation question was not found."}
	case errors.Is(err, persistence.ErrInvocationRuntimeQuestionExpired):
		return &persistence.DomainError{Code: "INVOCATION_QUESTION_EXPIRED", Message: "Invocation question has expired."}
	case errors.Is(err, persistence.ErrInvocationRuntimeQuestionConflict), errors.Is(err, persistence.ErrInvocationRuntimeQuestionDeliveryAmbiguous), errors.Is(err, persistence.ErrLeaseLost):
		return &persistence.DomainError{Code: "INVOCATION_QUESTION_CONFLICT", Message: "Invocation question cannot be answered in its current state."}
	case errors.Is(err, persistence.ErrInvocationRuntimeQuestionInvalid), errors.Is(err, reconcile.ErrInvocationAuthorizationInvalid):
		return &persistence.DomainError{Code: "INVALID_INVOCATION_QUESTION", Message: "Invocation question answer is invalid."}
	case errors.Is(err, reconcile.ErrInvocationQuestionDenied):
		return &persistence.DomainError{Code: "INVOCATION_QUESTION_FORBIDDEN", Message: "The invocation policy denied this question answer."}
	default:
		return &persistence.DomainError{Code: "INVOCATION_QUESTION_UNAVAILABLE", Message: "Invocation question is temporarily unavailable.", Retryable: true}
	}
}

func (server *Server) getInvocationQuestion(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	status, body := notFound("Invocation question was not found.")
	writeJSON(response, status, body)
}
func (server *Server) answerInvocationQuestion(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	server.mutate(response, request, func(_ string, body []byte) (int, any) {
		input, apiError := decodeBody[answerInvocationQuestionRequest](body)
		if apiError != nil {
			return http.StatusBadRequest, ErrorEnvelope{Error: *apiError}
		}
		if !validQuestionAnswerRequest(input, body) {
			return invalidField("answers", "Supply one to three answers and expectedVersion 1 within the bounded request size.")
		}
		return notFound("Invocation question was not found.")
	})
}

// Reject duplicate and noncanonical field spellings before accepting an answer.
func validQuestionAnswerJSON(raw []byte) bool {
	allowed := map[string]bool{"expectedVersion": true, "answers": true, "questionId": true, "optionIds": true, "text": true}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 4 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return true
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || !allowed[key] || seen[key] {
					return false
				}
				seen[key] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for decoder.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := decoder.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
