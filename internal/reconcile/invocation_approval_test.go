package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
)

type approvalStoreStub struct {
	value       persistence.InvocationRuntimeApproval
	resolveCall persistence.InvocationRuntimeApprovalResolution
}

func (store *approvalStoreStub) GetInvocationRuntimeApproval(
	context.Context,
	string,
	string,
) (persistence.InvocationRuntimeApproval, error) {
	return store.value, nil
}

func (store *approvalStoreStub) ResolveInvocationRuntimeApproval(
	_ context.Context,
	resolution persistence.InvocationRuntimeApprovalResolution,
) (persistence.InvocationRuntimeApproval, error) {
	store.resolveCall = resolution
	value := store.value
	value.State = "resolved"
	value.Decision = resolution.Decision
	value.ResolvedBy = resolution.ActorID
	value.ResolutionCorrelationID = resolution.CorrelationID
	return value, nil
}

func (store *approvalStoreStub) ResolveInvocationRuntimeApprovalCommand(
	ctx context.Context,
	_ persistence.Idempotency,
	resolution persistence.InvocationRuntimeApprovalResolution,
	authorize persistence.InvocationRuntimeApprovalEntryAuthorizer,
) (persistence.CommandResult, error) {
	store.resolveCall = resolution
	candidate := store.value
	candidate.Decision = resolution.Decision
	candidate.ResolvedBy = resolution.ActorID
	candidate.ResolutionCorrelationID = resolution.CorrelationID
	if err := authorize(ctx, candidate); err != nil {
		return persistence.CommandResult{}, err
	}
	return persistence.CommandResult{Status: 200, Body: []byte(`{}`)}, nil
}

type approvalAuthorizerStub struct {
	err      error
	approval persistence.InvocationRuntimeApproval
	phase    string
}

func (authorizer *approvalAuthorizerStub) AuthorizeInvocationApproval(
	_ context.Context,
	approval persistence.InvocationRuntimeApproval,
	phase string,
) error {
	authorizer.approval = approval
	authorizer.phase = phase
	return authorizer.err
}

func TestInvocationApprovalResolverUsesActualPrincipalAtEntry(t *testing.T) {
	t.Parallel()
	store := &approvalStoreStub{value: persistence.InvocationRuntimeApproval{
		Contract:          persistence.InvocationRuntimeApprovalContract,
		IsolationDomainID: "iso_00000000000000000001",
		ID:                "apr_00000000000000000001",
		OperationID:       "op_00000000000000000001",
		InvocationID:      "inv_00000000000000000001",
		ServiceID:         "svc_00000000000000000001",
		RevisionID:        "rev_00000000000000000001",
		RequestedAction:   "workspace.change",
		State:             "pending",
		Version:           1,
	}}
	authorizer := &approvalAuthorizerStub{}
	resolver, err := NewInvocationApprovalResolver(store, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	resolution := persistence.InvocationRuntimeApprovalResolution{
		IsolationDomainID: store.value.IsolationDomainID,
		InvocationID:      store.value.InvocationID,
		ApprovalID:        store.value.ID,
		ExpectedVersion:   1,
		Decision:          "approve",
		ActorID:           "human-controller",
		CorrelationID:     "cor_00000000000000000001",
	}
	resolved, err := resolver.Resolve(context.Background(), resolution)
	if err != nil {
		t.Fatal(err)
	}
	if authorizer.phase != InvocationApprovalPhaseEntry ||
		authorizer.approval.ResolvedBy != resolution.ActorID ||
		authorizer.approval.ResolutionCorrelationID != resolution.CorrelationID ||
		authorizer.approval.Decision != resolution.Decision ||
		store.resolveCall != resolution ||
		resolved.ResolvedBy != resolution.ActorID {
		t.Fatalf("resolution attribution was not preserved: %#v, %#v", authorizer, resolved)
	}
}

func TestInvocationApprovalResolverWithholdsDeniedResolution(t *testing.T) {
	t.Parallel()
	store := &approvalStoreStub{value: persistence.InvocationRuntimeApproval{
		IsolationDomainID: "iso_00000000000000000001",
		ID:                "apr_00000000000000000001",
		OperationID:       "op_00000000000000000001",
		InvocationID:      "inv_00000000000000000001",
		ServiceID:         "svc_00000000000000000001",
		RevisionID:        "rev_00000000000000000001",
		RequestedAction:   "process.execute",
		State:             "pending",
		Version:           1,
	}}
	authorizer := &approvalAuthorizerStub{err: ErrInvocationApprovalDenied}
	resolver, err := NewInvocationApprovalResolver(store, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(context.Background(), persistence.InvocationRuntimeApprovalResolution{
		IsolationDomainID: store.value.IsolationDomainID,
		InvocationID:      store.value.InvocationID,
		ApprovalID:        store.value.ID,
		ExpectedVersion:   1,
		Decision:          "approve",
		ActorID:           "unauthorized-controller",
		CorrelationID:     "cor_00000000000000000001",
	})
	if !errors.Is(err, ErrInvocationApprovalDenied) {
		t.Fatalf("denied resolution = %v", err)
	}
	if store.resolveCall.ApprovalID != "" {
		t.Fatal("denied resolution reached durable state")
	}
}

func TestInvocationApprovalResolverCommandAuthorizesCandidateAtEntry(t *testing.T) {
	t.Parallel()
	store := &approvalStoreStub{value: persistence.InvocationRuntimeApproval{
		IsolationDomainID: "iso_00000000000000000001",
		ID:                "apr_00000000000000000001",
		OperationID:       "op_00000000000000000001",
		InvocationID:      "inv_00000000000000000001",
		ServiceID:         "svc_00000000000000000001",
		RevisionID:        "rev_00000000000000000001",
		RequestedAction:   "workspace.change",
		State:             "pending",
		Version:           1,
	}}
	authorizer := &approvalAuthorizerStub{}
	resolver, err := NewInvocationApprovalResolver(store, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	resolution := persistence.InvocationRuntimeApprovalResolution{
		IsolationDomainID: store.value.IsolationDomainID,
		InvocationID:      store.value.InvocationID,
		ApprovalID:        store.value.ID,
		ExpectedVersion:   1,
		Decision:          "deny",
		ActorID:           "human-controller",
		CorrelationID:     "cor_00000000000000000001",
	}
	result, err := resolver.ResolveCommand(
		context.Background(),
		persistence.Idempotency{IsolationDomainID: store.value.IsolationDomainID},
		resolution,
	)
	if err != nil || result.Status != 200 {
		t.Fatalf("resolve command = (%#v, %v)", result, err)
	}
	if authorizer.phase != InvocationApprovalPhaseEntry ||
		authorizer.approval.InvocationID != resolution.InvocationID ||
		authorizer.approval.Decision != resolution.Decision ||
		authorizer.approval.ResolvedBy != resolution.ActorID ||
		authorizer.approval.ResolutionCorrelationID != resolution.CorrelationID ||
		store.resolveCall != resolution {
		t.Fatalf("command authorization = (%#v, %#v)", authorizer, store.resolveCall)
	}
}

func TestInvocationApprovalResolverCommandReturnsEntryDenial(t *testing.T) {
	t.Parallel()
	store := &approvalStoreStub{value: persistence.InvocationRuntimeApproval{
		IsolationDomainID: "iso_00000000000000000001",
		ID:                "apr_00000000000000000001",
		OperationID:       "op_00000000000000000001",
		InvocationID:      "inv_00000000000000000001",
		ServiceID:         "svc_00000000000000000001",
		RevisionID:        "rev_00000000000000000001",
		RequestedAction:   "process.execute",
		State:             "pending",
		Version:           1,
	}}
	authorizer := &approvalAuthorizerStub{err: ErrInvocationApprovalDenied}
	resolver, err := NewInvocationApprovalResolver(store, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.ResolveCommand(
		context.Background(),
		persistence.Idempotency{IsolationDomainID: store.value.IsolationDomainID},
		persistence.InvocationRuntimeApprovalResolution{
			IsolationDomainID: store.value.IsolationDomainID,
			InvocationID:      store.value.InvocationID,
			ApprovalID:        store.value.ID,
			ExpectedVersion:   1,
			Decision:          "approve",
			ActorID:           "unauthorized-controller",
			CorrelationID:     "cor_00000000000000000001",
		},
	)
	if !errors.Is(err, ErrInvocationApprovalDenied) {
		t.Fatalf("denied command resolution = %v", err)
	}
}
