package reconcile

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
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
	onRead            func()
	onBegin           func()
	keepPending       bool
	lifetime          time.Duration
	recordCalls       int
}

func (store *runtimeApprovalStoreStub) RecordInvocationRuntimeApprovalRequest(
	_ context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
	target persistence.InvocationRuntimeTarget,
	request persistence.InvocationRuntimeApprovalRequest,
) (persistence.InvocationRuntimeApproval, error) {
	store.recordCalls++
	store.approval = persistence.InvocationRuntimeApproval{
		Contract:          persistence.InvocationRuntimeApprovalContract,
		CreatedAt:         time.Now().UTC().Truncate(time.Microsecond),
		ExpiresAt:         claim.DeadlineAt,
		EffectID:          effect.EffectID,
		IsolationDomainID: target.IsolationDomainID,
		ID:                identity.Derived("apr", target.IsolationDomainID+":"+target.OperationID+":"+strconv.FormatUint(request.SourceSequence, 10)),
		OperationID:       target.OperationID,
		InvocationID:      target.InvocationID,
		ServiceID:         target.ServiceID,
		RevisionID:        target.RevisionID,
		RequestedAction:   request.RequestedAction,
		SourceSequence:    request.SourceSequence,
		State:             "pending",
		Version:           1,
	}
	if maximum := store.approval.CreatedAt.Add(15 * time.Minute); store.approval.ExpiresAt.After(maximum) {
		store.approval.ExpiresAt = maximum
	}
	if store.lifetime > 0 {
		store.approval.ExpiresAt = store.approval.CreatedAt.Add(store.lifetime)
	}
	return store.approval, nil
}

func (store *runtimeApprovalStoreStub) CloseInvocationRuntimeApproval(_ context.Context, _ persistence.OperationClaim, _ persistence.EffectRecord, _ string, _ string) (persistence.InvocationRuntimeApproval, error) {
	if store.approval.State == "delivering" {
		store.approval.State = "delivery_unknown"
	} else if store.approval.State != "delivered" {
		store.approval.State = "closed"
	}
	return store.approval, nil
}

func (store *runtimeApprovalStoreStub) GetInvocationRuntimeApproval(
	context.Context,
	string,
	string,
) (persistence.InvocationRuntimeApproval, error) {
	if store.onRead != nil {
		store.onRead()
	}
	if store.keepPending {
		return store.approval, nil
	}
	store.approval.State = "resolved"
	store.approval.Version = 2
	store.approval.Decision = "approve"
	store.approval.ResolvedBy = "actual-controller"
	store.approval.ResolutionCorrelationID = "cor_00000000000000000002"
	store.approval.ResolvedAt = time.Now()
	return store.approval, nil
}

func (store *runtimeApprovalStoreStub) BeginInvocationRuntimeApprovalDelivery(
	_ context.Context,
	_ persistence.OperationClaim,
	_ persistence.EffectRecord,
	_ string,
	effectiveDecision string,
) (persistence.InvocationRuntimeApproval, error) {
	if store.onBegin != nil {
		store.onBegin()
	}
	store.effectiveDecision = effectiveDecision
	store.approval.State = "delivering"
	store.approval.Version = 3
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
	store.approval.Version = 4
	return store.approval, nil
}

type approvalTurnStub struct {
	decisionID string
	decision   dgruntime.ApprovalDecision
	err        error
	deadline   time.Time
	onResolve  func(context.Context)
}

func (*approvalTurnStub) ApprovalPending(ctx context.Context, _ string) (bool, error) {
	return true, ctx.Err()
}

func (*approvalTurnStub) Events() <-chan dgruntime.Event {
	return make(chan dgruntime.Event)
}

func (turn *approvalTurnStub) ResolveApproval(
	ctx context.Context,
	id string,
	decision dgruntime.ApprovalDecision,
) error {
	turn.deadline, _ = ctx.Deadline()
	if turn.onResolve != nil {
		turn.onResolve(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
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
				LeaseExpiresAt:      time.Now().Add(30 * time.Second),
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
			_, err := driveRuntimeApprovalForTest(driver,
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
			if test.expectedDecision != "" && !turn.deadline.Equal(claim.LeaseExpiresAt) {
				t.Fatal("native approval delivery was not bounded to its lease")
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

func TestInvocationRuntimeApprovalDeliveryRechecksReadinessAtEffectBoundaries(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"request", "waiting", "authorization", "reservation", "acknowledgement"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			claim, effect, target := runtimeDriverFixture()
			claim.DeadlineAt = time.Now().Add(time.Minute)
			claim.LeaseExpiresAt = time.Now().Add(30 * time.Second)
			unavailable := errors.New("governed readiness withdrawn")
			ready := boundary != "request"
			store := &runtimeApprovalStoreStub{}
			turn := &approvalTurnStub{}
			if boundary == "waiting" {
				store.keepPending = true
				store.onRead = func() { ready = false }
			}
			if boundary == "reservation" {
				store.onBegin = func() { ready = false }
			}
			if boundary == "acknowledgement" {
				turn.onResolve = func(context.Context) { ready = false }
			}
			authorizer := approvalBoundaryAuthorizer(func(context.Context, persistence.InvocationRuntimeApproval, string) error {
				if boundary == "authorization" {
					ready = false
				}
				return nil
			})
			driver := &InvocationRuntimeDriver{store: &approvalRuntimeStore{}, approvalStore: store, approvalAuthorizer: authorizer, leaseDuration: time.Minute, renewInterval: time.Millisecond,
				readiness: func(context.Context) error {
					if !ready {
						return unavailable
					}
					return nil
				},
			}
			_, err := driveRuntimeApprovalForTest(driver, context.Background(), claim, effect, target, turn, dgruntime.Event{Sequence: 1, Type: "interaction.approval.requested", Payload: map[string]any{"approvalId": "approval-1", "action": "process.execute"}})
			if !errors.Is(err, unavailable) || store.completed {
				t.Fatalf("readiness boundary completed delivery: %v", err)
			}
			wantReserved := boundary == "reservation" || boundary == "acknowledgement"
			if (store.effectiveDecision != "") != wantReserved {
				t.Fatal("readiness failure lost the reservation boundary")
			}
			if (turn.decisionID != "") != (boundary == "acknowledgement") {
				t.Fatal("readiness loss permitted a native write")
			}
		})
	}
}

type approvalBoundaryAuthorizer func(context.Context, persistence.InvocationRuntimeApproval, string) error

func (authorize approvalBoundaryAuthorizer) AuthorizeInvocationApproval(ctx context.Context, approval persistence.InvocationRuntimeApproval, phase string) error {
	return authorize(ctx, approval, phase)
}

func TestInvocationRuntimeApprovalDeliveryCannotOutliveItsClaim(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"expired lease", "lease expires during delivery", "operation expires during delivery", "cancelled after reservation", "approval expires while waiting", "approval expires during delivery"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			claim, effect, target := runtimeDriverFixture()
			claim.DeadlineAt = time.Now().Add(time.Minute)
			claim.LeaseExpiresAt = time.Now().Add(30 * time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			store := &runtimeApprovalStoreStub{}
			turn := &approvalTurnStub{}
			wantErr := error(context.DeadlineExceeded)
			wantDeadline := claim.LeaseExpiresAt
			switch boundary {
			case "approval expires while waiting":
				store.lifetime = 50 * time.Millisecond
				store.keepPending = true
			case "approval expires during delivery":
				store.lifetime = 50 * time.Millisecond
				turn.onResolve = func(ctx context.Context) { <-ctx.Done() }
			case "expired lease":
				claim.LeaseExpiresAt = time.Now().Add(-time.Second)
			case "lease expires during delivery":
				claim.LeaseExpiresAt = time.Now().Add(50 * time.Millisecond)
				wantDeadline = claim.LeaseExpiresAt
				turn.onResolve = func(ctx context.Context) { <-ctx.Done() }
			case "operation expires during delivery":
				claim.DeadlineAt = time.Now().Add(50 * time.Millisecond)
				wantDeadline = claim.DeadlineAt
				turn.onResolve = func(ctx context.Context) { <-ctx.Done() }
			case "cancelled after reservation":
				store.onBegin = cancel
				wantErr = context.Canceled
			}
			driver := &InvocationRuntimeDriver{store: &approvalRuntimeStore{}, approvalStore: store, approvalAuthorizer: &approvalAuthorizerStub{}, leaseDuration: time.Minute, renewInterval: time.Millisecond}
			_, err := driveRuntimeApprovalForTest(driver, ctx, claim, effect, target, turn, dgruntime.Event{Sequence: 1, Type: "interaction.approval.requested", Payload: map[string]any{"approvalId": "approval-1", "action": "process.execute"}})
			if !errors.Is(err, wantErr) || store.completed || turn.decisionID != "" {
				t.Fatalf("expired authority delivered an approval: %v", err)
			}
			if store.lifetime > 0 {
				wantDeadline = store.approval.ExpiresAt
			}
			if !turn.deadline.IsZero() && !turn.deadline.Equal(wantDeadline) {
				t.Fatal("native delivery exceeded the earlier lease or operation deadline")
			}
		})
	}
}

// Existing effect-boundary cases drive the same incremental mediator used by
// runTurn. Stream and terminal behavior is covered by full turn tests.
func driveRuntimeApprovalForTest(driver *InvocationRuntimeDriver, ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, turn dgruntime.ApprovalTurn, event dgruntime.Event) (result persistence.OperationClaim, runErr error) {
	approvals := &invocationRuntimeApprovals{driver: driver, target: target, effect: effect, turn: turn}
	defer func() { runErr = errors.Join(runErr, approvals.close(context.Background(), claim, "runtime-ended")) }()
	if _, err := approvals.record(ctx, claim, event, false); err != nil {
		return claim, err
	}
	ticker := time.NewTicker(driver.renewInterval)
	defer ticker.Stop()
	for approvals.pending != nil {
		if err := approvals.poll(ctx, claim); err != nil {
			return claim, err
		}
		if approvals.pending == nil {
			break
		}
		select {
		case <-ctx.Done():
			return claim, ctx.Err()
		case <-ticker.C:
		}
	}
	return claim, nil
}
