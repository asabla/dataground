package reconcile

import (
	"context"
	"errors"

	"github.com/asabla/dataground/internal/persistence"
)

const (
	InvocationApprovalPhaseEntry  = "entry"
	InvocationApprovalPhaseEffect = "effect"
)

var (
	ErrInvocationApprovalDenied      = errors.New("invocation approval denied")
	ErrInvocationApprovalInvalid     = errors.New("invocation approval is invalid")
	ErrInvocationApprovalUnavailable = errors.New("invocation approval is unavailable")
)

type InvocationApprovalAuthorizer interface {
	AuthorizeInvocationApproval(
		context.Context,
		persistence.InvocationRuntimeApproval,
		string,
	) error
}

type InvocationApprovalStore interface {
	GetInvocationRuntimeApproval(context.Context, string, string) (persistence.InvocationRuntimeApproval, error)
	ResolveInvocationRuntimeApproval(
		context.Context,
		persistence.InvocationRuntimeApprovalResolution,
	) (persistence.InvocationRuntimeApproval, error)
}

type InvocationApprovalResolver struct {
	store      InvocationApprovalStore
	authorizer InvocationApprovalAuthorizer
}

func NewInvocationApprovalResolver(
	store InvocationApprovalStore,
	authorizer InvocationApprovalAuthorizer,
) (*InvocationApprovalResolver, error) {
	if governedInvocationDependencyMissing(store) ||
		governedInvocationDependencyMissing(authorizer) {
		return nil, errors.New("invocation approval resolver dependencies are required")
	}
	return &InvocationApprovalResolver{store: store, authorizer: authorizer}, nil
}

func (resolver *InvocationApprovalResolver) Resolve(
	ctx context.Context,
	resolution persistence.InvocationRuntimeApprovalResolution,
) (persistence.InvocationRuntimeApproval, error) {
	if resolver == nil || ctx == nil {
		return persistence.InvocationRuntimeApproval{}, ErrInvocationApprovalUnavailable
	}
	approval, err := resolver.store.GetInvocationRuntimeApproval(
		ctx, resolution.IsolationDomainID, resolution.ApprovalID,
	)
	if err != nil {
		return persistence.InvocationRuntimeApproval{}, err
	}
	candidate := approval
	candidate.Decision = resolution.Decision
	candidate.ResolvedBy = resolution.ActorID
	candidate.ResolutionCorrelationID = resolution.CorrelationID
	if err := resolver.authorizer.AuthorizeInvocationApproval(
		ctx, candidate, InvocationApprovalPhaseEntry,
	); err != nil {
		return persistence.InvocationRuntimeApproval{}, err
	}
	return resolver.store.ResolveInvocationRuntimeApproval(ctx, resolution)
}

var _ InvocationApprovalAuthorizer = (*InvocationAuthorizer)(nil)
