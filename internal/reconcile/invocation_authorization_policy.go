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
		InvocationAuthorizationRequest,
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
	ownedRequest, err := clonePolicyBoundInvocationAuthorizationRequest(request)
	if err != nil {
		return err
	}
	policy = cloneInvocationAuthorizationPolicy(policy)
	if err := decision.evaluator.EvaluateInvocationAuthorization(ctx, policy, ownedRequest); err != nil {
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
	if policy.Contract != InvocationAuthorizationPolicyContract ||
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

func clonePolicyBoundInvocationAuthorizationRequest(
	request InvocationAuthorizationRequest,
) (InvocationAuthorizationRequest, error) {
	owned := request
	if request.Runtime == nil {
		return owned, nil
	}
	runtimeRequest, err := cloneInvocationAuthorizationRuntime(*request.Runtime)
	if err != nil {
		return InvocationAuthorizationRequest{}, err
	}
	owned.Runtime = &runtimeRequest
	return owned, nil
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

var _ InvocationAuthorizationDecision = (*PolicyBoundInvocationAuthorizationDecision)(nil)
