package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/identity"
)

var errQuestionExpiryUnavailable = errors.New("durable question expiry is unavailable")

const (
	questionExpiryBatchSize = 100
	questionExpiryInterval  = 250 * time.Millisecond
	questionExpiryTimeout   = 2 * time.Second
	questionExpiryActor     = "dataground-question-expiry"
)

type questionExpiryStore interface {
	ExpireInvocationRuntimeQuestions(context.Context, string, string, string, int) (int, error)
}

// The expiry owner runs independently of RunOne, which can wait on a native
// turn. PostgreSQL decides which questions are due and commits their terminal
// states, audit and outbox records together; this loop owns only bounded polling.
type questionExpiryOwner struct {
	store             questionExpiryStore
	scope             string
	interval, timeout time.Duration
	cancel            context.CancelFunc
	done              chan struct{}
	mu                sync.Mutex
	lastSuccess       time.Time
	failed            bool
}

func newQuestionExpiryOwner(ctx context.Context, store questionExpiryStore, scope string, interval, timeout time.Duration) (*questionExpiryOwner, error) {
	if ctx == nil || ctx.Err() != nil || store == nil || !isolationDomainIDPattern.MatchString(scope) || interval <= 0 || timeout <= 0 {
		return nil, errQuestionExpiryUnavailable
	}
	switch value := reflect.ValueOf(store); value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return nil, errQuestionExpiryUnavailable
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	owner := &questionExpiryOwner{store: store, scope: scope, interval: interval, timeout: timeout, cancel: cancel, done: make(chan struct{})}
	if err := owner.sweep(ctx); err != nil {
		cancel()
		return nil, err
	}
	go owner.run(ctx)
	return owner, nil
}

func (owner *questionExpiryOwner) sweep(ctx context.Context) error {
	bounded, cancel := context.WithTimeout(ctx, owner.timeout)
	defer cancel()
	count, err := owner.store.ExpireInvocationRuntimeQuestions(bounded, owner.scope, questionExpiryActor, identity.New("cor"), questionExpiryBatchSize)
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if err != nil || bounded.Err() != nil || count < 0 || count > questionExpiryBatchSize {
		owner.failed = true
		return errQuestionExpiryUnavailable
	}
	owner.failed = false
	owner.lastSuccess = time.Now()
	return nil
}

func (owner *questionExpiryOwner) run(ctx context.Context) {
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

func (owner *questionExpiryOwner) Ready() error {
	if owner == nil {
		return errQuestionExpiryUnavailable
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.failed || owner.lastSuccess.IsZero() || time.Since(owner.lastSuccess) > owner.interval+owner.timeout {
		return errQuestionExpiryUnavailable
	}
	return nil
}

func (owner *questionExpiryOwner) Close() {
	if owner == nil {
		return
	}
	owner.cancel()
	<-owner.done
}

type governedWorkerReadiness struct {
	certification runtimeCertificationReadiness
	questions     *questionExpiryOwner
}

func (readiness governedWorkerReadiness) Check(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil || readiness.certification == nil {
		return errQuestionExpiryUnavailable
	}
	if err := readiness.questions.Ready(); err != nil {
		return err
	}
	if err := readiness.certification.Check(ctx); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return errQuestionExpiryUnavailable
	}
	return readiness.questions.Ready()
}
