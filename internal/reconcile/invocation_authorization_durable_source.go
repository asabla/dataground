package reconcile

import (
	"context"
	"errors"

	"github.com/asabla/dataground/internal/persistence"
)

type DurableInvocationAuthorizationPolicyStore interface {
	GetInvocationAuthorizationPolicy(
		context.Context,
		string,
		string,
		string,
	) (persistence.InvocationAuthorizationPolicyRecord, error)
}

type DurableInvocationAuthorizationPolicySource struct {
	store DurableInvocationAuthorizationPolicyStore
}

func NewDurableInvocationAuthorizationPolicySource(
	store DurableInvocationAuthorizationPolicyStore,
) (*DurableInvocationAuthorizationPolicySource, error) {
	if governedInvocationDependencyMissing(store) {
		return nil, errors.New("durable invocation authorization policy store is required")
	}
	return &DurableInvocationAuthorizationPolicySource{store: store}, nil
}

func (source *DurableInvocationAuthorizationPolicySource) ResolveInvocationAuthorizationPolicy(
	ctx context.Context,
	scope InvocationAuthorizationPolicyScope,
) (InvocationAuthorizationPolicy, error) {
	if err := ctx.Err(); err != nil {
		return InvocationAuthorizationPolicy{}, err
	}
	if source == nil || governedInvocationDependencyMissing(source.store) ||
		!validInvocationAuthorizationPolicyScope(scope) {
		return InvocationAuthorizationPolicy{}, ErrInvocationAuthorizationPolicyUnavailable
	}
	record, err := source.store.GetInvocationAuthorizationPolicy(
		ctx,
		scope.IsolationDomainID,
		scope.ServiceID,
		scope.RevisionID,
	)
	if err != nil {
		return InvocationAuthorizationPolicy{}, stableInvocationAuthorizationDependencyError(ctx, err)
	}
	if record.Contract != InvocationAuthorizationPolicyContract ||
		record.IsolationDomainID != scope.IsolationDomainID ||
		record.ServiceID != scope.ServiceID ||
		record.RevisionID != scope.RevisionID {
		return InvocationAuthorizationPolicy{}, ErrInvocationAuthorizationPolicyInvalid
	}
	policy := InvocationAuthorizationPolicy{
		Contract:          record.Contract,
		IsolationDomainID: record.IsolationDomainID,
		ServiceID:         record.ServiceID,
		RevisionID:        record.RevisionID,
		PolicySetID:       record.PolicySetID,
		Schema:            append([]byte(nil), record.Schema...),
		Policies:          append([]byte(nil), record.Policies...),
	}
	if len(record.PolicyDigest) != len(policy.Digest) {
		return InvocationAuthorizationPolicy{}, ErrInvocationAuthorizationPolicyInvalid
	}
	copy(policy.Digest[:], record.PolicyDigest)
	if !validInvocationAuthorizationPolicy(policy, scope) {
		return InvocationAuthorizationPolicy{}, ErrInvocationAuthorizationPolicyInvalid
	}
	return cloneInvocationAuthorizationPolicy(policy), nil
}

var _ InvocationAuthorizationPolicySource = (*DurableInvocationAuthorizationPolicySource)(nil)
