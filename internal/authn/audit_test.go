package authn_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/authn"
)

func TestAuditedAuthenticatorRecordsAttributedInDomainSuccess(t *testing.T) {
	t.Parallel()

	principal := authenticationAuditPrincipal(t, testDomain)
	recorder := &recordingAuthenticationAttemptRecorder{}
	authenticator, err := authn.NewAuditedAuthenticator(
		authenticationAuditDelegate{principal: principal},
		recorder,
	)
	if err != nil {
		t.Fatalf("create audited authenticator: %v", err)
	}
	correlationID := "cor_00000000000000000001"
	ctx := authenticationAuditContext(t, testDomain, correlationID)

	got, err := authenticator.Authenticate(ctx, []byte(testToken))
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if got.ID() != testActor {
		t.Fatalf("principal ID = %q, want %q", got.ID(), testActor)
	}
	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	record := recorder.records[0]
	if !record.Valid() ||
		record.IsolationDomainID != testDomain ||
		record.PrincipalID != testActor ||
		record.PrincipalKind != authn.PrincipalHuman ||
		record.Method != authn.AuthenticationMethodDevelopmentBearer ||
		record.Outcome != authn.AuthenticationOutcomeAuthenticated ||
		record.CorrelationID != correlationID {
		t.Fatalf("record = %#v", record)
	}
}

func TestAuditedAuthenticatorMinimizesNonSuccessRecords(t *testing.T) {
	t.Parallel()

	otherDomain := "iso_00000000000000000002"
	tests := map[string]struct {
		delegate authn.Authenticator
		domainID string
		outcome  authn.AuthenticationOutcome
		wantErr  error
	}{
		"invalid credential": {
			delegate: authenticationAuditDelegate{err: authn.ErrInvalidCredential},
			domainID: testDomain,
			outcome:  authn.AuthenticationOutcomeRejected,
			wantErr:  authn.ErrInvalidCredential,
		},
		"unavailable": {
			delegate: authenticationAuditDelegate{err: errors.New("private dependency detail")},
			domainID: testDomain,
			outcome:  authn.AuthenticationOutcomeUnavailable,
			wantErr:  authn.ErrUnavailable,
		},
		"invalid principal": {
			delegate: authenticationAuditDelegate{},
			domainID: testDomain,
			outcome:  authn.AuthenticationOutcomeUnavailable,
			wantErr:  authn.ErrUnavailable,
		},
		"foreign scope": {
			delegate: authenticationAuditDelegate{principal: authenticationAuditPrincipal(t, testDomain)},
			domainID: otherDomain,
			outcome:  authn.AuthenticationOutcomeScopeDenied,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			recorder := &recordingAuthenticationAttemptRecorder{}
			authenticator, err := authn.NewAuditedAuthenticator(test.delegate, recorder)
			if err != nil {
				t.Fatalf("create audited authenticator: %v", err)
			}
			principal, err := authenticator.Authenticate(
				authenticationAuditContext(t, test.domainID, "cor_00000000000000000002"),
				[]byte(testToken),
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if test.outcome == authn.AuthenticationOutcomeScopeDenied && !principal.Valid() {
				t.Fatal("scope-denied authentication did not return its valid principal")
			}
			if len(recorder.records) != 1 {
				t.Fatalf("records = %d, want 1", len(recorder.records))
			}
			record := recorder.records[0]
			if record.Outcome != test.outcome || record.PrincipalID != "" || record.PrincipalKind != "" {
				t.Fatalf("minimized record = %#v", record)
			}
		})
	}
}

func TestAuditedAuthenticatorWithholdsResultWhenAuditFails(t *testing.T) {
	t.Parallel()

	authenticator, err := authn.NewAuditedAuthenticator(
		authenticationAuditDelegate{principal: authenticationAuditPrincipal(t, testDomain)},
		&recordingAuthenticationAttemptRecorder{err: errors.New("private persistence detail")},
	)
	if err != nil {
		t.Fatalf("create audited authenticator: %v", err)
	}
	principal, err := authenticator.Authenticate(
		authenticationAuditContext(t, testDomain, "cor_00000000000000000003"),
		[]byte(testToken),
	)
	if !errors.Is(err, authn.ErrUnavailable) || principal.Valid() {
		t.Fatalf("result = %#v, %v; want unavailable with no principal", principal, err)
	}
}

func TestAuditedAuthenticatorRecordsForcedRejectionWithoutCallingDelegate(t *testing.T) {
	t.Parallel()

	delegate := &countingAuthenticationAuditDelegate{
		principal: authenticationAuditPrincipal(t, testDomain),
	}
	recorder := &recordingAuthenticationAttemptRecorder{}
	authenticator, err := authn.NewAuditedAuthenticator(delegate, recorder)
	if err != nil {
		t.Fatalf("create audited authenticator: %v", err)
	}
	ctx := authenticationAuditContext(t, testDomain, "cor_00000000000000000005")
	ctx, err = authn.WithRejectedAuthenticationAttempt(ctx)
	if err != nil {
		t.Fatalf("mark rejected authentication: %v", err)
	}

	principal, err := authenticator.Authenticate(ctx, nil)
	if !errors.Is(err, authn.ErrInvalidCredential) || principal.Valid() {
		t.Fatalf("result = %#v, %v; want rejected with no principal", principal, err)
	}
	if delegate.calls != 0 {
		t.Fatalf("delegate calls = %d, want 0", delegate.calls)
	}
	if len(recorder.records) != 1 ||
		recorder.records[0].Outcome != authn.AuthenticationOutcomeRejected ||
		recorder.records[0].PrincipalID != "" ||
		recorder.records[0].PrincipalKind != "" {
		t.Fatalf("records = %#v", recorder.records)
	}
}

func TestAuditedAuthenticatorRequiresScopeAndPreservesCancellation(t *testing.T) {
	t.Parallel()

	recorder := &recordingAuthenticationAttemptRecorder{}
	authenticator, err := authn.NewAuditedAuthenticator(
		authenticationAuditDelegate{principal: authenticationAuditPrincipal(t, testDomain)},
		recorder,
	)
	if err != nil {
		t.Fatalf("create audited authenticator: %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), []byte(testToken)); !errors.Is(err, authn.ErrUnavailable) {
		t.Fatalf("missing-scope error = %v, want unavailable", err)
	}
	ctx, cancel := context.WithCancel(authenticationAuditContext(
		t,
		testDomain,
		"cor_00000000000000000004",
	))
	cancel()
	if _, err := authenticator.Authenticate(ctx, []byte(testToken)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v, want cancellation", err)
	}
	if len(recorder.records) != 0 {
		t.Fatalf("incomplete attempts were recorded: %d", len(recorder.records))
	}
}

func TestAuditedAuthenticatorRejectsIncompleteAssemblyAndSerialization(t *testing.T) {
	t.Parallel()

	var delegate *authenticationAuditDelegate
	if _, err := authn.NewAuditedAuthenticator(delegate, &recordingAuthenticationAttemptRecorder{}); err == nil {
		t.Fatal("typed-nil authenticator was accepted")
	}
	var recorder *recordingAuthenticationAttemptRecorder
	if _, err := authn.NewAuditedAuthenticator(authenticationAuditDelegate{}, recorder); err == nil {
		t.Fatal("typed-nil recorder was accepted")
	}
	authenticator, err := authn.NewAuditedAuthenticator(
		authenticationAuditDelegate{principal: authenticationAuditPrincipal(t, testDomain)},
		&recordingAuthenticationAttemptRecorder{},
	)
	if err != nil {
		t.Fatalf("create audited authenticator: %v", err)
	}
	if _, err := json.Marshal(authenticator); err == nil {
		t.Fatal("audited authenticator serialized")
	}
}

func authenticationAuditContext(t *testing.T, domainID, correlationID string) context.Context {
	t.Helper()
	ctx, err := authn.WithAuthenticationAttemptScope(context.Background(), authn.AuthenticationAttemptScope{
		IsolationDomainID: domainID,
		CorrelationID:     correlationID,
	})
	if err != nil {
		t.Fatalf("create authentication attempt scope: %v", err)
	}
	return ctx
}

func authenticationAuditPrincipal(t *testing.T, domainID string) authn.Principal {
	t.Helper()
	principal, err := authn.NewPrincipal(authn.PrincipalInput{
		ID: testActor, Kind: authn.PrincipalHuman, Issuer: "test", Subject: testActor,
		Audience: authn.APIAudience, IsolationDomains: []string{domainID},
	})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	return principal
}

type countingAuthenticationAuditDelegate struct {
	principal authn.Principal
	calls     int
}

func (delegate *countingAuthenticationAuditDelegate) Authenticate(
	_ context.Context,
	_ []byte,
) (authn.Principal, error) {
	delegate.calls++
	return delegate.principal, nil
}

func (*countingAuthenticationAuditDelegate) AuthenticationMethod() authn.AuthenticationMethod {
	return authn.AuthenticationMethodDevelopmentBearer
}

type authenticationAuditDelegate struct {
	principal authn.Principal
	err       error
}

func (delegate authenticationAuditDelegate) Authenticate(
	_ context.Context,
	_ []byte,
) (authn.Principal, error) {
	return delegate.principal, delegate.err
}

func (authenticationAuditDelegate) AuthenticationMethod() authn.AuthenticationMethod {
	return authn.AuthenticationMethodDevelopmentBearer
}

type recordingAuthenticationAttemptRecorder struct {
	records []authn.AuthenticationAttemptRecord
	err     error
}

func (recorder *recordingAuthenticationAttemptRecorder) RecordAuthenticationAttempt(
	_ context.Context,
	record authn.AuthenticationAttemptRecord,
) error {
	recorder.records = append(recorder.records, record)
	return recorder.err
}

var _ authn.Authenticator = (*countingAuthenticationAuditDelegate)(nil)
var _ authn.Authenticator = authenticationAuditDelegate{}
var _ authn.AuthenticationAttemptRecorder = (*recordingAuthenticationAttemptRecorder)(nil)
