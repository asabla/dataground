package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

type questionStoreStub struct {
	scope, invocation, id string
	answer                persistence.InvocationRuntimeQuestionAnswer
	idempotency           persistence.Idempotency
	value                 domain.InvocationQuestion
	result                persistence.CommandResult
	err                   error
	called                bool
}

func (store *questionStoreStub) GetInvocationQuestion(_ context.Context, scope, invocation, id string) (domain.InvocationQuestion, error) {
	store.scope, store.invocation, store.id = scope, invocation, id
	return store.value, store.err
}
func (store *questionStoreStub) AnswerInvocationRuntimeQuestionCommand(_ context.Context, idempotency persistence.Idempotency, answer persistence.InvocationRuntimeQuestionAnswer, authorize persistence.InvocationRuntimeQuestionAuthorizer) (persistence.CommandResult, error) {
	store.called = true
	store.idempotency = idempotency
	store.answer = answer
	if authorize == nil {
		return persistence.CommandResult{}, errors.New("missing authorizer")
	}
	return store.result, store.err
}

func questionAnswerHTTPRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	const scope = "iso_00000000000000000001"
	const actor = "usr_00000000000000000001"
	principal, err := authn.NewPrincipal(authn.PrincipalInput{ID: actor, Kind: authn.PrincipalHuman, Issuer: "test", Subject: actor, Audience: authn.APIAudience, IsolationDomains: []string{scope}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/isolation-domains/"+scope+"/invocations/inv_00000000000000000001/questions/qst_00000000000000000001/answers", strings.NewReader(body))
	request.SetPathValue("isolationDomainId", scope)
	request.SetPathValue("invocationId", "inv_00000000000000000001")
	request.SetPathValue("questionId", "qst_00000000000000000001")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "question-answer-0001")
	ctx := context.WithValue(request.Context(), authenticatedPrincipalKey{}, principal)
	ctx = context.WithValue(ctx, authenticatedCorrelationKey{}, "cor_00000000000000000001")
	return request.WithContext(ctx)
}
func allowQuestionAPI(context.Context, persistence.InvocationRuntimeQuestion, string) error {
	return nil
}

const validQuestionAnswerBody = `{"expectedVersion":1,"answers":[{"questionId":"item_1","text":"target"}]}`

func TestQuestionAPIReadBindsPathAndDisablesCaching(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	store := &questionStoreStub{value: domain.InvocationQuestion{SchemaVersion: domain.InvocationQuestionSchemaV1, ID: "qst_00000000000000000001", State: "pending", Version: 1, ExpiresAt: now.Add(time.Minute)}}
	server := &DurableServer{questions: store}
	request := questionAnswerHTTPRequest(t, "")
	response := httptest.NewRecorder()
	server.getInvocationQuestion(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("question read: %d", response.Code)
	}
	if store.scope != request.PathValue("isolationDomainId") || store.invocation != request.PathValue("invocationId") || store.id != request.PathValue("questionId") {
		t.Fatal("question read lost exact path")
	}
}

func TestQuestionAPIAnswerBindsPrincipalPathAndIdempotency(t *testing.T) {
	t.Parallel()
	store := &questionStoreStub{result: persistence.CommandResult{Status: http.StatusOK, Body: []byte(`{"state":"answered"}`)}}
	server := &DurableServer{questions: store, questionAuthorizer: allowQuestionAPI}
	request := questionAnswerHTTPRequest(t, validQuestionAnswerBody)
	response := httptest.NewRecorder()
	server.answerInvocationQuestion(response, request)
	if response.Code != http.StatusOK || !store.called || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("answer response: %d", response.Code)
	}
	answer := store.answer
	if answer.IsolationDomainID != request.PathValue("isolationDomainId") || answer.InvocationID != request.PathValue("invocationId") || answer.QuestionID != request.PathValue("questionId") || answer.ActorID != "usr_00000000000000000001" || answer.CorrelationID != "cor_00000000000000000001" || answer.ExpectedVersion != 1 || len(answer.Answers) != 1 || *answer.Answers[0].Text != "target" {
		t.Fatal("question answer binding lost")
	}
	if store.idempotency.Method != http.MethodPost || store.idempotency.Path != request.URL.EscapedPath() || store.idempotency.Key != request.Header.Get("Idempotency-Key") || store.idempotency.IsolationDomainID != answer.IsolationDomainID {
		t.Fatal("question idempotency binding lost")
	}
}

func TestQuestionAPIRejectsAmbiguousOrUnboundedBodiesBeforeStore(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"unknown field":     `{"expectedVersion":1,"answers":[{"questionId":"item_1","nativeId":"hidden","text":"x"}]}`,
		"wrong case":        `{"expectedVersion":1,"answers":[{"questionId":"item_1","Text":"x"}]}`,
		"duplicate field":   `{"expectedVersion":1,"answers":[{"questionId":"item_1","text":"x","text":"y"}]}`,
		"duplicate version": `{"expectedVersion":2,"expectedVersion":1,"answers":[{"questionId":"item_1","text":"x"}]}`,
		"missing answers":   `{"expectedVersion":1,"answers":[]}`,
		"wrong version":     `{"expectedVersion":2,"answers":[{"questionId":"item_1","text":"x"}]}`,
		"oversized":         strings.Repeat(" ", 32768) + validQuestionAnswerBody,
		"invalid utf8":      strings.Replace(validQuestionAnswerBody, "target", string([]byte{0xff}), 1),
	} {
		t.Run(name, func(t *testing.T) {
			store := &questionStoreStub{}
			server := &DurableServer{questions: store, questionAuthorizer: allowQuestionAPI}
			response := httptest.NewRecorder()
			server.answerInvocationQuestion(response, questionAnswerHTTPRequest(t, body))
			if response.Code != http.StatusBadRequest || store.called {
				t.Fatalf("invalid question answer: %d, called %t", response.Code, store.called)
			}
		})
	}
}

func TestQuestionAPIMapsStableErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{persistence.ErrInvocationRuntimeQuestionMissing, 404, "RESOURCE_NOT_FOUND"},
		{persistence.ErrInvocationRuntimeQuestionConflict, 409, "INVOCATION_QUESTION_CONFLICT"},
		{persistence.ErrInvocationRuntimeQuestionExpired, 410, "INVOCATION_QUESTION_EXPIRED"},
		{persistence.ErrInvocationRuntimeQuestionInvalid, 400, "INVALID_INVOCATION_QUESTION"},
		{reconcile.ErrInvocationQuestionDenied, 403, "INVOCATION_QUESTION_FORBIDDEN"},
		{errors.New("upstream credential detail"), 503, "INVOCATION_QUESTION_UNAVAILABLE"},
	} {
		t.Run(test.code, func(t *testing.T) {
			store := &questionStoreStub{err: test.err}
			server := &DurableServer{questions: store, questionAuthorizer: allowQuestionAPI}
			response := httptest.NewRecorder()
			server.answerInvocationQuestion(response, questionAnswerHTTPRequest(t, validQuestionAnswerBody))
			var problem ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || problem.Error.Code != test.code || problem.Error.CorrelationID != "cor_00000000000000000001" || strings.Contains(response.Body.String(), "credential detail") {
				t.Fatalf("question error: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
