package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/lifecycle/publication"
	"github.com/asabla/dataground/internal/persistence"
)

type governedPublicationTestDriver struct {
	fakeDriver
	verify func(context.Context, persistence.OperationClaim) error
}

func (driver *governedPublicationTestDriver) PublishClaimed(ctx context.Context, claim persistence.OperationClaim) error {
	return driver.verify(ctx, claim)
}

func TestGovernedPublicationRequiresVerifierAndNeverUsesReferenceEffects(t *testing.T) {
	claim := persistence.OperationClaim{Kind: persistence.OperationKindPublication, IsolationDomainID: "iso_test", ID: "op_test", ResourceID: "rev_test", Command: "publish", ObservedState: "queued", StateMachineVersion: publication.QueuedDevelopmentVersion, DeadlineAt: time.Now().Add(time.Hour)}
	store := newFakeStore(claim)
	fallback := &fakeDriver{}
	if _, err := New(store, fallback, "worker").RunOne(context.Background(), claim.Kind); err == nil || len(store.transitions) != 0 || fallback.applyCount != 0 {
		t.Fatal("missing verifier reached publication")
	}
	var missing *governedPublicationTestDriver
	if _, err := New(newFakeStore(claim), missing, "worker").RunOne(context.Background(), claim.Kind); err == nil {
		t.Fatal("typed-nil verifier accepted")
	}
	store = newFakeStore(claim)
	calls := 0
	driver := &governedPublicationTestDriver{verify: func(_ context.Context, got persistence.OperationClaim) error {
		calls++
		if got.ObservedState != "validating" || got.StateMachineVersion != publication.QueuedDevelopmentVersion || got.ResourceID != claim.ResourceID {
			t.Fatal("verification lost exact claim")
		}
		return persistence.ErrDevelopmentPublicationUnavailable
	}}
	worker := New(store, driver, "worker")
	if _, err := worker.RunOne(context.Background(), claim.Kind); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOne(context.Background(), claim.Kind); err != nil {
		t.Fatal(err)
	}
	if store.retryCount != 1 || calls != 1 || driver.applyCount != 0 || driver.observeCount != 0 || len(store.effects) != 0 || len(store.transitions) != 1 {
		t.Fatal("governed publication used external effect or falsely completed")
	}
	claim.StateMachineVersion = 99
	if _, err := New(newFakeStore(claim), fallback, "worker").RunOne(context.Background(), claim.Kind); err == nil {
		t.Fatal("unknown publication version accepted")
	}
}

type authorizedPublicationTestDriver struct {
	governedPublicationTestDriver
}

func (driver *authorizedPublicationTestDriver) PublishAuthorizedClaimed(ctx context.Context, claim persistence.OperationClaim) error {
	return driver.verify(ctx, claim)
}
func TestAuthorizedPublicationCannotUseOperatorDriver(t *testing.T) {
	claim := persistence.OperationClaim{Kind: persistence.OperationKindPublication, IsolationDomainID: "iso_test", ID: "op_test", ResourceID: "rev_test", Command: "publish", ObservedState: "queued", StateMachineVersion: publication.AuthorizedDevelopmentVersion, DeadlineAt: time.Now().Add(time.Hour)}
	operator := &governedPublicationTestDriver{verify: func(context.Context, persistence.OperationClaim) error {
		t.Fatal("operator verifier called")
		return nil
	}}
	store := newFakeStore(claim)
	if _, err := New(store, operator, "worker").RunOne(context.Background(), claim.Kind); err == nil || len(store.transitions) != 0 {
		t.Fatal("operator driver advanced authorized request")
	}
	var missing *authorizedPublicationTestDriver
	if _, err := New(newFakeStore(claim), missing, "worker").RunOne(context.Background(), claim.Kind); err == nil {
		t.Fatal("typed nil authorized driver admitted")
	}
	calls := 0
	driver := &authorizedPublicationTestDriver{governedPublicationTestDriver: governedPublicationTestDriver{verify: func(_ context.Context, got persistence.OperationClaim) error {
		calls++
		if got.StateMachineVersion != 4 || got.ObservedState != "validating" {
			t.Fatal("lost authorized claim")
		}
		return persistence.ErrDevelopmentPublicationUnavailable
	}}}
	store = newFakeStore(claim)
	worker := New(store, driver, "worker")
	for range 2 {
		if _, err := worker.RunOne(context.Background(), claim.Kind); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 || store.retryCount != 1 || len(store.transitions) != 1 || driver.applyCount != 0 || driver.observeCount != 0 {
		t.Fatal("authorized retry bypassed boundary")
	}
}
