package reconcile

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func TestInvocationSurvivesAmbiguousEffectWithoutRepeatingApply(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "invoke", ObservedState: "queued",
		StateMachineVersion: 2,
		DeadlineAt:          time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	driver := &fakeDriver{ambiguousOnce: true}
	worker := New(store, driver, "worker-a")

	runUntilState(t, worker, store, "succeeded")
	if driver.applyCount != 2 {
		t.Fatalf("external effects applied %d times, want each of two phases exactly once", driver.applyCount)
	}
	if driver.observeCount < 2 {
		t.Fatalf("external effect observed %d times, want observation before retry", driver.observeCount)
	}
	if store.effects["start-invocation"].Status != "succeeded" ||
		store.effects["run-invocation"].Status != "succeeded" {
		t.Fatalf("effect statuses = %#v, want both succeeded", store.effects)
	}
}

func TestInvocationRecoversWhenEffectReceiptPersistenceFails(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "invoke", ObservedState: "starting",
		StateMachineVersion: 2,
		DeadlineAt:          time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	store.failRecordOnce = true
	driver := &fakeDriver{}
	worker := New(store, driver, "worker-a")

	runUntilState(t, worker, store, "succeeded")
	if driver.applyCount != 2 {
		t.Fatalf("external effects applied %d times after receipt failure, want each phase once", driver.applyCount)
	}
}

func TestInvocationRecoversWhenTransitionCommitFailsAfterEffect(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "invoke", ObservedState: "starting",
		StateMachineVersion: 2,
		DeadlineAt:          time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	store.failAdvanceOnce = true
	driver := &fakeDriver{}
	worker := New(store, driver, "worker-a")

	if _, err := worker.RunOne(context.Background(), persistence.OperationKindInvocation); err == nil {
		t.Fatal("first transition unexpectedly succeeded")
	}
	runUntilState(t, worker, store, "succeeded")
	if driver.applyCount != 2 {
		t.Fatalf("external effects applied %d times after transition failure, want each phase once", driver.applyCount)
	}
}

func TestVersionTwoCancellationUsesDurableEffectWithoutStartingRuntime(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "cancel", ObservedState: "cancelling",
		StateMachineVersion: 2,
		DeadlineAt:          time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	driver := &fakeDriver{}
	worker := New(store, driver, "worker-a")

	runUntilState(t, worker, store, "cancelled")
	if driver.applyCount != 1 || store.effects["cancel-invocation"].Status != "succeeded" {
		t.Fatalf("version 2 cancellation effects = apply %d, receipts %#v", driver.applyCount, store.effects)
	}
	if _, started := store.effects["start-invocation"]; started {
		t.Fatalf("cancellation created a start effect: %#v", store.effects)
	}
	ran, err := worker.RunOne(context.Background(), persistence.OperationKindInvocation)
	if err != nil || ran {
		t.Fatalf("poll after terminal cancellation = (%v, %v), want (false, nil)", ran, err)
	}
}

func TestVersionTwoCancellationObservesAmbiguousEffectBeforeCompletion(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "cancel", ObservedState: "cancelling",
		StateMachineVersion: 2,
		DeadlineAt:          time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	driver := &fakeDriver{ambiguousOnce: true}
	worker := New(store, driver, "worker-a")

	runUntilState(t, worker, store, "cancelled")
	if driver.applyCount != 1 || driver.observeCount < 2 {
		t.Fatalf(
			"ambiguous cancellation calls = apply %d, observe %d, want one apply and observation before retry",
			driver.applyCount,
			driver.observeCount,
		)
	}
	if store.effects["cancel-invocation"].Status != "succeeded" {
		t.Fatalf("cancellation receipt = %#v, want succeeded", store.effects["cancel-invocation"])
	}
}

func TestVersionOneCancellationRetainsOriginalEffectlessPath(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "cancel", ObservedState: "queued",
		StateMachineVersion: 1,
		DeadlineAt:          time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	driver := &fakeDriver{}
	worker := New(store, driver, "worker-a")

	runUntilState(t, worker, store, "cancelled")
	if driver.applyCount != 0 || len(store.effects) != 0 {
		t.Fatalf("version 1 cancellation effects = apply %d, receipts %#v", driver.applyCount, store.effects)
	}
	if want := []string{"cancelling", "cancelled"}; !reflect.DeepEqual(store.transitions, want) {
		t.Fatalf("version 1 cancellation transitions = %v, want %v", store.transitions, want)
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

func TestInvocationSeparatesAdmissionFromRuntimeExecution(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "invoke", ObservedState: "queued",
		StateMachineVersion: 2,
		DeadlineAt:          time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	worker := New(store, &fakeDriver{}, "worker-a")

	runUntilState(t, worker, store, "succeeded")
	want := []string{"starting", "running", "observing", "succeeded"}
	if len(store.transitions) != len(want) {
		t.Fatalf("transitions = %v, want %v", store.transitions, want)
	}
	for index := range want {
		if store.transitions[index] != want[index] {
			t.Fatalf("transitions = %v, want %v", store.transitions, want)
		}
	}
}

func TestVersionOneInvocationCompletesThroughItsOriginalSingleEffect(t *testing.T) {
	store := newFakeStore(persistence.OperationClaim{
		Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
		ID: "op_test", ResourceID: "inv_test", Command: "invoke", ObservedState: "queued",
		StateMachineVersion: 1,
		DeadlineAt:          time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
	})
	driver := &fakeDriver{}
	worker := New(store, driver, "worker-a")

	runUntilState(t, worker, store, "succeeded")
	want := []string{"starting", "observing", "succeeded"}
	if !reflect.DeepEqual(store.transitions, want) {
		t.Fatalf("version 1 transitions = %v, want %v", store.transitions, want)
	}
	if driver.applyCount != 1 || store.effects["start-invocation"].Status != "succeeded" {
		t.Fatalf("version 1 effects = apply %d, receipts %#v", driver.applyCount, store.effects)
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

func TestReconcilerRejectsEffectOutsideLifecyclePhase(t *testing.T) {
	claim := persistence.OperationClaim{
		Kind:                persistence.OperationKindInvocation,
		Command:             "invoke",
		ObservedState:       "starting",
		StateMachineVersion: 2,
	}
	driver := &fakeDriver{}
	worker := New(newFakeStore(claim), driver, "worker-a")
	if err := worker.applyEffect(
		context.Background(),
		claim,
		"run-invocation",
		"observing",
	); err == nil {
		t.Fatal("runtime effect was accepted before admission completed")
	}
	if driver.applyCount != 0 || driver.observeCount != 0 {
		t.Fatalf("rejected effect reached driver: apply %d, observe %d", driver.applyCount, driver.observeCount)
	}
}

func TestReconcilerTerminatesRejectedEffectsWithoutRetry(t *testing.T) {
	tests := map[string]struct {
		driver *fakeDriver
		reason persistence.OperationFailureReason
		code   string
	}{
		"denied during apply": {
			driver: &fakeDriver{applyErr: errors.Join(ErrEffectDenied, errors.New("denied"))},
			reason: persistence.OperationFailureEffectDenied,
			code:   "EXTERNAL_EFFECT_DENIED",
		},
		"invalid during observation": {
			driver: &fakeDriver{observeErr: errors.Join(ErrEffectInvalid, errors.New("invalid"))},
			reason: persistence.OperationFailureEffectInvalid,
			code:   "EXTERNAL_EFFECT_INVALID",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore(persistence.OperationClaim{
				Kind: persistence.OperationKindInvocation, IsolationDomainID: "iso_test",
				ID: "op_test", ResourceID: "inv_test", Command: "invoke", ObservedState: "starting",
				StateMachineVersion: 2,
				DeadlineAt:          time.Now().Add(time.Hour), CorrelationID: "corr_test", ActorID: "actor_test",
			})
			worker := New(store, test.driver, "worker-a")
			ran, err := worker.RunOne(context.Background(), persistence.OperationKindInvocation)
			if err != nil || !ran {
				t.Fatalf("rejected effect reconciliation = (%t, %v)", ran, err)
			}
			if store.claim.ObservedState != "failed" || store.failureReason != test.reason {
				t.Fatalf("terminal rejection = state %q, reason %q", store.claim.ObservedState, store.failureReason)
			}
			if store.retryCount != 0 || store.effects["start-invocation"].Status != "failed" ||
				store.effectCodes["start-invocation"] != test.code {
				t.Fatalf(
					"rejection persistence = retries %d, effect %#v, code %q",
					store.retryCount,
					store.effects["start-invocation"],
					store.effectCodes["start-invocation"],
				)
			}
		})
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
	effects         map[string]persistence.EffectRecord
	effectCodes     map[string]string
	transitions     []string
	failRecordOnce  bool
	failAdvanceOnce bool
	failureReason   persistence.OperationFailureReason
	retryCount      int
}

func newFakeStore(claim persistence.OperationClaim) *fakeStore {
	return &fakeStore{
		claim: claim, effects: make(map[string]persistence.EffectRecord), effectCodes: make(map[string]string),
	}
}

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

func (store *fakeStore) Fail(
	ctx context.Context,
	claim persistence.OperationClaim,
	reason persistence.OperationFailureReason,
) error {
	if err := store.Advance(ctx, claim, "failed", nil); err != nil {
		return err
	}
	store.failureReason = reason
	return nil
}

func (store *fakeStore) ScheduleRetry(_ context.Context, claim persistence.OperationClaim, _, _ string, _ time.Time) error {
	if !store.leased || claim.FencingToken != store.claim.FencingToken {
		return persistence.ErrLeaseLost
	}
	store.retryCount++
	store.leased = false
	return nil
}

func (store *fakeStore) PrepareEffect(_ context.Context, claim persistence.OperationClaim, phase string, _ [32]byte) (persistence.EffectRecord, error) {
	effect := store.effects[phase]
	if effect.EffectID == "" {
		effect = persistence.EffectRecord{
			IsolationDomainID: claim.IsolationDomainID,
			EffectID:          "eff_" + phase,
			OperationKind:     claim.Kind,
			OperationID:       claim.ID,
			Phase:             phase,
			Status:            "prepared",
		}
		store.effects[phase] = effect
	}
	return effect, nil
}

func (store *fakeStore) RecordEffect(_ context.Context, effect persistence.EffectRecord, status string, observation map[string]any, errorCode string) error {
	if store.failRecordOnce {
		store.failRecordOnce = false
		return errors.New("injected effect receipt failure")
	}
	effect.Status = status
	effect.Observation = observation
	store.effects[effect.Phase] = effect
	store.effectCodes[effect.Phase] = errorCode
	return nil
}

type fakeDriver struct {
	applyCount    int
	observeCount  int
	ambiguousOnce bool
	observeErr    error
	applyErr      error
	receipts      map[string]map[string]any
}

func (driver *fakeDriver) Observe(_ context.Context, effect persistence.EffectRecord) (map[string]any, bool, error) {
	driver.observeCount++
	if driver.observeErr != nil {
		return nil, false, driver.observeErr
	}
	receipt := driver.receipts[effect.Phase]
	return receipt, receipt != nil, nil
}

func (driver *fakeDriver) Apply(_ context.Context, effect persistence.EffectRecord) (map[string]any, error) {
	driver.applyCount++
	if driver.applyErr != nil {
		return nil, driver.applyErr
	}
	if driver.receipts == nil {
		driver.receipts = make(map[string]map[string]any)
	}
	receipt := map[string]any{"effectId": effect.EffectID, "status": "succeeded"}
	driver.receipts[effect.Phase] = receipt
	if driver.ambiguousOnce {
		driver.ambiguousOnce = false
		return nil, ErrAmbiguousEffect
	}
	return receipt, nil
}
