package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"regexp"
)

const InvocationAuthorizationPolicyContract = "dataground.invocation-authorization-policy/v1"

const maxInvocationAuthorizationPolicyIDBytes = 128

const maxInvocationAuthorizationSchemaBytes = 1 << 20

const maxInvocationAuthorizationPolicyBytes = 1 << 20

var (
	ErrInvocationAuthorizationPolicyInvalid     = errors.New("invocation authorization policy is invalid")
	ErrInvocationAuthorizationPolicyUnavailable = errors.New("invocation authorization policy is unavailable")
	invocationAuthorizationPolicyIDPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

type InvocationAuthorizationPolicyScope struct {
	IsolationDomainID string
	ServiceID         string
	RevisionID        string
}

type InvocationAuthorizationPolicy struct {
	Contract          string
	IsolationDomainID string
	ServiceID         string
	RevisionID        string
	PolicySetID       string
	Digest            [sha256.Size]byte
	Schema            []byte `json:"-"`
	Policies          []byte `json:"-"`
}

type invocationAuthorizationPolicyKey struct {
	IsolationDomainID string
	ServiceID         string
	RevisionID        string
}

type StaticInvocationAuthorizationPolicySource struct {
	policies map[invocationAuthorizationPolicyKey]InvocationAuthorizationPolicy
}

func NewStaticInvocationAuthorizationPolicySource(
	policies []InvocationAuthorizationPolicy,
) (*StaticInvocationAuthorizationPolicySource, error) {
	if len(policies) == 0 {
		return nil, errors.New("at least one invocation authorization policy is required")
	}
	source := &StaticInvocationAuthorizationPolicySource{
		policies: make(map[invocationAuthorizationPolicyKey]InvocationAuthorizationPolicy, len(policies)),
	}
	for _, policy := range policies {
		scope := InvocationAuthorizationPolicyScope{
			IsolationDomainID: policy.IsolationDomainID,
			ServiceID:         policy.ServiceID,
			RevisionID:        policy.RevisionID,
		}
		if !validInvocationAuthorizationPolicyScope(scope) ||
			!validInvocationAuthorizationPolicy(policy, scope) {
			return nil, ErrInvocationAuthorizationPolicyInvalid
		}
		key := invocationAuthorizationPolicyKey(scope)
		if _, exists := source.policies[key]; exists {
			return nil, errors.New("duplicate invocation authorization policy scope")
		}
		source.policies[key] = cloneInvocationAuthorizationPolicy(policy)
	}
	return source, nil
}

func (source *StaticInvocationAuthorizationPolicySource) ResolveInvocationAuthorizationPolicy(
	ctx context.Context,
	scope InvocationAuthorizationPolicyScope,
) (InvocationAuthorizationPolicy, error) {
	if err := ctx.Err(); err != nil {
		return InvocationAuthorizationPolicy{}, err
	}
	if source == nil || !validInvocationAuthorizationPolicyScope(scope) {
		return InvocationAuthorizationPolicy{}, ErrInvocationAuthorizationPolicyUnavailable
	}
	policy, ok := source.policies[invocationAuthorizationPolicyKey(scope)]
	if !ok {
		return InvocationAuthorizationPolicy{}, ErrInvocationAuthorizationPolicyUnavailable
	}
	return cloneInvocationAuthorizationPolicy(policy), nil
}

func validInvocationAuthorizationPolicyScope(scope InvocationAuthorizationPolicyScope) bool {
	return scope.IsolationDomainID != "" && scope.ServiceID != "" && scope.RevisionID != ""
}

type InvocationAuthorizationPolicySource interface {
	ResolveInvocationAuthorizationPolicy(
		context.Context,
		InvocationAuthorizationPolicyScope,
	) (InvocationAuthorizationPolicy, error)
}

type InvocationCedarEvaluator interface {
	EvaluateInvocationAuthorization(
		context.Context,
		InvocationAuthorizationPolicy,
		InvocationCedarInput,
	) error
}

type PolicyBoundInvocationAuthorizationDecision struct {
	source    InvocationAuthorizationPolicySource
	evaluator InvocationCedarEvaluator
}

func NewPolicyBoundInvocationAuthorizationDecision(
	source InvocationAuthorizationPolicySource,
	evaluator InvocationCedarEvaluator,
) (*PolicyBoundInvocationAuthorizationDecision, error) {
	if governedInvocationDependencyMissing(source) {
		return nil, errors.New("invocation authorization policy source is required")
	}
	if governedInvocationDependencyMissing(evaluator) {
		return nil, errors.New("invocation Cedar evaluator is required")
	}
	return &PolicyBoundInvocationAuthorizationDecision{source: source, evaluator: evaluator}, nil
}

func (decision *PolicyBoundInvocationAuthorizationDecision) AuthorizeInvocationEffect(
	ctx context.Context,
	request InvocationAuthorizationRequest,
) error {
	if !validInvocationAuthorizationRequest(request) {
		return ErrInvocationAuthorizationInvalid
	}
	scope := InvocationAuthorizationPolicyScope{
		IsolationDomainID: request.IsolationDomainID,
		ServiceID:         request.ServiceID,
		RevisionID:        request.RevisionID,
	}
	policy, err := decision.source.ResolveInvocationAuthorizationPolicy(ctx, scope)
	if err != nil {
		return stableInvocationAuthorizationDependencyError(ctx, err)
	}
	if !validInvocationAuthorizationPolicy(policy, scope) {
		return ErrInvocationAuthorizationPolicyInvalid
	}
	input, err := mapInvocationCedarInput(request)
	if err != nil {
		return err
	}
	input, err = cloneInvocationCedarInput(input)
	if err != nil {
		return ErrInvocationAuthorizationInvalid
	}
	policy = cloneInvocationAuthorizationPolicy(policy)
	if err := decision.evaluator.EvaluateInvocationAuthorization(ctx, policy, input); err != nil {
		if errors.Is(err, ErrInvocationAuthorizationDenied) {
			return ErrInvocationAuthorizationDenied
		}
		return stableInvocationAuthorizationDependencyError(ctx, err)
	}
	return nil
}

func validInvocationAuthorizationPolicy(
	policy InvocationAuthorizationPolicy,
	scope InvocationAuthorizationPolicyScope,
) bool {
	if !validInvocationAuthorizationPolicyScope(scope) ||
		policy.Contract != InvocationAuthorizationPolicyContract ||
		policy.IsolationDomainID != scope.IsolationDomainID ||
		policy.ServiceID != scope.ServiceID ||
		policy.RevisionID != scope.RevisionID ||
		len(policy.PolicySetID) == 0 ||
		len(policy.PolicySetID) > maxInvocationAuthorizationPolicyIDBytes ||
		!invocationAuthorizationPolicyIDPattern.MatchString(policy.PolicySetID) ||
		len(policy.Schema) == 0 ||
		len(policy.Schema) > maxInvocationAuthorizationSchemaBytes ||
		len(policy.Policies) == 0 ||
		len(policy.Policies) > maxInvocationAuthorizationPolicyBytes {
		return false
	}
	return policy.Digest == invocationAuthorizationPolicyDigest(policy.Schema, policy.Policies)
}

func invocationAuthorizationPolicyDigest(schema []byte, policies []byte) [sha256.Size]byte {
	digest := sha256.New()
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(schema)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write(schema)
	binary.BigEndian.PutUint64(size[:], uint64(len(policies)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write(policies)
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func cloneInvocationAuthorizationPolicy(
	policy InvocationAuthorizationPolicy,
) InvocationAuthorizationPolicy {
	policy.Schema = append([]byte(nil), policy.Schema...)
	policy.Policies = append([]byte(nil), policy.Policies...)
	return policy
}

func stableInvocationAuthorizationDependencyError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrInvocationAuthorizationPolicyUnavailable
}

var _ InvocationAuthorizationPolicySource = (*StaticInvocationAuthorizationPolicySource)(nil)

var _ InvocationAuthorizationDecision = (*PolicyBoundInvocationAuthorizationDecision)(nil)
