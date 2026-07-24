package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestStaticCedarInvocationAuthorizerGovernsAllPhases(t *testing.T) {
	t.Parallel()

	policy, err := NewInvocationAuthorizationPolicy(
		InvocationAuthorizationPolicyScope{
			IsolationDomainID: "iso_1",
			ServiceID:         "svc_1",
			RevisionID:        "rev_1",
		},
		"policy_1",
		CanonicalInvocationCedarSchema(),
		[]byte(`permit(
			principal == DataGround::Actor::"actor_1",
			action,
			resource == DataGround::Invocation::"inv_1"
		) when {
			context.isolationDomainID == "iso_1" &&
			context.operationID == "op_1" &&
			context.serviceID == "svc_1" &&
			context.revisionID == "rev_1" &&
			context.correlationID == "corr_1"
		};`),
	)
	if err != nil {
		t.Fatalf("construct policy: %v", err)
	}
	authorizer, err := NewStaticCedarInvocationAuthorizer(
		[]InvocationAuthorizationPolicy{policy},
	)
	if err != nil {
		t.Fatalf("compose authorizer: %v", err)
	}
	admission := persistence.InvocationAdmissionTarget{
		IsolationDomainID: "iso_1",
		OperationID:       "op_1",
		InvocationID:      "inv_1",
		ServiceID:         "svc_1",
		RevisionID:        "rev_1",
		ActorID:           "actor_1",
		CorrelationID:     "corr_1",
	}
	if err := authorizer.AuthorizeInvocationAdmission(context.Background(), admission); err != nil {
		t.Fatalf("authorize admission: %v", err)
	}
	runtimeTarget := persistence.InvocationRuntimeTarget{
		IsolationDomainID: admission.IsolationDomainID,
		OperationID:       admission.OperationID,
		InvocationID:      admission.InvocationID,
		ServiceID:         admission.ServiceID,
		RevisionID:        admission.RevisionID,
		ActorID:           admission.ActorID,
		CorrelationID:     admission.CorrelationID,
	}
	if err := authorizer.AuthorizeInvocationRuntime(
		context.Background(),
		runtimeTarget,
		dgruntime.StartRequest{
			ApprovalMode: dgruntime.ApprovalLocked,
			SandboxMode:  dgruntime.SandboxReadOnly,
		},
	); err != nil {
		t.Fatalf("authorize runtime: %v", err)
	}
	cancellation := persistence.InvocationCancellationTarget{
		IsolationDomainID: admission.IsolationDomainID,
		OperationID:       admission.OperationID,
		InvocationID:      admission.InvocationID,
		ServiceID:         admission.ServiceID,
		RevisionID:        admission.RevisionID,
		ActorID:           admission.ActorID,
		CorrelationID:     admission.CorrelationID,
	}
	if err := authorizer.AuthorizeInvocationCancellation(
		context.Background(),
		cancellation,
	); err != nil {
		t.Fatalf("authorize cancellation: %v", err)
	}

	admission.RevisionID = "rev_missing"
	if err := authorizer.AuthorizeInvocationAdmission(
		context.Background(),
		admission,
	); !errors.Is(err, ErrInvocationAuthorizationPolicyUnavailable) {
		t.Fatalf("missing revision error = %v", err)
	}
}

func TestStaticCedarInvocationAuthorizerPreservesPhaseDenial(t *testing.T) {
	t.Parallel()

	policy, err := NewInvocationAuthorizationPolicy(
		InvocationAuthorizationPolicyScope{
			IsolationDomainID: "iso_1",
			ServiceID:         "svc_1",
			RevisionID:        "rev_1",
		},
		"policy_1",
		CanonicalInvocationCedarSchema(),
		[]byte(`permit(principal, action, resource) when { context.serviceID == "other" };`),
	)
	if err != nil {
		t.Fatalf("construct policy: %v", err)
	}
	authorizer, err := NewStaticCedarInvocationAuthorizer(
		[]InvocationAuthorizationPolicy{policy},
	)
	if err != nil {
		t.Fatalf("compose authorizer: %v", err)
	}
	err = authorizer.AuthorizeInvocationAdmission(
		context.Background(),
		persistence.InvocationAdmissionTarget{
			IsolationDomainID: "iso_1",
			OperationID:       "op_1",
			InvocationID:      "inv_1",
			ServiceID:         "svc_1",
			RevisionID:        "rev_1",
			ActorID:           "actor_1",
			CorrelationID:     "corr_1",
		},
	)
	if !errors.Is(err, ErrInvocationAdmissionDenied) ||
		!errors.Is(err, ErrInvocationAuthorizationDenied) {
		t.Fatalf("admission denial = %v", err)
	}
}

func TestStaticCedarInvocationAuthorizerRejectsInvalidPolicyAtStartup(t *testing.T) {
	t.Parallel()

	scope := InvocationAuthorizationPolicyScope{
		IsolationDomainID: "iso_1",
		ServiceID:         "svc_1",
		RevisionID:        "rev_1",
	}
	tests := map[string]struct {
		schema   []byte
		policies []byte
	}{
		"schema drift": {
			schema:   []byte(`{"DataGround":{}}`),
			policies: []byte("permit(principal, action, resource);"),
		},
		"parse diagnostic": {
			schema:   CanonicalInvocationCedarSchema(),
			policies: []byte("permit("),
		},
		"empty policy set": {
			schema:   CanonicalInvocationCedarSchema(),
			policies: []byte("// no policies"),
		},
	}
	for name, test := range tests {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			policy, err := NewInvocationAuthorizationPolicy(
				scope,
				"policy_1",
				test.schema,
				test.policies,
			)
			if err != nil {
				t.Fatalf("construct contract-valid policy: %v", err)
			}
			if _, err := NewStaticCedarInvocationAuthorizer(
				[]InvocationAuthorizationPolicy{policy},
			); !errors.Is(err, ErrInvocationAuthorizationPolicyInvalid) {
				t.Fatalf("composition error = %v", err)
			}
		})
	}
	if _, err := NewStaticCedarInvocationAuthorizer(nil); err == nil {
		t.Fatal("empty policy configuration was accepted")
	}
}
