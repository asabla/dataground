package reconcile

import (
	"context"
	"errors"
	"testing"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestCedarInvocationAuthorizationEvaluatorPermitsClosedInput(t *testing.T) {
	t.Parallel()

	evaluator := NewCedarInvocationAuthorizationEvaluator()
	policy := testCanonicalInvocationAuthorizationPolicy(`permit(
		principal == DataGround::Actor::"actor_1",
		action == DataGround::Action::"admit",
		resource == DataGround::Invocation::"inv_1"
	) when {
		context.isolationDomainID == "iso_1" &&
		context.operationID == "op_1" &&
		context.serviceID == "svc_1" &&
		context.revisionID == "rev_1" &&
		context.correlationID == "corr_1"
	};`)
	input := testInvocationCedarInput(t, testInvocationAuthorizationRequest())
	if err := evaluator.EvaluateInvocationAuthorization(context.Background(), policy, input); err != nil {
		t.Fatalf("evaluate permit: %v", err)
	}
}

func TestCedarInvocationAuthorizationEvaluatorMapsCleanDeny(t *testing.T) {
	t.Parallel()

	evaluator := NewCedarInvocationAuthorizationEvaluator()
	input := testInvocationCedarInput(t, testInvocationAuthorizationRequest())
	tests := map[string]string{
		"no matching permit": `permit(principal, action, resource) when { context.serviceID == "other" };`,
		"matching forbid": `
			permit(principal, action, resource);
			forbid(principal, action, resource) when { context.serviceID == "svc_1" };
		`,
	}
	for name, policies := range tests {
		name, policies := name, policies
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := evaluator.EvaluateInvocationAuthorization(
				context.Background(),
				testCanonicalInvocationAuthorizationPolicy(policies),
				input,
			)
			if !errors.Is(err, ErrInvocationAuthorizationDenied) {
				t.Fatalf("evaluation error = %v, want denial", err)
			}
		})
	}
}

func TestCedarInvocationAuthorizationEvaluatorRejectsUncertainEvaluation(t *testing.T) {
	t.Parallel()

	evaluator := NewCedarInvocationAuthorizationEvaluator()
	input := testInvocationCedarInput(t, testInvocationAuthorizationRequest())
	tests := map[string]InvocationAuthorizationPolicy{
		"schema drift": func() InvocationAuthorizationPolicy {
			policy := testCanonicalInvocationAuthorizationPolicy("permit(principal, action, resource);")
			policy.Schema = append(policy.Schema, ' ')
			policy.Digest = invocationAuthorizationPolicyDigest(policy.Schema, policy.Policies)
			return policy
		}(),
		"parse diagnostic": testCanonicalInvocationAuthorizationPolicy("permit("),
		"empty policy set": testCanonicalInvocationAuthorizationPolicy("// no policies"),
		"evaluation diagnostic": testCanonicalInvocationAuthorizationPolicy(
			`permit(principal, action, resource) when { context.missing == "value" };`,
		),
	}
	for name, policy := range tests {
		name, policy := name, policy
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := evaluator.EvaluateInvocationAuthorization(context.Background(), policy, input)
			if !errors.Is(err, errInvocationCedarEvaluation) {
				t.Fatalf("evaluation error = %v, want unavailable evaluation", err)
			}
			if errors.Is(err, ErrInvocationAuthorizationDenied) {
				t.Fatalf("uncertain evaluation became denial: %v", err)
			}
		})
	}
}

func TestCedarInvocationAuthorizationEvaluatorMapsRuntimeFacts(t *testing.T) {
	t.Parallel()

	request := testInvocationAuthorizationRequest()
	request.Action = InvocationAuthorizationRun
	request.Runtime = &dgruntime.StartRequest{
		ApprovalMode: dgruntime.ApprovalLocked,
		SandboxMode:  dgruntime.SandboxWorkspaceWrite,
		OutputSchema: map[string]any{"type": "object"},
		Artifacts: []dgruntime.ArtifactDeclaration{
			{Kind: "report"},
			{Kind: "file"},
			{Kind: "file"},
		},
	}
	input := testInvocationCedarInput(t, request)
	policy := testCanonicalInvocationAuthorizationPolicy(`permit(
		principal,
		action == DataGround::Action::"run",
		resource
	) when {
		context.runtime.approvalMode == "locked" &&
		context.runtime.sandboxMode == "workspace-write" &&
		context.runtime.hasOutputSchema &&
		context.runtime.artifactCount == 3 &&
		context.runtime.artifactKinds.contains("file") &&
		context.runtime.artifactKinds.contains("report")
	};`)
	if err := NewCedarInvocationAuthorizationEvaluator().EvaluateInvocationAuthorization(
		context.Background(),
		policy,
		input,
	); err != nil {
		t.Fatalf("evaluate runtime facts: %v", err)
	}
}

func TestCedarInvocationAuthorizationEvaluatorHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := NewCedarInvocationAuthorizationEvaluator().EvaluateInvocationAuthorization(
		ctx,
		testCanonicalInvocationAuthorizationPolicy("permit(principal, action, resource);"),
		testInvocationCedarInput(t, testInvocationAuthorizationRequest()),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("evaluation error = %v, want cancellation", err)
	}
}

func TestCanonicalInvocationCedarSchemaIsOwned(t *testing.T) {
	t.Parallel()

	first := CanonicalInvocationCedarSchema()
	second := CanonicalInvocationCedarSchema()
	first[0] = 'X'
	if second[0] == 'X' {
		t.Fatal("canonical schema accessor returned shared mutable bytes")
	}
}

func testCanonicalInvocationAuthorizationPolicy(policies string) InvocationAuthorizationPolicy {
	policy := testInvocationAuthorizationPolicy()
	policy.Schema = CanonicalInvocationCedarSchema()
	policy.Policies = []byte(policies)
	policy.Digest = invocationAuthorizationPolicyDigest(policy.Schema, policy.Policies)
	return policy
}

func testInvocationCedarInput(
	t *testing.T,
	request InvocationAuthorizationRequest,
) InvocationCedarInput {
	t.Helper()
	input, err := mapInvocationCedarInput(request)
	if err != nil {
		t.Fatalf("map Cedar input: %v", err)
	}
	return input
}
