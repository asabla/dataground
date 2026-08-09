package reconcile

import (
	"context"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

type invocationAuthorizationDecisionFunc func(context.Context, InvocationAuthorizationRequest) error

func (decision invocationAuthorizationDecisionFunc) AuthorizeInvocationEffect(
	ctx context.Context,
	request InvocationAuthorizationRequest,
) error {
	return decision(ctx, request)
}

type nilInvocationAuthorizationDecision struct{}

func (*nilInvocationAuthorizationDecision) AuthorizeInvocationEffect(
	context.Context,
	InvocationAuthorizationRequest,
) error {
	return nil
}

func TestInvocationAuthorizerMapsGovernedPhases(t *testing.T) {
	t.Parallel()

	requests := make([]InvocationAuthorizationRequest, 0, 4)
	authorizer, err := NewInvocationAuthorizer(invocationAuthorizationDecisionFunc(func(
		_ context.Context,
		request InvocationAuthorizationRequest,
	) error {
		requests = append(requests, request)
		return nil
	}))
	if err != nil {
		t.Fatalf("construct invocation authorizer: %v", err)
	}
	admission := persistence.InvocationAdmissionTarget{
		IsolationDomainID: "iso_1", OperationID: "op_1", InvocationID: "inv_1",
		ServiceID: "svc_1", RevisionID: "rev_1", ActorID: "actor_1", CorrelationID: "corr_1",
	}
	if err := authorizer.AuthorizeInvocationAdmission(context.Background(), admission); err != nil {
		t.Fatalf("authorize admission: %v", err)
	}
	runtimeRequest := dgruntime.StartRequest{
		Prompt: "produce the result",
		OutputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"result": map[string]any{"type": "string"}},
		},
		Artifacts: []dgruntime.ArtifactDeclaration{{
			ID: "report", Name: "report.txt", SandboxPath: "/workspace/report.txt",
			MediaType: "text/plain", Kind: "file",
		}},
		ApprovalMode: dgruntime.ApprovalLocked,
		SandboxMode:  dgruntime.SandboxWorkspaceWrite,
	}
	runtimeTarget := persistence.InvocationRuntimeTarget{
		IsolationDomainID: "iso_1", OperationID: "op_1", InvocationID: "inv_1",
		ServiceID: "svc_1", RevisionID: "rev_1", ActorID: "actor_1", CorrelationID: "corr_1",
	}
	if err := authorizer.AuthorizeInvocationRuntime(context.Background(), runtimeTarget, runtimeRequest); err != nil {
		t.Fatalf("authorize runtime: %v", err)
	}
	approval := persistence.InvocationRuntimeApproval{
		IsolationDomainID:       "iso_1",
		ID:                      "apr_00000000000000000001",
		OperationID:             "op_1",
		InvocationID:            "inv_1",
		ServiceID:               "svc_1",
		RevisionID:              "rev_1",
		RequestedAction:         "workspace.change",
		Decision:                "approve",
		ResolvedBy:              "actual-controller",
		ResolutionCorrelationID: "corr_approval",
	}
	if err := authorizer.AuthorizeInvocationApproval(
		context.Background(), approval, InvocationApprovalPhaseEntry,
	); err != nil {
		t.Fatalf("authorize approval: %v", err)
	}
	cancellation := persistence.InvocationCancellationTarget{
		IsolationDomainID: "iso_1", OperationID: "op_1", InvocationID: "inv_1",
		ServiceID: "svc_1", RevisionID: "rev_1", ActorID: "actor_2", CorrelationID: "corr_2",
	}
	if err := authorizer.AuthorizeInvocationCancellation(context.Background(), cancellation); err != nil {
		t.Fatalf("authorize cancellation: %v", err)
	}
	if len(requests) != 4 {
		t.Fatalf("authorization requests = %d, want 4", len(requests))
	}
	if requests[0].Action != InvocationAuthorizationAdmit || requests[0].Runtime != nil {
		t.Fatalf("admission authorization = %#v", requests[0])
	}
	if requests[1].Action != InvocationAuthorizationRun || requests[1].Runtime == nil {
		t.Fatalf("runtime authorization = %#v", requests[1])
	}
	if requests[2].Action != InvocationAuthorizationApprove ||
		requests[2].Approval == nil ||
		requests[2].ActorID != "actual-controller" ||
		requests[2].CorrelationID != "corr_approval" ||
		requests[2].Approval.ID != approval.ID ||
		requests[2].Approval.Phase != InvocationApprovalPhaseEntry {
		t.Fatalf("approval authorization = %#v", requests[2])
	}
	if requests[3].Action != InvocationAuthorizationCancel || requests[3].Runtime != nil ||
		requests[3].ActorID != "actor_2" || requests[3].CorrelationID != "corr_2" {
		t.Fatalf("cancellation authorization = %#v", requests[3])
	}
	if requests[1].Runtime == &runtimeRequest {
		t.Fatal("runtime authorization retained the caller request address")
	}
	requests[1].Runtime.Artifacts[0].Name = "mutated"
	requests[1].Runtime.OutputSchema["type"] = "array"
	properties := requests[1].Runtime.OutputSchema["properties"].(map[string]any)
	properties["result"].(map[string]any)["type"] = "number"
	if runtimeRequest.Artifacts[0].Name != "report.txt" ||
		runtimeRequest.OutputSchema["type"] != "object" ||
		runtimeRequest.OutputSchema["properties"].(map[string]any)["result"].(map[string]any)["type"] != "string" {
		t.Fatal("decision mutation changed the normalized runtime request")
	}
}

func TestInvocationAuthorizerMapsStableDenials(t *testing.T) {
	t.Parallel()

	authorizer, err := NewInvocationAuthorizer(invocationAuthorizationDecisionFunc(func(
		context.Context,
		InvocationAuthorizationRequest,
	) error {
		return ErrInvocationAuthorizationDenied
	}))
	if err != nil {
		t.Fatalf("construct invocation authorizer: %v", err)
	}
	admission := persistence.InvocationAdmissionTarget{
		IsolationDomainID: "iso_1", OperationID: "op_1", InvocationID: "inv_1",
		ServiceID: "svc_1", RevisionID: "rev_1", ActorID: "actor_1", CorrelationID: "corr_1",
	}
	if err := authorizer.AuthorizeInvocationAdmission(context.Background(), admission); !errors.Is(
		err,
		ErrInvocationAdmissionDenied,
	) || !errors.Is(err, ErrInvocationAuthorizationDenied) {
		t.Fatalf("admission denial = %v", err)
	}
	runtimeTarget := persistence.InvocationRuntimeTarget{
		IsolationDomainID: "iso_1", OperationID: "op_1", InvocationID: "inv_1",
		ServiceID: "svc_1", RevisionID: "rev_1", ActorID: "actor_1", CorrelationID: "corr_1",
	}
	if err := authorizer.AuthorizeInvocationRuntime(
		context.Background(),
		runtimeTarget,
		dgruntime.StartRequest{},
	); !errors.Is(err, ErrInvocationRuntimeDenied) {
		t.Fatalf("runtime denial = %v", err)
	}
	cancellation := persistence.InvocationCancellationTarget{
		IsolationDomainID: "iso_1", OperationID: "op_1", InvocationID: "inv_1",
		ServiceID: "svc_1", RevisionID: "rev_1", ActorID: "actor_1", CorrelationID: "corr_1",
	}
	if err := authorizer.AuthorizeInvocationCancellation(
		context.Background(),
		cancellation,
	); !errors.Is(err, ErrInvocationCancellationDenied) {
		t.Fatalf("cancellation denial = %v", err)
	}
}

func TestInvocationAuthorizerFailsClosedBeforeDecision(t *testing.T) {
	t.Parallel()

	calls := 0
	authorizer, err := NewInvocationAuthorizer(invocationAuthorizationDecisionFunc(func(
		context.Context,
		InvocationAuthorizationRequest,
	) error {
		calls++
		return nil
	}))
	if err != nil {
		t.Fatalf("construct invocation authorizer: %v", err)
	}
	if err := authorizer.AuthorizeInvocationAdmission(
		context.Background(),
		persistence.InvocationAdmissionTarget{},
	); !errors.Is(err, ErrInvocationAuthorizationInvalid) {
		t.Fatalf("invalid admission authorization = %v", err)
	}
	if calls != 0 {
		t.Fatalf("decision calls = %d, want 0", calls)
	}

	var typedNil *nilInvocationAuthorizationDecision
	if _, err := NewInvocationAuthorizer(typedNil); err == nil {
		t.Fatal("typed-nil decision was accepted")
	}
}
