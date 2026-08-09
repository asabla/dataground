package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

type approvalRuntimeStore struct {
	InvocationRuntimeStore
}

func (*approvalRuntimeStore) RenewLease(
	_ context.Context,
	claim persistence.OperationClaim,
	_ time.Duration,
) (persistence.OperationClaim, error) {
	return claim, nil
}

type runtimeApprovalStoreStub struct {
	approval          persistence.InvocationRuntimeApproval
	effectiveDecision string
	completed         bool
}

func (store *runtimeApprovalStoreStub) RecordInvocationRuntimeApprovalRequest(
	_ context.Context,
	_ persistence.OperationClaim,
	_ persistence.EffectRecord,
	target persistence.InvocationRuntimeTarget,
	request persistence.InvocationRuntimeApprovalRequest,
) (persistence.InvocationRuntimeApproval, error) {
	store.approval = persistence.InvocationRuntimeApproval{
		Contract:          persistence.InvocationRuntimeApprovalContract,
		IsolationDomainID: target.IsolationDomainID,
		ID:                "apr_00000000000000000001",
		OperationID:       target.OperationID,
		InvocationID:      target.InvocationID,
		ServiceID:         target.ServiceID,
		RevisionID:        target.RevisionID,
		RequestedAction:   request.RequestedAction,
		SourceSequence:    request.SourceSequence,
		State:             "pending",
		Version:           1,
	}
	return store.approval, nil
}

func (store *runtimeApprovalStoreStub) GetInvocationRuntimeApproval(
	context.Context,
	string,
	string,
) (persistence.InvocationRuntimeApproval, error) {
	store.approval.State = "resolved"
	store.approval.Version = 2
	store.approval.Decision = "approve"
	store.approval.ResolvedBy = "actual-controller"
	store.approval.ResolutionCorrelationID = "cor_00000000000000000002"
	return store.approval, nil
}

func (store *runtimeApprovalStoreStub) BeginInvocationRuntimeApprovalDelivery(
	_ context.Context,
	_ persistence.OperationClaim,
	_ persistence.EffectRecord,
	_ string,
	effectiveDecision string,
) (persistence.InvocationRuntimeApproval, error) {
	store.effectiveDecision = effectiveDecision
	store.approval.State = "delivering"
	store.approval.EffectiveDecision = effectiveDecision
	return store.approval, nil
}

func (store *runtimeApprovalStoreStub) CompleteInvocationRuntimeApprovalDelivery(
	context.Context,
	persistence.OperationClaim,
	persistence.EffectRecord,
	string,
) (persistence.InvocationRuntimeApproval, error) {
	store.completed = true
	store.approval.State = "delivered"
	return store.approval, nil
}

type approvalTurnStub struct {
	decisionID string
	decision   dgruntime.ApprovalDecision
	err        error
}

func (*approvalTurnStub) Events() <-chan dgruntime.Event {
	return make(chan dgruntime.Event)
}

func (turn *approvalTurnStub) ResolveApproval(
	_ context.Context,
	id string,
	decision dgruntime.ApprovalDecision,
) error {
	turn.decisionID = id
	turn.decision = decision
	return turn.err
}

func (*approvalTurnStub) Interrupt(context.Context) error { return nil }
func (*approvalTurnStub) Wait(context.Context) error      { return nil }
func (*approvalTurnStub) Close() error                    { return nil }

func TestInvocationRuntimeApprovalDeliveryReauthorizesAndUsesPlatformIdentity(t *testing.T) {
	t.Parallel()
	dependencyFailure := errors.New("approval authorization unavailable")
	deliveryFailure := errors.New("native approval acknowledgement lost")
	tests := []struct {
		name              string
		authorizationErr  error
		deliveryErr       error
		expectedDecision  dgruntime.ApprovalDecision
		expectedEffective string
		expectedCompleted bool
		expectedError     error
	}{
		{
			name:              "allowed approval",
			expectedDecision:  dgruntime.ApprovalApprove,
			expectedEffective: "approve",
			expectedCompleted: true,
		},
		{
			name: "revoked approval fails closed",
			authorizationErr: errors.Join(
				ErrInvocationApprovalDenied,
				ErrInvocationAuthorizationDenied,
			),
			expectedDecision:  dgruntime.ApprovalDeny,
			expectedEffective: "deny",
			expectedCompleted: true,
		},
		{
			name:             "authorization dependency fails before delivery",
			authorizationErr: dependencyFailure,
			expectedError:    dependencyFailure,
		},
		{
			name:              "lost native acknowledgement remains reserved",
			deliveryErr:       deliveryFailure,
			expectedDecision:  dgruntime.ApprovalApprove,
			expectedEffective: "approve",
			expectedError:     deliveryFailure,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runtimeStore := &approvalRuntimeStore{}
			approvalStore := &runtimeApprovalStoreStub{}
			authorizer := &approvalAuthorizerStub{err: test.authorizationErr}
			driver := &InvocationRuntimeDriver{
				store:              runtimeStore,
				approvalStore:      approvalStore,
				approvalAuthorizer: authorizer,
				leaseDuration:      time.Minute,
				renewInterval:      time.Second,
			}
			claim := persistence.OperationClaim{
				Kind:                persistence.OperationKindInvocation,
				IsolationDomainID:   "iso_00000000000000000001",
				ID:                  "op_00000000000000000001",
				ResourceID:          "inv_00000000000000000001",
				Command:             "invoke",
				ObservedState:       "running",
				StateMachineVersion: 2,
				LeaseOwner:          "worker",
				FencingToken:        1,
				DeadlineAt:          time.Now().Add(time.Minute),
			}
			effect := persistence.EffectRecord{
				IsolationDomainID: claim.IsolationDomainID,
				EffectID:          "eff_00000000000000000001",
				OperationKind:     claim.Kind,
				OperationID:       claim.ID,
				Phase:             "run-invocation",
				Status:            "prepared",
			}
			target := persistence.InvocationRuntimeTarget{
				IsolationDomainID: claim.IsolationDomainID,
				OperationID:       claim.ID,
				InvocationID:      claim.ResourceID,
				ServiceID:         "svc_00000000000000000001",
				RevisionID:        "rev_00000000000000000001",
			}
			turn := &approvalTurnStub{err: test.deliveryErr}
			_, err := driver.recordRuntimeEvent(
				context.Background(),
				claim,
				effect,
				target,
				turn,
				dgruntime.Event{
					Sequence: 9,
					Type:     "interaction.approval.requested",
					Payload: map[string]any{
						"approvalId": "approval-1",
						"action":     "workspace.change",
					},
				},
			)
			if !errors.Is(err, test.expectedError) {
				t.Fatalf("approval delivery error = %v, want %v", err, test.expectedError)
			}
			expectedAdapterID := ""
			if test.expectedDecision != "" {
				expectedAdapterID = "approval-1"
			}
			if turn.decisionID != expectedAdapterID ||
				turn.decision != test.expectedDecision ||
				approvalStore.effectiveDecision != test.expectedEffective ||
				approvalStore.completed != test.expectedCompleted ||
				authorizer.phase != InvocationApprovalPhaseEffect ||
				authorizer.approval.ResolvedBy != "actual-controller" {
				t.Fatalf(
					"delivery = turn %#v, approval %#v, authorization %#v",
					turn,
					approvalStore.approval,
					authorizer,
				)
			}
		})
	}
}
