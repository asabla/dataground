package reconcile

import "github.com/asabla/dataground/internal/authz"

// NewCedarInvocationAuthorizer composes an exact-scope policy source with the
// canonical Cedar evaluator and the shared governed-phase authorizer. Durable
// sources remain responsible for returning immutable, validated bundle bytes.
func NewCedarInvocationAuthorizer(
	source InvocationAuthorizationPolicySource,
) (*InvocationAuthorizer, error) {
	return newCedarInvocationAuthorizer(source, NewCedarInvocationAuthorizationEvaluator())
}

// NewAuditedCedarInvocationAuthorizer composes the same exact-scope evaluator
// while withholding completed decisions until their durable audit record is
// accepted. Policy lookup failures remain dependency failures rather than
// completed decisions.
func NewAuditedCedarInvocationAuthorizer(
	source InvocationAuthorizationPolicySource,
	recorder authz.InvocationDecisionRecorder,
) (*InvocationAuthorizer, error) {
	evaluator, err := NewAuditedInvocationCedarEvaluator(
		NewCedarInvocationAuthorizationEvaluator(),
		recorder,
	)
	if err != nil {
		return nil, err
	}
	return newCedarInvocationAuthorizer(source, evaluator)
}

func newCedarInvocationAuthorizer(
	source InvocationAuthorizationPolicySource,
	evaluator InvocationCedarEvaluator,
) (*InvocationAuthorizer, error) {
	decision, err := NewPolicyBoundInvocationAuthorizationDecision(source, evaluator)
	if err != nil {
		return nil, err
	}
	authorizer, err := NewInvocationAuthorizer(decision)
	if err != nil {
		return nil, err
	}
	return authorizer, nil
}

// NewStaticCedarInvocationAuthorizer composes explicit immutable policy bundles
// with the canonical Cedar evaluator and the shared governed-phase authorizer.
// It validates every concrete Cedar policy before returning so deployment
// configuration fails before any consequential effect is attempted.
func NewStaticCedarInvocationAuthorizer(
	policies []InvocationAuthorizationPolicy,
) (*InvocationAuthorizer, error) {
	for _, policy := range policies {
		scope := InvocationAuthorizationPolicyScope{
			IsolationDomainID: policy.IsolationDomainID,
			ServiceID:         policy.ServiceID,
			RevisionID:        policy.RevisionID,
		}
		if _, err := validatedInvocationCedarPolicySet(policy, scope); err != nil {
			return nil, ErrInvocationAuthorizationPolicyInvalid
		}
	}
	source, err := NewStaticInvocationAuthorizationPolicySource(policies)
	if err != nil {
		return nil, err
	}
	return NewCedarInvocationAuthorizer(source)
}
