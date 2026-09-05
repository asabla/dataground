package persistence_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestQuestionAPIKeepsAnswersPrivateAndReauthorizesReceipts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	actor := identity.New("usr")
	_, policy := installQuestionAuthorizationFixture(t, ctx, fixture, `permit(principal == DataGround::Actor::"`+actor+`",action == DataGround::Action::"answer",resource);`)
	t.Cleanup(func() {
		if _, err := fixture.pool.Exec(context.Background(), `TRUNCATE api_authorization_decisions`); err != nil {
			t.Error(err)
		}
	})
	value := fixture.request(t, ctx, 20*time.Second)
	const token = "question-api-disposable-development-token-thirty-two-bytes"
	handlerFor := func(principalID string) http.Handler {
		t.Helper()
		authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: principalID, IsolationDomainID: value.IsolationDomainID})
		if err != nil {
			t.Fatal(err)
		}
		authorizer, err := authz.NewDevelopmentCedarAuthorizer(principalID, value.IsolationDomainID)
		if err != nil {
			t.Fatal(err)
		}
		audited, err := authz.NewAuditedAuthorizer(authorizer, fixture.repository)
		if err != nil {
			t.Fatal(err)
		}
		handler, err := api.NewDurableHandler(persistence.NewRepository(fixture.pool), authenticator, audited)
		if err != nil {
			t.Fatal(err)
		}
		return handler
	}
	handler := handlerFor(actor)
	path := "/v1/isolation-domains/" + value.IsolationDomainID + "/invocations/" + value.InvocationID + "/questions/" + value.ID
	request := func(handler http.Handler, method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "question-api-answer-0001")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("question HTTP %s: %d, %s", method, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("question response could be cached")
		}
		if strings.Contains(response.Body.String(), "private answer sentinel") || strings.Contains(response.Body.String(), "effectId") || strings.Contains(response.Body.String(), "operationId") {
			t.Fatal("question response disclosed private answer or runtime bookkeeping")
		}
		return response
	}
	read := request(handler, http.MethodGet, path, "", http.StatusOK)
	var public domain.InvocationQuestion
	if err := json.Unmarshal(read.Body.Bytes(), &public); err != nil {
		t.Fatal(err)
	}
	if public.ID != value.ID || public.State != "pending" || len(public.Questions) != 1 || public.Questions[0].Prompt != "private prompt sentinel" {
		t.Fatal("question public projection lost request")
	}
	request(handler, http.MethodGet, strings.Replace(path, value.InvocationID, identity.New("inv"), 1), "", http.StatusNotFound)
	body := `{"expectedVersion":1,"answers":[{"questionId":"item_1","text":"private answer sentinel"}]}`
	first := request(handler, http.MethodPost, path+"/answers", body, http.StatusOK)
	replay := request(handlerFor(actor), http.MethodPost, path+"/answers", body, http.StatusOK)
	if !bytes.Equal(first.Body.Bytes(), replay.Body.Bytes()) {
		t.Fatal("question receipt replay changed committed response")
	}
	request(handlerFor(identity.New("usr")), http.MethodPost, path+"/answers", body, http.StatusConflict)
	request(handler, http.MethodPost, path+"/answers", strings.Replace(body, "private answer sentinel", "changed answer", 1), http.StatusConflict)
	request(handler, http.MethodGet, path, "", http.StatusOK)
	var firstCorrelation, replayCorrelation string
	rows, err := fixture.pool.Query(ctx, `SELECT correlation_id FROM invocation_question_authorization_decisions WHERE isolation_domain_id=$1 AND question_id=$2 ORDER BY sequence`, value.IsolationDomainID, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	var correlations []string
	for rows.Next() {
		var correlation string
		if err := rows.Scan(&correlation); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		correlations = append(correlations, correlation)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(correlations) != 2 {
		t.Fatalf("question answer/replay decisions: %d", len(correlations))
	}
	firstCorrelation, replayCorrelation = correlations[0], correlations[1]
	if firstCorrelation == replayCorrelation {
		t.Fatal("receipt replay reused original evaluation correlation")
	}
	stored, err := fixture.repository.GetInvocationRuntimeQuestion(ctx, value.IsolationDomainID, value.InvocationID, value.ID)
	if err != nil || stored.AnswerCorrelationID != firstCorrelation || stored.AnsweredBy != actor || stored.Version != 2 {
		t.Fatalf("receipt replay changed immutable answer metadata: %v", err)
	}
	reason := sha256.Sum256([]byte("withdraw question API policy"))
	if err := fixture.repository.WithdrawInvocationAuthorizationPolicy(ctx, persistence.InvocationAuthorizationPolicyWithdrawal{Contract: persistence.InvocationAuthorizationPolicyWithdrawalContract, IsolationDomainID: policy.IsolationDomainID, ServiceID: policy.ServiceID, RevisionID: policy.RevisionID, PolicyDigest: policy.Digest[:], WithdrawnBy: actor, ReasonDigest: reason[:], CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	request(handler, http.MethodPost, path+"/answers", body, http.StatusServiceUnavailable)
	// The generic API decision stream continues to export new question actions;
	// exact invocation question-policy decisions retain their separate contract.
	exported, err := fixture.repository.ExportAuthorizationDecisions(ctx, value.IsolationDomainID, "", 100)
	if err != nil || len(exported.Records) < 6 {
		t.Fatalf("question API audit export: %d, %v", len(exported.Records), err)
	}
	foundRead, foundAnswer := false, false
	for _, record := range exported.Records {
		if record.Action == string(authz.ReadInvocationQuestion) {
			foundRead = true
		}
		if record.Action == string(authz.AnswerInvocationQuestion) {
			foundAnswer = true
		}
	}
	if !foundRead || !foundAnswer {
		t.Fatal("question API authorization missing from export")
	}
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 51); err == nil {
		t.Fatal("retained question API decisions allowed downgrade")
	}
}
