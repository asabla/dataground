package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/identity"
)

var errInteractionExpiryUnavailable = errors.New("durable interaction expiry is unavailable")

const (
	interactionExpiryBatchSize = 100
	interactionExpiryInterval  = 250 * time.Millisecond
	interactionExpiryTimeout   = 2 * time.Second
	questionExpiryActor        = "dataground-question-expiry"
	approvalExpiryActor        = "dataground-approval-expiry"
)

type interactionExpiryStore interface {
	ExpireInvocationRuntimeQuestions(context.Context, string, string, string, int) (int, error)
	ExpireInvocationRuntimeApprovals(context.Context, string, string, string, int) (int, error)
}

// The expiry owner runs independently of RunOne, which can wait on a native
// turn. PostgreSQL decides which interactions are due and commits their terminal
// states, audit and outbox records together; this loop owns only bounded polling.
type interactionExpiryOwner struct {
	store             interactionExpiryStore
	scope             string
	interval, timeout time.Duration
	cancel            context.CancelFunc
	done              chan struct{}
	mu                sync.Mutex
	lastSuccess       time.Time
	failed            bool
}

func newInteractionExpiryOwner(ctx context.Context, store interactionExpiryStore, scope string, interval, timeout time.Duration) (*interactionExpiryOwner, error) {
	if ctx == nil || ctx.Err() != nil || store == nil || !isolationDomainIDPattern.MatchString(scope) || interval <= 0 || timeout <= 0 {
		return nil, errInteractionExpiryUnavailable
	}
	switch value := reflect.ValueOf(store); value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return nil, errInteractionExpiryUnavailable
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	owner := &interactionExpiryOwner{store: store, scope: scope, interval: interval, timeout: timeout, cancel: cancel, done: make(chan struct{})}
	if err := owner.sweep(ctx); err != nil {
		cancel()
		return nil, err
	}
	go owner.run(ctx)
	return owner, nil
}

func (owner *interactionExpiryOwner) sweep(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, owner.timeout)
	defer cancel()
	count, err := owner.store.ExpireInvocationRuntimeQuestions(bounded, owner.scope, questionExpiryActor, identity.New("cor"), interactionExpiryBatchSize)
	approvalCount := 0
	if err == nil && bounded.Err() == nil && count >= 0 && count <= interactionExpiryBatchSize {
		approvalCount, err = owner.store.ExpireInvocationRuntimeApprovals(bounded, owner.scope, approvalExpiryActor, identity.New("cor"), interactionExpiryBatchSize)
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if approvalCount < 0 || approvalCount > interactionExpiryBatchSize || err != nil || bounded.Err() != nil || count < 0 || count > interactionExpiryBatchSize {
		owner.failed = true
		return errInteractionExpiryUnavailable
	}
	owner.failed = false
	owner.lastSuccess = time.Now()
	return nil
}

func (owner *interactionExpiryOwner) run(ctx context.Context) {
	defer close(owner.done)
	defer func() { owner.mu.Lock(); owner.failed = true; owner.mu.Unlock() }()
	ticker := time.NewTicker(owner.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			// A later successful batch restores readiness. Retried expiry is safe
			// because a committed terminal transition is never selected again.
			_ = owner.sweep(ctx)
		}
	}
}

func (owner *interactionExpiryOwner) Ready() error {
	if owner == nil {
		return errInteractionExpiryUnavailable
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.failed || owner.lastSuccess.IsZero() || time.Since(owner.lastSuccess) > owner.interval+owner.timeout {
		return errInteractionExpiryUnavailable
	}
	return nil
}

func (owner *interactionExpiryOwner) Close() {
	if owner == nil {
		return
	}
	owner.cancel()
	<-owner.done
}

type governedWorkerReadiness struct {
	certification runtimeCertificationReadiness
	interactions  *interactionExpiryOwner
}

func (readiness governedWorkerReadiness) Check(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil || readiness.certification == nil {
		return errInteractionExpiryUnavailable
	}
	if err := readiness.interactions.Ready(); err != nil {
		return err
	}
	if err := readiness.certification.Check(ctx); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return errInteractionExpiryUnavailable
	}
	return readiness.interactions.Ready()
}
