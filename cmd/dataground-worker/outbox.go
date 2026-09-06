package main

import (
	"context"
	"errors"
	"time"

	"github.com/asabla/dataground/internal/outbox"
	"github.com/asabla/dataground/internal/persistence"
)

var errWorkerOutboxScope = errors.New("outbox claim is outside the configured isolation domain")

type scopedOutboxStore interface {
	outbox.Store
	ClaimOutboxForIsolationDomain(context.Context, string, string, time.Duration) (*persistence.OutboxClaim, error)
}

type isolationScopedOutboxStore struct {
	repository scopedOutboxStore
	scope      string
}

func workerOutboxStore(repository scopedOutboxStore, config workerConfig) outbox.Store {
	if config.mode == workerModeGovernedDevelopment {
		return &isolationScopedOutboxStore{repository: repository, scope: config.isolationDomainID}
	}
	return repository
}
func (store *isolationScopedOutboxStore) ClaimOutbox(ctx context.Context, worker string, duration time.Duration) (*persistence.OutboxClaim, error) {
	if !isolationDomainIDPattern.MatchString(store.scope) {
		return nil, errWorkerOutboxScope
	}
	claim, err := store.repository.ClaimOutboxForIsolationDomain(ctx, store.scope, worker, duration)
	if err != nil {
		return nil, err
	}
	if claim != nil && claim.IsolationDomainID != store.scope {
		return nil, errWorkerOutboxScope
	}
	return claim, nil
}
func (store *isolationScopedOutboxStore) CompleteOutbox(ctx context.Context, claim persistence.OutboxClaim) error {
	if !isolationDomainIDPattern.MatchString(store.scope) || claim.IsolationDomainID != store.scope {
		return errWorkerOutboxScope
	}
	return store.repository.CompleteOutbox(ctx, claim)
}
func (store *isolationScopedOutboxStore) RetryOutbox(ctx context.Context, claim persistence.OutboxClaim, due time.Time) error {
	if !isolationDomainIDPattern.MatchString(store.scope) || claim.IsolationDomainID != store.scope {
		return errWorkerOutboxScope
	}
	return store.repository.RetryOutbox(ctx, claim, due)
}
