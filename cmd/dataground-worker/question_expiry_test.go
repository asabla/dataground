package main

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type expiryStoreFunc func(context.Context, string, string, string, int) (int, error)

func (f expiryStoreFunc) ExpireInvocationRuntimeQuestions(ctx context.Context, scope, actor, correlation string, limit int) (int, error) {
	return f(ctx, scope, actor, correlation, limit)
}

type expiryCertificationFunc func(context.Context) error

func (f expiryCertificationFunc) Check(ctx context.Context) error { return f(ctx) }

const expiryTestScope = "iso_00000000000000000001"

func TestQuestionExpiryOwnerRequiresAnInitialSuccessfulBoundedBatch(t *testing.T) {
	t.Parallel()
	for _, result := range []struct {
		count int
		err   error
	}{{0, errors.New("private database detail")}, {-1, nil}, {101, nil}} {
		owner, err := newQuestionExpiryOwner(context.Background(), expiryStoreFunc(func(context.Context, string, string, string, int) (int, error) { return result.count, result.err }), expiryTestScope, time.Second, time.Second)
		if owner != nil || err != errQuestionExpiryUnavailable {
			t.Fatalf("unsafe expiry startup: %v", err)
		}
	}
	var calls atomic.Int32
	store := expiryStoreFunc(func(context.Context, string, string, string, int) (int, error) { calls.Add(1); return 0, nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		ctx               context.Context
		store             questionExpiryStore
		scope             string
		interval, timeout time.Duration
	}{
		{nil, store, expiryTestScope, time.Second, time.Second}, {ctx, store, expiryTestScope, time.Second, time.Second},
		{context.Background(), expiryStoreFunc(nil), expiryTestScope, time.Second, time.Second}, {context.Background(), store, "another-domain", time.Second, time.Second},
		{context.Background(), store, expiryTestScope, 0, time.Second}, {context.Background(), store, expiryTestScope, time.Second, 0},
	} {
		if owner, err := newQuestionExpiryOwner(test.ctx, test.store, test.scope, test.interval, test.timeout); owner != nil || err != errQuestionExpiryUnavailable {
			t.Fatal("invalid expiry owner accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid configuration reached expiry store")
	}
}

func TestQuestionExpiryOwnerSerializesScopedBatchesAndJoinsShutdown(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32
	entered := make(chan struct{})
	var mu sync.Mutex
	correlations := map[string]bool{}
	store := expiryStoreFunc(func(ctx context.Context, scope, actor, correlation string, limit int) (int, error) {
		concurrent := active.Add(1)
		defer active.Add(-1)
		if concurrent > maximum.Load() {
			maximum.Store(concurrent)
		}
		if scope != expiryTestScope || actor != questionExpiryActor || limit != 100 || !regexp.MustCompile(`^cor_[0-9a-z]{20,32}$`).MatchString(correlation) {
			t.Error("expiry batch lost closed scope or attribution")
		}
		mu.Lock()
		if correlations[correlation] {
			t.Error("expiry batch reused correlation")
		}
		correlations[correlation] = true
		mu.Unlock()
		deadline, found := ctx.Deadline()
		if !found || time.Until(deadline) > time.Second {
			t.Error("expiry batch has no bounded deadline")
		}
		if calls.Add(1) == 1 {
			return 100, nil
		}
		close(entered)
		<-ctx.Done()
		return 0, ctx.Err()
	})
	owner, err := newQuestionExpiryOwner(context.Background(), store, expiryTestScope, time.Millisecond, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("independent expiry polling did not run")
	}
	owner.Close()
	owner.Close()
	if maximum.Load() != 1 || active.Load() != 0 || calls.Load() != 2 || owner.Ready() != errQuestionExpiryUnavailable {
		t.Fatal("expiry did not stop and join its single store call")
	}
}

func TestQuestionExpiryOwnerWithholdsReadinessOnFailureAndRecovers(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var fail atomic.Bool
	store := expiryStoreFunc(func(context.Context, string, string, string, int) (int, error) {
		calls.Add(1)
		if fail.Load() {
			return 0, errors.New("database unavailable")
		}
		return 0, nil
	})
	owner, err := newQuestionExpiryOwner(context.Background(), store, expiryTestScope, 5*time.Millisecond, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	certificationErr := errors.New("certification expired")
	readiness := governedWorkerReadiness{questions: owner, certification: expiryCertificationFunc(func(context.Context) error { return certificationErr })}
	if readiness.Check(context.Background()) != certificationErr {
		t.Fatal("expiry readiness bypassed certification")
	}
	fail.Store(true)
	awaitExpiryCondition(t, func() bool { return owner.Ready() == errQuestionExpiryUnavailable })
	if readiness.Check(context.Background()) != errQuestionExpiryUnavailable {
		t.Fatal("failed expiry allowed a governed boundary")
	}
	fail.Store(false)
	awaitExpiryCondition(t, func() bool { return owner.Ready() == nil })
	if calls.Load() < 3 || readiness.Check(context.Background()) != certificationErr {
		t.Fatal("recovery did not retain independent certification denial")
	}
	resources := &workerResources{readiness: readiness, questionExpiry: owner}
	if resources.Ready(context.Background()) != certificationErr {
		t.Fatal("worker resources lost combined readiness")
	}
	if err := resources.Close(); err != nil {
		t.Fatal(err)
	}
	if resources.Ready(context.Background()) != errQuestionExpiryUnavailable {
		t.Fatal("closed resources remained ready")
	}
	if (&workerResources{}).Ready(context.Background()) != nil {
		t.Fatal("reference resources acquired question requirements")
	}
}

func TestQuestionExpiryOwnerRejectsExpiredBatchAndStaleProgress(t *testing.T) {
	t.Parallel()
	store := expiryStoreFunc(func(ctx context.Context, _, _, _ string, _ int) (int, error) { <-ctx.Done(); return 0, nil })
	owner, err := newQuestionExpiryOwner(context.Background(), store, expiryTestScope, time.Millisecond, 5*time.Millisecond)
	if owner != nil || err != errQuestionExpiryUnavailable {
		t.Fatal("late success started expiry owner")
	}
	stale := &questionExpiryOwner{interval: time.Millisecond, timeout: time.Millisecond, lastSuccess: time.Now().Add(-time.Second)}
	if stale.Ready() != errQuestionExpiryUnavailable {
		t.Fatal("stale expiry progress remained ready")
	}
}

func TestGovernedReadinessRechecksExpiryAfterCertification(t *testing.T) {
	t.Parallel()
	var fail atomic.Bool
	store := expiryStoreFunc(func(context.Context, string, string, string, int) (int, error) {
		if fail.Load() {
			return 0, errors.New("unavailable")
		}
		return 0, nil
	})
	owner, err := newQuestionExpiryOwner(context.Background(), store, expiryTestScope, 5*time.Millisecond, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	readiness := governedWorkerReadiness{questions: owner, certification: expiryCertificationFunc(func(context.Context) error {
		fail.Store(true)
		awaitExpiryCondition(t, func() bool { return owner.Ready() != nil })
		return nil
	})}
	if readiness.Check(context.Background()) != errQuestionExpiryUnavailable {
		t.Fatal("expiry failed during certification but effect remained ready")
	}
}

func awaitExpiryCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal("expiry condition did not become true")
		case <-ticker.C:
		}
	}
}
