package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
)

func TestProtectedRoutesWithholdAllowedRequestsWhenAuditFails(t *testing.T) {
	t.Parallel()

	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{
		BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain,
	})
	if err != nil {
		t.Fatalf("create development authenticator: %v", err)
	}
	delegate, err := authz.NewDevelopmentCedarAuthorizer(testActor, testDomain)
	if err != nil {
		t.Fatalf("create development authorizer: %v", err)
	}
	recorder := &failingAPIDecisionRecorder{}
	authorizer, err := authz.NewAuditedAuthorizer(delegate, recorder)
	if err != nil {
		t.Fatalf("create audited authorizer: %v", err)
	}
	handler, err := api.NewHandler(authenticator, authorizer)
	if err != nil {
		t.Fatalf("create protected handler: %v", err)
	}
	reader := &countingReader{}
	request := httptest.NewRequest(http.MethodPost, serviceCollectionPath(testDomain), reader)
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "audit-failure-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("authorization audit failure status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if reader.reads != 0 {
		t.Fatalf("request body was read before authorization audit completed: %d reads", reader.reads)
	}
	if len(recorder.records) != 1 {
		t.Fatalf("attempted audit records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.Outcome != authz.OutcomeAllowed ||
		record.Action != authz.CreateAgentService ||
		record.ResourceType != authz.IsolationDomain ||
		record.ResourceID != testDomain ||
		record.PrincipalID != testActor ||
		record.CorrelationID == "" {
		t.Fatalf("attempted audit record = %#v", record)
	}
	var problem api.ErrorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatalf("decode audit failure response: %v", err)
	}
	if problem.Error.Code != "AUTHORIZATION_UNAVAILABLE" ||
		problem.Error.CorrelationID != record.CorrelationID ||
		!problem.Error.Retryable {
		t.Fatalf("audit failure response = %#v", problem.Error)
	}
}

type failingAPIDecisionRecorder struct {
	records []authz.DecisionRecord
}

func (recorder *failingAPIDecisionRecorder) RecordAuthorizationDecision(
	_ context.Context,
	record authz.DecisionRecord,
) error {
	recorder.records = append(recorder.records, record)
	return errors.New("injected audit persistence failure")
}

var _ authz.DecisionRecorder = (*failingAPIDecisionRecorder)(nil)
