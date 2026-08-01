package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/authz"
)

type invocationDecisionRecorderStub struct {
	records []authz.InvocationDecisionRecord
	err     error
}

func (recorder *invocationDecisionRecorderStub) RecordInvocationAuthorizationDecision(
	_ context.Context,
	record authz.InvocationDecisionRecord,
) error {
	recorder.records = append(recorder.records, record)
	return recorder.err
}

type nilInvocationDecisionRecorder struct{}

func (*nilInvocationDecisionRecorder) RecordInvocationAuthorizationDecision(
	context.Context,
	authz.InvocationDecisionRecord,
) error {
	return nil
}

func TestAuditedInvocationCedarEvaluatorRecordsCompletedOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		evaluation error
		wantOutcome authz.Outcome
		wantError   error
	}{
		{name: "allowed", wantOutcome: authz.OutcomeAllowed},
		{
			name:        "denied",
			evaluation: ErrInvocationAuthorizationDenied,
			wantOutcome: authz.OutcomeDenied,
			wantError:   ErrInvocationAuthorizationDenied,
		},
		{
			name:        "unavailable",
			evaluation: errors.New("Cedar diagnostic detail"),
			wantOutcome: authz.OutcomeUnavailable,
			wantError:   errors.New("Cedar diagnostic detail"),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := &invocationDecisionRecorderStub{}
			evaluator, err := NewAuditedInvocationCedarEvaluator(
				invocationCedarEvaluatorFunc(func(
					context.Context,
					InvocationAuthorizationPolicy,
					InvocationCedarInput,
				) error {
					return test.evaluation
				}),
				recorder,
			)
			if err != nil {
				t.Fatalf("construct audited evaluator: %v", err)
			}
			policy, input := invocationAuthorizationAuditFixture(t)
			got := evaluator.EvaluateInvocationAuthorization(context.Background(), policy, input)
			if test.wantError == nil && got != nil {
				t.Fatalf("evaluation error = %v", got)
			}
			if test.wantError != nil && got == nil {
				t.Fatal("evaluation unexpectedly succeeded")
			}
			if len(recorder.records) != 1 {
				t.Fatalf("record count = %d, want 1", len(recorder.records))
			}
			record := recorder.records[0]
			if record.ActorID != input.Principal.ID ||
				record.IsolationDomainID != input.IsolationDomainID ||
				record.OperationID != input.OperationID ||
				record.InvocationID != input.Resource.ID ||
				record.ServiceID != input.ServiceID ||
				record.RevisionID != input.RevisionID ||
				record.Action != authz.InvocationAdmit ||
				record.Outcome != test.wantOutcome ||
				record.PolicySetID != policy.PolicySetID ||
				record.PolicyDigest == "" ||
				record.CorrelationID != input.CorrelationID ||
				!record.Valid() {
				t.Fatalf("record = %#v", record)
			}
		})
	}
}

func TestAuditedInvocationCedarEvaluatorWithholdsDecisionOnRecorderFailure(t *testing.T) {
	t.Parallel()

	recorder := &invocationDecisionRecorderStub{err: errors.New("database detail")}
	evaluator, err := NewAuditedInvocationCedarEvaluator(
		invocationCedarEvaluatorFunc(func(
			context.Context,
			InvocationAuthorizationPolicy,
			InvocationCedarInput,
		) error {
			return nil
		}),
		recorder,
	)
	if err != nil {
		t.Fatalf("construct audited evaluator: %v", err)
	}
	policy, input := invocationAuthorizationAuditFixture(t)
	got := evaluator.EvaluateInvocationAuthorization(context.Background(), policy, input)
	if !errors.Is(got, ErrInvocationAuthorizationPolicyUnavailable) {
		t.Fatalf("evaluation error = %v, want unavailable", got)
	}
	if errors.Is(got, recorder.err) {
		t.Fatalf("evaluation exposed recorder detail: %v", got)
	}
	if len(recorder.records) != 1 {
		t.Fatalf("record attempts = %d, want 1", len(recorder.records))
	}
}

func TestAuditedInvocationCedarEvaluatorDoesNotRecordCancellation(t *testing.T) {
	t.Parallel()

	recorder := &invocationDecisionRecorderStub{}
	evaluator, err := NewAuditedInvocationCedarEvaluator(
		invocationCedarEvaluatorFunc(func(
			context.Context,
			InvocationAuthorizationPolicy,
			InvocationCedarInput,
		) error {
			return context.Canceled
		}),
		recorder,
	)
	if err != nil {
		t.Fatalf("construct audited evaluator: %v", err)
	}
	policy, input := invocationAuthorizationAuditFixture(t)
	if got := evaluator.EvaluateInvocationAuthorization(context.Background(), policy, input); !errors.Is(got, context.Canceled) {
		t.Fatalf("evaluation error = %v, want cancellation", got)
	}
	if len(recorder.records) != 0 {
		t.Fatalf("record count = %d, want 0", len(recorder.records))
	}
}

func TestAuditedInvocationCedarEvaluatorRejectsIncompleteAssemblyAndSerialization(t *testing.T) {
	t.Parallel()

	var delegate *nilInvocationCedarEvaluator
	if _, err := NewAuditedInvocationCedarEvaluator(delegate, &invocationDecisionRecorderStub{}); err == nil {
		t.Fatal("typed-nil evaluator was accepted")
	}
	var recorder *nilInvocationDecisionRecorder
	if _, err := NewAuditedInvocationCedarEvaluator(
		invocationCedarEvaluatorFunc(func(context.Context, InvocationAuthorizationPolicy, InvocationCedarInput) error {
			return nil
		}),
		recorder,
	); err == nil {
		t.Fatal("typed-nil recorder was accepted")
	}
	evaluator, err := NewAuditedInvocationCedarEvaluator(
		invocationCedarEvaluatorFunc(func(context.Context, InvocationAuthorizationPolicy, InvocationCedarInput) error {
			return nil
		}),
		&invocationDecisionRecorderStub{},
	)
	if err != nil {
		t.Fatalf("construct audited evaluator: %v", err)
	}
	if _, err := json.Marshal(evaluator); err == nil {
		t.Fatal("audited evaluator serialized")
	}
}

func TestAuditedCedarInvocationAuthorizerDoesNotInventLookupDecisions(t *testing.T) {
	t.Parallel()

	recorder := &invocationDecisionRecorderStub{}
	authorizer, err := NewAuditedCedarInvocationAuthorizer(
		invocationAuthorizationPolicySourceFunc(func(
			context.Context,
			InvocationAuthorizationPolicyScope,
		) (InvocationAuthorizationPolicy, error) {
			return InvocationAuthorizationPolicy{}, errors.New("database detail")
		}),
		recorder,
	)
	if err != nil {
		t.Fatalf("construct audited authorizer: %v", err)
	}
	_, input := invocationAuthorizationAuditFixture(t)
	request := InvocationAuthorizationRequest{
		Action:            InvocationAuthorizationAdmit,
		IsolationDomainID: input.IsolationDomainID,
		OperationID:       input.OperationID,
		InvocationID:      input.Resource.ID,
		ServiceID:         input.ServiceID,
		RevisionID:        input.RevisionID,
		ActorID:           input.Principal.ID,
		CorrelationID:     input.CorrelationID,
	}
	got := authorizer.decision.AuthorizeInvocationEffect(context.Background(), request)
	if !errors.Is(got, ErrInvocationAuthorizationPolicyUnavailable) {
		t.Fatalf("authorization error = %v, want unavailable", got)
	}
	if len(recorder.records) != 0 {
		t.Fatalf("lookup failure record count = %d, want 0", len(recorder.records))
	}
}

func invocationAuthorizationAuditFixture(
	t *testing.T,
) (InvocationAuthorizationPolicy, InvocationCedarInput) {
	t.Helper()
	policy, err := NewInvocationAuthorizationPolicy(
		InvocationAuthorizationPolicyScope{
			IsolationDomainID: "iso_00000000000000000001",
			ServiceID:         "svc_00000000000000000001",
			RevisionID:        "rev_00000000000000000001",
		},
		"policy.audit-test",
		CanonicalInvocationCedarSchema(),
		[]byte("permit(principal, action, resource);"),
	)
	if err != nil {
		t.Fatalf("construct policy: %v", err)
	}
	input := InvocationCedarInput{
		Contract: InvocationCedarContract,
		Principal: InvocationCedarEntityUID{
			Type: invocationCedarPrincipalType,
			ID:   "operator@example.invalid",
		},
		Action: InvocationCedarEntityUID{
			Type: invocationCedarActionType,
			ID:   string(InvocationAuthorizationAdmit),
		},
		Resource: InvocationCedarEntityUID{
			Type: invocationCedarResourceType,
			ID:   "inv_00000000000000000001",
		},
		IsolationDomainID: policy.IsolationDomainID,
		OperationID:       "op_00000000000000000001",
		ServiceID:         policy.ServiceID,
		RevisionID:        policy.RevisionID,
		CorrelationID:     "cor_00000000000000000001",
	}
	return policy, input
}
