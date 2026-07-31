package reconcile

// NewCedarInvocationAuthorizer composes an exact-scope policy source with the
// canonical Cedar evaluator and the shared governed-phase authorizer. Durable
// sources remain responsible for returning immutable, validated bundle bytes.
func NewCedarInvocationAuthorizer(
	source InvocationAuthorizationPolicySource,
) (*InvocationAuthorizer, error) {
	decision, err := NewPolicyBoundInvocationAuthorizationDecision(
		source,
		NewCedarInvocationAuthorizationEvaluator(),
	)
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
