package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func TestDispatcherRetriesThenCompletesWithoutChangingEventIdentity(t *testing.T) {
	store := &fakeStore{claim: persistence.OutboxClaim{
		ID: "out_test", IsolationDomainID: "iso_test", EventType: "test.accepted",
		LeaseOwner: "worker-a", FencingToken: 1,
	}}
	publisher := &fakePublisher{failOnce: true}
	dispatcher := New(store, publisher, "worker-a")

	if ran, err := dispatcher.RunOne(context.Background()); err != nil || !ran {
		t.Fatalf("first dispatch = (%v, %v)", ran, err)
	}
	if !store.retried || store.completed {
		t.Fatalf("first dispatch state = retried:%v completed:%v", store.retried, store.completed)
	}
	if ran, err := dispatcher.RunOne(context.Background()); err != nil || !ran {
		t.Fatalf("second dispatch = (%v, %v)", ran, err)
	}
	if !store.completed || publisher.ids[0] != publisher.ids[1] {
		t.Fatalf("delivery did not preserve event identity: %#v", publisher.ids)
	}
}

type fakeStore struct {
	claim     persistence.OutboxClaim
	retried   bool
	completed bool
}

func (store *fakeStore) ClaimOutbox(_ context.Context, worker string, _ time.Duration) (*persistence.OutboxClaim, error) {
	if store.completed {
		return nil, nil
	}
	store.claim.LeaseOwner = worker
	store.claim.FencingToken++
	copy := store.claim
	return &copy, nil
}

func (store *fakeStore) CompleteOutbox(_ context.Context, claim persistence.OutboxClaim) error {
	if claim.FencingToken != store.claim.FencingToken {
		return persistence.ErrLeaseLost
	}
	store.completed = true
	return nil
}

func (store *fakeStore) RetryOutbox(_ context.Context, claim persistence.OutboxClaim, _ time.Time) error {
	if claim.FencingToken != store.claim.FencingToken {
		return persistence.ErrLeaseLost
	}
	store.retried = true
	return nil
}

type fakePublisher struct {
	failOnce bool
	ids      []string
}

func (publisher *fakePublisher) Publish(_ context.Context, claim persistence.OutboxClaim) error {
	publisher.ids = append(publisher.ids, claim.ID)
	if publisher.failOnce {
		publisher.failOnce = false
		return errors.New("injected delivery failure")
	}
	return nil
}
