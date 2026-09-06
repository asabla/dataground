package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

type workerOutboxStub struct {
	scopedOutboxStore
	scope                      string
	worker                     string
	duration                   time.Duration
	claim                      *persistence.OutboxClaim
	completes, retries, claims int
}

func (store *workerOutboxStub) ClaimOutboxForIsolationDomain(_ context.Context, scope, worker string, duration time.Duration) (*persistence.OutboxClaim, error) {
	store.claims++
	store.scope = scope
	store.worker = worker
	store.duration = duration
	return store.claim, nil
}
func (store *workerOutboxStub) CompleteOutbox(context.Context, persistence.OutboxClaim) error {
	store.completes++
	return nil
}
func (store *workerOutboxStub) RetryOutbox(context.Context, persistence.OutboxClaim, time.Time) error {
	store.retries++
	return nil
}
func TestGovernedOutboxBindsClaimsAndMutationsToConfiguredDomain(t *testing.T) {
	t.Parallel()
	const scope = "iso_00000000000000000001"
	repository := &workerOutboxStub{claim: &persistence.OutboxClaim{IsolationDomainID: scope}}
	store := workerOutboxStore(repository, workerConfig{mode: workerModeGovernedDevelopment, isolationDomainID: scope})
	claim, err := store.ClaimOutbox(context.Background(), "worker", 30*time.Second)
	if err != nil || claim != repository.claim || repository.scope != scope || repository.worker != "worker" || repository.duration != 30*time.Second {
		t.Fatal("governed outbox lost scope or lease settings")
	}
	if err := store.CompleteOutbox(context.Background(), *claim); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryOutbox(context.Background(), *claim, time.Now()); err != nil {
		t.Fatal(err)
	}
	foreign := *claim
	foreign.IsolationDomainID = "iso_00000000000000000002"
	if !errors.Is(store.CompleteOutbox(context.Background(), foreign), errWorkerOutboxScope) || !errors.Is(store.RetryOutbox(context.Background(), foreign, time.Now()), errWorkerOutboxScope) || repository.completes != 1 || repository.retries != 1 {
		t.Fatal("foreign claim reached outbox mutation")
	}
	repository.claim = &foreign
	if claim, err := store.ClaimOutbox(context.Background(), "worker", time.Second); claim != nil || !errors.Is(err, errWorkerOutboxScope) {
		t.Fatal("foreign claim reached publisher")
	}
	invalid := workerOutboxStore(repository, workerConfig{mode: workerModeGovernedDevelopment})
	before := repository.claims
	if claim, err := invalid.ClaimOutbox(context.Background(), "worker", time.Second); claim != nil || !errors.Is(err, errWorkerOutboxScope) || repository.claims != before {
		t.Fatal("empty governed scope fell back to global claims")
	}
	if workerOutboxStore(repository, workerConfig{mode: workerModeReference}) != repository {
		t.Fatal("reference dispatcher acquired governed scope")
	}
}
