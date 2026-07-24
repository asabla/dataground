package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type invocationAuthorizationPolicySourceFunc func(
	context.Context,
	InvocationAuthorizationPolicyScope,
) (InvocationAuthorizationPolicy, error)

func (source invocationAuthorizationPolicySourceFunc) ResolveInvocationAuthorizationPolicy(
	ctx context.Context,
	scope InvocationAuthorizationPolicyScope,
) (InvocationAuthorizationPolicy, error) {
	return source(ctx, scope)
}

type invocationCedarEvaluatorFunc func(
	context.Context,
	InvocationAuthorizationPolicy,
	InvocationCedarInput,
) error

func (evaluator invocationCedarEvaluatorFunc) EvaluateInvocationAuthorization(
	ctx context.Context,
	policy InvocationAuthorizationPolicy,
	input InvocationCedarInput,
) error {
	return evaluator(ctx, policy, input)
}

type nilInvocationAuthorizationPolicySource struct{}

func (*nilInvocationAuthorizationPolicySource) ResolveInvocationAuthorizationPolicy(
	context.Context,
	InvocationAuthorizationPolicyScope,
) (InvocationAuthorizationPolicy, error) {
	return InvocationAuthorizationPolicy{}, nil
}

type nilInvocationCedarEvaluator struct{}

func (*nilInvocationCedarEvaluator) EvaluateInvocationAuthorization(
	context.Context,
	InvocationAuthorizationPolicy,
	InvocationCedarInput,
) error {
	return nil
}

func TestPolicyBoundInvocationAuthorizationDecisionBindsAndOwnsPolicy(t *testing.T) {
	t.Parallel()

	policy := testInvocationAuthorizationPolicy()
	request := testInvocationAuthorizationRequest()
	sourceCalls := 0
	evaluatorCalls := 0
	decision, err := NewPolicyBoundInvocationAuthorizationDecision(
		invocationAuthorizationPolicySourceFunc(func(
			_ context.Context,
			scope InvocationAuthorizationPolicyScope,
		) (InvocationAuthorizationPolicy, error) {
			sourceCalls++
			want := InvocationAuthorizationPolicyScope{
				IsolationDomainID: request.IsolationDomainID,
				ServiceID:         request.ServiceID,
				RevisionID:        request.RevisionID,
			}
			if scope != want {
				t.Fatalf("policy scope = %#v, want %#v", scope, want)
			}
			return policy, nil
		}),
		invocationCedarEvaluatorFunc(func(
			_ context.Context,
			got InvocationAuthorizationPolicy,
			gotInput InvocationCedarInput,
		) error {
			evaluatorCalls++
			if got.Contract != InvocationAuthorizationPolicyContract ||
				got.PolicySetID != policy.PolicySetID ||
				gotInput.Action.ID != "admit" {
				t.Fatalf("evaluation input = %#v, %#v", got, gotInput)
			}
			got.Schema[0] = 'X'
			got.Policies[0] = 'X'
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("construct policy-bound decision: %v", err)
	}
	if err := decision.AuthorizeInvocationEffect(context.Background(), request); err != nil {
		t.Fatalf("authorize invocation: %v", err)
	}
	if sourceCalls != 1 || evaluatorCalls != 1 {
		t.Fatalf("calls = source %d, evaluator %d", sourceCalls, evaluatorCalls)
	}
	if string(policy.Schema) != `{"type":"object"}` || string(policy.Policies) != "permit(principal, action, resource);" {
		t.Fatal("evaluation mutated source-owned policy bytes")
	}
}

func TestPolicyBoundInvocationAuthorizationDecisionRejectsPolicyDrift(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*InvocationAuthorizationPolicy){
		"contract": func(policy *InvocationAuthorizationPolicy) {
			policy.Contract = "dataground.invocation-authorization-policy/v2"
		},
		"isolation domain": func(policy *InvocationAuthorizationPolicy) {
			policy.IsolationDomainID = "iso_other"
		},
		"service": func(policy *InvocationAuthorizationPolicy) {
			policy.ServiceID = "svc_other"
		},
		"revision": func(policy *InvocationAuthorizationPolicy) {
			policy.RevisionID = "rev_other"
		},
		"policy set": func(policy *InvocationAuthorizationPolicy) {
			policy.PolicySetID = ""
		},
		"unsafe policy set": func(policy *InvocationAuthorizationPolicy) {
			policy.PolicySetID = "../policy"
		},
		"oversized policy set": func(policy *InvocationAuthorizationPolicy) {
			policy.PolicySetID = strings.Repeat("a", maxInvocationAuthorizationPolicyIDBytes+1)
		},
		"digest": func(policy *InvocationAuthorizationPolicy) {
			policy.Policies = append(policy.Policies, ' ')
		},
		"empty schema": func(policy *InvocationAuthorizationPolicy) {
			policy.Schema = nil
		},
		"empty policies": func(policy *InvocationAuthorizationPolicy) {
			policy.Policies = nil
		},
		"oversized schema": func(policy *InvocationAuthorizationPolicy) {
			policy.Schema = make([]byte, maxInvocationAuthorizationSchemaBytes+1)
			policy.Digest = invocationAuthorizationPolicyDigest(policy.Schema, policy.Policies)
		},
		"oversized policies": func(policy *InvocationAuthorizationPolicy) {
			policy.Policies = make([]byte, maxInvocationAuthorizationPolicyBytes+1)
			policy.Digest = invocationAuthorizationPolicyDigest(policy.Schema, policy.Policies)
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			policy := testInvocationAuthorizationPolicy()
			mutate(&policy)
			evaluatorCalls := 0
			decision, err := NewPolicyBoundInvocationAuthorizationDecision(
				invocationAuthorizationPolicySourceFunc(func(
					context.Context,
					InvocationAuthorizationPolicyScope,
				) (InvocationAuthorizationPolicy, error) {
					return policy, nil
				}),
				invocationCedarEvaluatorFunc(func(
					context.Context,
					InvocationAuthorizationPolicy,
					InvocationCedarInput,
				) error {
					evaluatorCalls++
					return nil
				}),
			)
			if err != nil {
				t.Fatalf("construct policy-bound decision: %v", err)
			}
			if err := decision.AuthorizeInvocationEffect(
				context.Background(),
				testInvocationAuthorizationRequest(),
			); !errors.Is(err, ErrInvocationAuthorizationPolicyInvalid) {
				t.Fatalf("authorization error = %v", err)
			}
			if evaluatorCalls != 0 {
				t.Fatalf("evaluator calls = %d, want 0", evaluatorCalls)
			}
		})
	}
}

func TestPolicyBoundInvocationAuthorizationDecisionMapsStableOutcomes(t *testing.T) {
	t.Parallel()

	request := testInvocationAuthorizationRequest()
	policy := testInvocationAuthorizationPolicy()
	sourceFailure := errors.New("source detail")
	evaluatorFailure := errors.New("evaluator detail")
	tests := []struct {
		name      string
		sourceErr error
		evalErr   error
		want      error
	}{
		{name: "source unavailable", sourceErr: sourceFailure, want: ErrInvocationAuthorizationPolicyUnavailable},
		{name: "evaluation unavailable", evalErr: evaluatorFailure, want: ErrInvocationAuthorizationPolicyUnavailable},
		{name: "denied", evalErr: ErrInvocationAuthorizationDenied, want: ErrInvocationAuthorizationDenied},
		{name: "cancelled", sourceErr: context.Canceled, want: context.Canceled},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			decision, err := NewPolicyBoundInvocationAuthorizationDecision(
				invocationAuthorizationPolicySourceFunc(func(
					context.Context,
					InvocationAuthorizationPolicyScope,
				) (InvocationAuthorizationPolicy, error) {
					return policy, test.sourceErr
				}),
				invocationCedarEvaluatorFunc(func(
					context.Context,
					InvocationAuthorizationPolicy,
					InvocationCedarInput,
				) error {
					return test.evalErr
				}),
			)
			if err != nil {
				t.Fatalf("construct policy-bound decision: %v", err)
			}
			got := decision.AuthorizeInvocationEffect(context.Background(), request)
			if !errors.Is(got, test.want) {
				t.Fatalf("authorization error = %v, want %v", got, test.want)
			}
			if errors.Is(got, sourceFailure) || errors.Is(got, evaluatorFailure) {
				t.Fatalf("authorization exposed dependency detail: %v", got)
			}
		})
	}
}

func TestStaticInvocationAuthorizationPolicySourceResolvesExactOwnedPolicy(t *testing.T) {
	t.Parallel()

	policy := testInvocationAuthorizationPolicy()
	source, err := NewStaticInvocationAuthorizationPolicySource(
		[]InvocationAuthorizationPolicy{policy},
	)
	if err != nil {
		t.Fatalf("construct static policy source: %v", err)
	}
	policy.Schema[0] = 'X'
	policy.Policies[0] = 'X'

	scope := InvocationAuthorizationPolicyScope{
		IsolationDomainID: "iso_1",
		ServiceID:         "svc_1",
		RevisionID:        "rev_1",
	}
	first, err := source.ResolveInvocationAuthorizationPolicy(context.Background(), scope)
	if err != nil {
		t.Fatalf("resolve policy: %v", err)
	}
	if string(first.Schema) != `{\"type\":\"object\"}` ||
		string(first.Policies) != "permit(principal, action, resource);" {
		t.Fatal("source did not retain owned policy bytes")
	}
	first.Schema[0] = 'X'
	first.Policies[0] = 'X'
	second, err := source.ResolveInvocationAuthorizationPolicy(context.Background(), scope)
	if err != nil {
		t.Fatalf("resolve policy again: %v", err)
	}
	if second.Schema[0] == 'X' || second.Policies[0] == 'X' {
		t.Fatal("resolved policy shared mutable bytes")
	}
}

func TestStaticInvocationAuthorizationPolicySourceRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	policy := testInvocationAuthorizationPolicy()
	tests := map[string][]InvocationAuthorizationPolicy{
		"empty":                  nil,
		"duplicate scope":        {policy, policy},
		"empty isolation domain": {func() InvocationAuthorizationPolicy {
			invalid := policy
			invalid.IsolationDomainID = ""
			return invalid
		}()},
		"digest mismatch": {func() InvocationAuthorizationPolicy {
			invalid := policy
			invalid.Policies = append(invalid.Policies, ' ')
			return invalid
		}()},
	}
	for name, policies := range tests {
		name, policies := name, policies
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewStaticInvocationAuthorizationPolicySource(policies); err == nil {
				t.Fatal("invalid policy configuration was accepted")
			}
		})
	}
}

func TestStaticInvocationAuthorizationPolicySourceFailsClosed(t *testing.T) {
	t.Parallel()

	source, err := NewStaticInvocationAuthorizationPolicySource(
		[]InvocationAuthorizationPolicy{testInvocationAuthorizationPolicy()},
	)
	if err != nil {
		t.Fatalf("construct static policy source: %v", err)
	}
	tests := []struct {
		name  string
		ctx   context.Context
		scope InvocationAuthorizationPolicyScope
		want  error
	}{
		{
			name: "missing exact revision",
			ctx:  context.Background(),
			scope: InvocationAuthorizationPolicyScope{
				IsolationDomainID: "iso_1",
				ServiceID:         "svc_1",
				RevisionID:        "rev_other",
			},
			want: ErrInvocationAuthorizationPolicyUnavailable,
		},
		{
			name:  "invalid scope",
			ctx:   context.Background(),
			scope: InvocationAuthorizationPolicyScope{},
			want:  ErrInvocationAuthorizationPolicyUnavailable,
		},
		{
			name: "cancelled",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}(),
			scope: InvocationAuthorizationPolicyScope{
				IsolationDomainID: "iso_1",
				ServiceID:         "svc_1",
				RevisionID:        "rev_1",
			},
			want: context.Canceled,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := source.ResolveInvocationAuthorizationPolicy(test.ctx, test.scope)
			if !errors.Is(err, test.want) {
				t.Fatalf("resolve error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestInvocationAuthorizationPolicyExcludesContentFromSerialization(t *testing.T) {
	t.Parallel()

	policy := testInvocationAuthorizationPolicy()
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal policy metadata: %v", err)
	}
	if strings.Contains(string(encoded), string(policy.Schema)) ||
		strings.Contains(string(encoded), string(policy.Policies)) {
		t.Fatalf("serialized policy exposed content: %s", encoded)
	}
}

func TestPolicyBoundInvocationAuthorizationDecisionFailsClosedAtConstruction(t *testing.T) {
	t.Parallel()

	source := invocationAuthorizationPolicySourceFunc(func(
		context.Context,
		InvocationAuthorizationPolicyScope,
	) (InvocationAuthorizationPolicy, error) {
		return testInvocationAuthorizationPolicy(), nil
	})
	evaluator := invocationCedarEvaluatorFunc(func(
		context.Context,
		InvocationAuthorizationPolicy,
		InvocationCedarInput,
	) error {
		return nil
	})
	var nilSource *nilInvocationAuthorizationPolicySource
	var nilEvaluator *nilInvocationCedarEvaluator
	tests := []struct {
		name      string
		source    InvocationAuthorizationPolicySource
		evaluator InvocationCedarEvaluator
	}{
		{name: "missing source", evaluator: evaluator},
		{name: "typed nil source", source: nilSource, evaluator: evaluator},
		{name: "missing evaluator", source: source},
		{name: "typed nil evaluator", source: source, evaluator: nilEvaluator},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewPolicyBoundInvocationAuthorizationDecision(
				test.source,
				test.evaluator,
			); err == nil {
				t.Fatal("invalid dependencies were accepted")
			}
		})
	}
}

func testInvocationAuthorizationPolicy() InvocationAuthorizationPolicy {
	policy := InvocationAuthorizationPolicy{
		Contract:          InvocationAuthorizationPolicyContract,
		IsolationDomainID: "iso_1",
		ServiceID:         "svc_1",
		RevisionID:        "rev_1",
		PolicySetID:       "policy_1",
		Schema:            []byte(`{"type":"object"}`),
		Policies:          []byte("permit(principal, action, resource);"),
	}
	policy.Digest = invocationAuthorizationPolicyDigest(policy.Schema, policy.Policies)
	return policy
}

func testInvocationAuthorizationRequest() InvocationAuthorizationRequest {
	return InvocationAuthorizationRequest{
		Action:            InvocationAuthorizationAdmit,
		IsolationDomainID: "iso_1",
		OperationID:       "op_1",
		InvocationID:      "inv_1",
		ServiceID:         "svc_1",
		RevisionID:        "rev_1",
		ActorID:           "actor_1",
		CorrelationID:     "corr_1",
	}
}
