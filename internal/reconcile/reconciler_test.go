package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func TestInvocationSurvivesAmbiguousEffectWithoutRepeatingApply(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "invoke", ObservedState: "queued",
		DeadlineAt: time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	driver := &fakeDriver{ambiguousOnce: true}
	worker := New(store, driver, "worker-a")

	runUntilState(t, worker, store, "succeeded")
	if driver.applyCount != 1 {
		t.Fatalf("external effect applied %d times, want exactly once", driver.applyCount)
	}
	if driver.observeCount < 2 {
		t.Fatalf("external effect observed %d times, want observation before retry", driver.observeCount)
	}
	if store.effect.Status != "succeeded" {
		t.Fatalf("effect status = %q, want succeeded", store.effect.Status)
	}
}

func TestInvocationRecoversWhenEffectReceiptPersistenceFails(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "invoke", ObservedState: "starting",
		DeadlineAt: time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	store.failRecordOnce = true
	driver := &fakeDriver{}
	worker := New(store, driver, "worker-a")

	runUntilState(t, worker, store, "succeeded")
	if driver.applyCount != 1 {
		t.Fatalf("external effect applied %d times after receipt failure, want once", driver.applyCount)
	}
}

func TestInvocationRecoversWhenTransitionCommitFailsAfterEffect(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "invoke", ObservedState: "starting",
		DeadlineAt: time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	store.failAdvanceOnce = true
	driver := &fakeDriver{}
	worker := New(store, driver, "worker-a")

	if _, err := worker.RunOne(context.Background(), persistence.OperationKindInvocation); err == nil {
		t.Fatal("first transition unexpectedly succeeded")
	}
	runUntilState(t, worker, store, "succeeded")
	if driver.applyCount != 1 {
		t.Fatalf("external effect applied %d times after transition failure, want once", driver.applyCount)
	}
}

func TestCancellationReachesStableTerminalStateWithoutStartingEffect(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "cancel", ObservedState: "queued",
		DeadlineAt: time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	driver := &fakeDriver{}
	worker := New(store, driver, "worker-a")

	runUntilState(t, worker, store, "cancelled")
	if driver.applyCount != 0 {
		t.Fatalf("cancellation applied %d start effects, want zero", driver.applyCount)
	}
	ran, err := worker.RunOne(context.Background(), persistence.OperationKindInvocation)
	if err != nil || ran {
		t.Fatalf("poll after terminal cancellation = (%v, %v), want (false, nil)", ran, err)
	}
}

func TestPublicationUsesExplicitFiniteStates(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindPublication, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "rev_test", Command: "publish", ObservedState: "queued",
		DeadlineAt: time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	worker := New(store, &fakeDriver{}, "worker-a")

	runUntilState(t, worker, store, "published")
	want := []string{"validating", "applying", "observing", "published"}
	if len(store.transitions) != len(want) {
		t.Fatalf("transitions = %v, want %v", store.transitions, want)
	}
	for index := range want {
		if store.transitions[index] != want[index] {
			t.Fatalf("transitions = %v, want %v", store.transitions, want)
		}
	}
}

func TestStaleLeaseCannotAdvance(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindPublication, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "rev_test", Command: "publish", ObservedState: "queued",
		DeadlineAt: time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	claim, err := store.ClaimNext(context.Background(), persistence.OperationKindPublication, "worker-a", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	store.claim.FencingToken++
	if err := store.Advance(context.Background(), *claim, "validating", nil); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatalf("stale advance error = %v, want ErrLeaseLost", err)
	}
}

func runUntilState(t *testing.T, worker *Reconciler, store *fakeStore, terminal string) {
	t.Helper()
	for attempt := 0; attempt < 12 && store.claim.ObservedState != terminal; attempt++ {
		ran, err := worker.RunOne(context.Background(), store.claim.Kind)
		if err != nil {
			t.Fatalf("reconcile attempt %d: %v", attempt, err)
		}
		if !ran {
			t.Fatalf("reconcile attempt %d did not claim work", attempt)
		}
	}
	if store.claim.ObservedState != terminal {
		t.Fatalf("state = %q, want %q", store.claim.ObservedState, terminal)
	}
}

type fakeStore struct {
	claim           persistence.OperationClaim
	terminal        bool
	leased          bool
	effect          persistence.EffectRecord
	transitions     []string
	failRecordOnce  bool
	failAdvanceOnce bool
}

func newFakeStore(claim persistence.OperationClaim) *fakeStore { return &fakeStore{claim: claim} }

func (store *fakeStore) ClaimNext(_ context.Context, kind, worker string, _ time.Duration) (*persistence.OperationClaim, error) {
	if store.terminal || kind != store.claim.Kind || store.leased {
		return nil, nil
	}
	store.claim.Attempt++
	store.claim.FencingToken++
	store.claim.LeaseOwner = worker
	store.leased = true
	copy := store.claim
	return &copy, nil
}

func (store *fakeStore) Advance(_ context.Context, claim persistence.OperationClaim, state string, _ map[string]any) error {
	if !store.leased || claim.FencingToken != store.claim.FencingToken || claim.LeaseOwner != store.claim.LeaseOwner {
		return persistence.ErrLeaseLost
	}
	if store.failAdvanceOnce {
		store.failAdvanceOnce = false
		store.leased = false // simulate expiry before a replacement worker claims
		return errors.New("injected transition commit failure")
	}
	store.claim.ObservedState = state
	store.leased = false
	store.transitions = append(store.transitions, state)
	store.terminal = state == "published" || state == "succeeded" || state == "failed" || state == "cancelled"
	return nil
}

func (store *fakeStore) ScheduleRetry(_ context.Context, claim persistence.OperationClaim, _, _ string, _ time.Time) error {
	if !store.leased || claim.FencingToken != store.claim.FencingToken {
		return persistence.ErrLeaseLost
	}
	store.leased = false
	return nil
}

func (store *fakeStore) PrepareEffect(_ context.Context, claim persistence.OperationClaim, phase string, _ [32]byte) (persistence.EffectRecord, error) {
	if store.effect.EffectID == "" {
		store.effect = persistence.EffectRecord{
			IsolationDomainID: claim.IsolationDomainID,
			EffectID:          "eff_test",
			OperationKind:     claim.Kind,
			OperationID:       claim.ID,
			Phase:             phase,
			Status:            "prepared",
		}
	}
	return store.effect, nil
}

func (store *fakeStore) RecordEffect(_ context.Context, _ persistence.EffectRecord, status string, observation map[string]any, _ string) error {
	if store.failRecordOnce {
		store.failRecordOnce = false
		return errors.New("injected effect receipt failure")
	}
	store.effect.Status = status
	store.effect.Observation = observation
	return nil
}

type fakeDriver struct {
	applyCount    int
	observeCount  int
	ambiguousOnce bool
	receipt       map[string]any
}

func (driver *fakeDriver) Observe(_ context.Context, _ persistence.EffectRecord) (map[string]any, bool, error) {
	driver.observeCount++
	return driver.receipt, driver.receipt != nil, nil
}

func (driver *fakeDriver) Apply(_ context.Context, effect persistence.EffectRecord) (map[string]any, error) {
	driver.applyCount++
	driver.receipt = map[string]any{"effectId": effect.EffectID, "status": "succeeded"}
	if driver.ambiguousOnce {
		driver.ambiguousOnce = false
		return nil, ErrAmbiguousEffect
	}
	return driver.receipt, nil
}
