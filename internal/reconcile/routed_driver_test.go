package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func TestReconcilerPassesExactClaimToClaimBoundRoute(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "invoke", ObservedState: "running",
		StateMachineVersion: 2, DeadlineAt: time.Now().Add(time.Hour),
		CorrelationID: "corr_test", ActorID: "actor_test",
	})
	claimed := &recordingClaimedDriver{}
	driver, err := NewClaimedRoutedDriver(
		&fakeDriver{},
		nil,
		map[EffectRoute]ClaimedEffectDriver{
			{OperationKind: persistence.OperationKindInvocation, Phase: "run-invocation"}: claimed,
		},
	)
	if err != nil {
		t.Fatalf("create claimed routed driver: %v", err)
	}
	worker := New(store, driver, "worker-a")

	ran, err := worker.RunOne(context.Background(), persistence.OperationKindInvocation)
	if err != nil || !ran {
		t.Fatalf("claimed reconciliation = (%t, %v)", ran, err)
	}
	if claimed.applyCount != 1 || claimed.observeCount != 1 {
		t.Fatalf("claimed calls = observe %d, apply %d, want one each", claimed.observeCount, claimed.applyCount)
	}
	if claimed.claim.LeaseOwner != "worker-a" ||
		claimed.claim.FencingToken == 0 ||
		claimed.claim.IsolationDomainID != claimed.effect.IsolationDomainID ||
		claimed.claim.Kind != claimed.effect.OperationKind ||
		claimed.claim.ID != claimed.effect.OperationID {
		t.Fatalf("claimed route received mismatched scope: claim %#v, effect %#v", claimed.claim, claimed.effect)
	}
	if store.claim.ObservedState != "observing" {
		t.Fatalf("state = %q, want observing", store.claim.ObservedState)
	}
}

func TestClaimBoundRouteRejectsClaimlessAndMismatchedCalls(t *testing.T) {
	claimed := &recordingClaimedDriver{}
	driver, err := NewClaimedRoutedDriver(
		&fakeDriver{},
		nil,
		map[EffectRoute]ClaimedEffectDriver{
			{OperationKind: persistence.OperationKindInvocation, Phase: "run-invocation"}: claimed,
		},
	)
	if err != nil {
		t.Fatalf("create claimed routed driver: %v", err)
	}
	effect := persistence.EffectRecord{
		IsolationDomainID: "iso_test",
		OperationKind:     persistence.OperationKindInvocation,
		OperationID:       "op_test",
		Phase:             "run-invocation",
	}
	if _, _, err := driver.Observe(context.Background(), effect); !errors.Is(err, ErrEffectClaimRequired) {
		t.Fatalf("claimless observation error = %v, want claim required", err)
	}
	claim := persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_other",
		ID: "op_test", Command: "invoke", ObservedState: "running",
		LeaseOwner: "worker-a", FencingToken: 1,
	}
	if _, err := driver.ApplyClaimed(context.Background(), claim, effect); !errors.Is(err, ErrEffectClaimMismatch) {
		t.Fatalf("mismatched claim error = %v, want claim mismatch", err)
	}
	if claimed.applyCount != 0 || claimed.observeCount != 0 {
		t.Fatalf("invalid calls reached claimed driver: observe %d, apply %d", claimed.observeCount, claimed.applyCount)
	}
}

func TestClaimedRoutingPreservesOrdinaryRoutes(t *testing.T) {
	ordinary := &fakeDriver{}
	driver, err := NewClaimedRoutedDriver(
		&fakeDriver{},
		map[EffectRoute]EffectDriver{
			{OperationKind: persistence.OperationKindInvocation, Phase: "start-invocation"}: ordinary,
		},
		map[EffectRoute]ClaimedEffectDriver{
			{OperationKind: persistence.OperationKindInvocation, Phase: "run-invocation"}: &recordingClaimedDriver{},
		},
	)
	if err != nil {
		t.Fatalf("create mixed routed driver: %v", err)
	}
	effect := persistence.EffectRecord{
		IsolationDomainID: "iso_test",
		OperationKind:     persistence.OperationKindInvocation,
		OperationID:       "op_test",
		Phase:             "start-invocation",
	}
	if _, _, err := driver.Observe(context.Background(), effect); err != nil {
		t.Fatalf("ordinary route observation: %v", err)
	}
	if ordinary.observeCount != 1 {
		t.Fatalf("ordinary route observations = %d, want one", ordinary.observeCount)
	}
}

func TestClaimedRoutingRejectsOverlappingRouteOwnership(t *testing.T) {
	route := EffectRoute{
		OperationKind: persistence.OperationKindInvocation,
		Phase:         "run-invocation",
	}
	_, err := NewClaimedRoutedDriver(
		&fakeDriver{},
		map[EffectRoute]EffectDriver{route: &fakeDriver{}},
		map[EffectRoute]ClaimedEffectDriver{route: &recordingClaimedDriver{}},
	)
	if err == nil {
		t.Fatal("overlapping ordinary and claim-bound route was accepted")
	}
}

type recordingClaimedDriver struct {
	observeCount int
	applyCount   int
	claim        persistence.OperationClaim
	effect       persistence.EffectRecord
}

func (driver *recordingClaimedDriver) ObserveClaimed(
	_ context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
) (map[string]any, bool, error) {
	driver.observeCount++
	driver.claim = claim
	driver.effect = effect
	return nil, false, nil
}

func (driver *recordingClaimedDriver) ApplyClaimed(
	_ context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
) (map[string]any, error) {
	driver.applyCount++
	driver.claim = claim
	driver.effect = effect
	return map[string]any{"status": "succeeded"}, nil
}
