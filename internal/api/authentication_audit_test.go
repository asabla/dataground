package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
)

func TestProtectedRoutesShareAuthenticationAndAuthorizationCorrelation(t *testing.T) {
	t.Parallel()

	authenticationRecorder := &recordingAuthenticationAttemptRecorder{}
	authenticator := auditedDevelopmentAuthenticator(t, testDomain, authenticationRecorder)
	delegate, err := authz.NewDevelopmentCedarAuthorizer(testActor, testDomain)
	if err != nil {
		t.Fatalf("create development authorizer: %v", err)
	}
	authorizationRecorder := &recordingAPIDecisionRecorder{}
	authorizer, err := authz.NewAuditedAuthorizer(delegate, authorizationRecorder)
	if err != nil {
		t.Fatalf("create audited authorizer: %v", err)
	}
	handler, err := api.NewHandler(authenticator, authorizer)
	if err != nil {
		t.Fatalf("create protected handler: %v", err)
	}

	response := perform(
		t,
		handler,
		http.MethodPost,
		serviceCollectionPath(testDomain),
		"authentication-audit-success",
		map[string]any{"name": "Audited service"},
		nil,
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusCreated, response.Body.String())
	}
	if len(authenticationRecorder.records) != 1 || len(authorizationRecorder.records) != 1 {
		t.Fatalf(
			"authentication records = %d, authorization records = %d",
			len(authenticationRecorder.records),
			len(authorizationRecorder.records),
		)
	}
	authentication := authenticationRecorder.records[0]
	authorization := authorizationRecorder.records[0]
	if authentication.Outcome != authn.AuthenticationOutcomeAuthenticated ||
		authentication.PrincipalID != testActor ||
		authentication.IsolationDomainID != testDomain ||
		authentication.CorrelationID == "" ||
		authentication.CorrelationID != authorization.CorrelationID {
		t.Fatalf("authentication = %#v; authorization = %#v", authentication, authorization)
	}
}

func TestProtectedRoutesCorrelateAndMinimizeRejectedAuthentication(t *testing.T) {
	t.Parallel()

	recorder := &recordingAuthenticationAttemptRecorder{}
	authenticator := auditedDevelopmentAuthenticator(t, testDomain, recorder)
	authorizer, err := authz.NewDevelopmentCedarAuthorizer(testActor, testDomain)
	if err != nil {
		t.Fatalf("create development authorizer: %v", err)
	}
	handler, err := api.NewHandler(authenticator, authorizer)
	if err != nil {
		t.Fatalf("create protected handler: %v", err)
	}
	response := perform(
		t,
		handler,
		http.MethodPost,
		serviceCollectionPath(testDomain),
		"authentication-audit-rejected",
		map[string]any{"name": "Rejected service"},
		map[string]string{"Authorization": "Bearer different-development-token-with-thirty-two-bytes"},
	)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.Outcome != authn.AuthenticationOutcomeRejected ||
		record.PrincipalID != "" ||
		record.PrincipalKind != "" ||
		record.CorrelationID == "" {
		t.Fatalf("record = %#v", record)
	}
	var problem api.ErrorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if problem.Error.Code != "UNAUTHENTICATED" || problem.Error.CorrelationID != record.CorrelationID {
		t.Fatalf("problem = %#v; record = %#v", problem.Error, record)
	}
}

func TestProtectedRoutesDoNotDiscloseForeignPrincipalInAuthenticationAudit(t *testing.T) {
	t.Parallel()

	recorder := &recordingAuthenticationAttemptRecorder{}
	authenticator := auditedDevelopmentAuthenticator(t, testDomain, recorder)
	authorizer, err := authz.NewDevelopmentCedarAuthorizer(testActor, testDomain)
	if err != nil {
		t.Fatalf("create development authorizer: %v", err)
	}
	handler, err := api.NewHandler(authenticator, authorizer)
	if err != nil {
		t.Fatalf("create protected handler: %v", err)
	}
	otherDomain := "iso_00000000000000000002"
	response := perform(
		t,
		handler,
		http.MethodPost,
		serviceCollectionPath(otherDomain),
		"authentication-audit-foreign",
		map[string]any{"name": "Foreign service"},
		nil,
	)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if record.IsolationDomainID != otherDomain ||
		record.Outcome != authn.AuthenticationOutcomeScopeDenied ||
		record.PrincipalID != "" ||
		record.PrincipalKind != "" {
		t.Fatalf("record = %#v", record)
	}
}

func auditedDevelopmentAuthenticator(
	t *testing.T,
	domainID string,
	recorder authn.AuthenticationAttemptRecorder,
) *authn.AuditedAuthenticator {
	t.Helper()
	delegate, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{
		BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: domainID,
	})
	if err != nil {
		t.Fatalf("create development authenticator: %v", err)
	}
	authenticator, err := authn.NewAuditedAuthenticator(delegate, recorder)
	if err != nil {
		t.Fatalf("create audited authenticator: %v", err)
	}
	return authenticator
}

type recordingAuthenticationAttemptRecorder struct {
	records []authn.AuthenticationAttemptRecord
}

func (recorder *recordingAuthenticationAttemptRecorder) RecordAuthenticationAttempt(
	_ context.Context,
	record authn.AuthenticationAttemptRecord,
) error {
	recorder.records = append(recorder.records, record)
	return nil
}

var _ authn.AuthenticationAttemptRecorder = (*recordingAuthenticationAttemptRecorder)(nil)
