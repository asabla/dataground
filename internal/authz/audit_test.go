package authz_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/identity"
)

const (
	auditActor  = "usr_00000000000000000001"
	otherActor  = "usr_00000000000000000002"
	auditDomain = "iso_00000000000000000001"
)

func TestAuditedAuthorizerRecordsClosedDecisionAndPolicyProvenance(t *testing.T) {
	t.Parallel()

	delegate, err := authz.NewDevelopmentCedarAuthorizer(auditActor, auditDomain)
	if err != nil {
		t.Fatalf("create Cedar authorizer: %v", err)
	}
	recorder := &recordingDecisionRecorder{}
	authorizer, err := authz.NewAuditedAuthorizer(delegate, recorder)
	if err != nil {
		t.Fatalf("create audited authorizer: %v", err)
	}

	allowed := auditRequest(t, auditActor)
	if err := authorizer.Authorize(context.Background(), allowed); err != nil {
		t.Fatalf("authorize allowed request: %v", err)
	}
	denied := auditRequest(t, otherActor)
	if err := authorizer.Authorize(context.Background(), denied); !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("authorize denied request: %v", err)
	}

	if len(recorder.records) != 2 {
		t.Fatalf("recorded decisions = %d, want 2", len(recorder.records))
	}
	if recorder.records[0].Outcome != authz.OutcomeAllowed ||
		recorder.records[1].Outcome != authz.OutcomeDenied {
		t.Fatalf("decision outcomes = %#v", recorder.records)
	}
	for index, record := range recorder.records {
		if !record.Valid() ||
			record.PrincipalID != []string{auditActor, otherActor}[index] ||
			record.PrincipalKind != authn.PrincipalHuman ||
			record.IsolationDomainID != auditDomain ||
			record.Action != authz.ReadInvocation ||
			record.ResourceType != authz.Invocation ||
			record.ResourceID != "inv_00000000000000000001" ||
			record.PolicySetID != "dataground-development-api" ||
			record.PolicyDigest == "" ||
			record.CorrelationID == "" {
			t.Fatalf("decision record %d = %#v", index, record)
		}
	}
	if recorder.records[0].PolicyDigest != recorder.records[1].PolicyDigest {
		t.Fatal("one immutable policy produced different audit digests")
	}
	if _, err := json.Marshal(authorizer); err == nil {
		t.Fatal("audited authorizer serialized")
	}
}

func TestAuditedAuthorizerFailsClosedWhenDecisionCannotBeRecorded(t *testing.T) {
	t.Parallel()

	delegate, err := authz.NewDevelopmentCedarAuthorizer(auditActor, auditDomain)
	if err != nil {
		t.Fatalf("create Cedar authorizer: %v", err)
	}
	authorizer, err := authz.NewAuditedAuthorizer(delegate, failingDecisionRecorder{})
	if err != nil {
		t.Fatalf("create audited authorizer: %v", err)
	}
	if err := authorizer.Authorize(context.Background(), auditRequest(t, auditActor)); !errors.Is(err, authz.ErrUnavailable) {
		t.Fatalf("allowed decision without audit = %v, want unavailable", err)
	}
	if err := authorizer.Authorize(context.Background(), auditRequest(t, otherActor)); !errors.Is(err, authz.ErrUnavailable) {
		t.Fatalf("denied decision without audit = %v, want unavailable", err)
	}
}

func TestAuditedAuthorizerRejectsIncompleteAuditAssemblyAndCorrelation(t *testing.T) {
	t.Parallel()

	delegate, err := authz.NewDevelopmentCedarAuthorizer(auditActor, auditDomain)
	if err != nil {
		t.Fatalf("create Cedar authorizer: %v", err)
	}
	var recorder *recordingDecisionRecorder
	if _, err := authz.NewAuditedAuthorizer(delegate, recorder); err == nil {
		t.Fatal("typed-nil decision recorder was accepted")
	}
	authorizer, err := authz.NewAuditedAuthorizer(delegate, &recordingDecisionRecorder{})
	if err != nil {
		t.Fatalf("create audited authorizer: %v", err)
	}
	request := auditRequest(t, auditActor)
	request.CorrelationID = ""
	if err := authorizer.Authorize(context.Background(), request); !errors.Is(err, authz.ErrUnavailable) {
		t.Fatalf("decision without correlation = %v, want unavailable", err)
	}
}

func auditRequest(t *testing.T, actorID string) authz.Request {
	t.Helper()
	principal, err := authn.NewPrincipal(authn.PrincipalInput{
		ID: actorID, Kind: authn.PrincipalHuman, Issuer: "test", Subject: actorID,
		Audience: authn.APIAudience, IsolationDomains: []string{auditDomain},
	})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	return authz.Request{
		Principal: principal, Action: authz.ReadInvocation, ResourceType: authz.Invocation,
		ResourceID: "inv_00000000000000000001", IsolationDomainID: auditDomain,
		CorrelationID: identity.New("cor"),
	}
}

type recordingDecisionRecorder struct {
	records []authz.DecisionRecord
}

func (recorder *recordingDecisionRecorder) RecordAuthorizationDecision(
	_ context.Context,
	record authz.DecisionRecord,
) error {
	recorder.records = append(recorder.records, record)
	return nil
}

type failingDecisionRecorder struct{}

func (failingDecisionRecorder) RecordAuthorizationDecision(context.Context, authz.DecisionRecord) error {
	return errors.New("injected audit failure")
}

var _ authz.DecisionRecorder = (*recordingDecisionRecorder)(nil)
var _ authz.DecisionRecorder = failingDecisionRecorder{}
