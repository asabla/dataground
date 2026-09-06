package persistence_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5/pgxpool"
)

func seedOutboxScopeEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scope string, age string) string {
	t.Helper()
	id := identity.New("out")
	if _, err := pool.Exec(ctx, `INSERT INTO outbox_events(id,isolation_domain_id,aggregate_type,aggregate_id,event_type,payload,correlation_id,available_at,created_at) VALUES ($1,$2,'service',$3,'service.created','{}',$4,clock_timestamp()-$5::interval,clock_timestamp()-$5::interval)`, id, scope, identity.New("svc"), identity.New("cor"), age); err != nil {
		t.Fatal(err)
	}
	return id
}
func TestOutboxClaimsStayScopedAndUseDurableLeaseAndRetryState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	scopeA, scopeB := identity.New("iso"), identity.New("iso")
	idB := seedOutboxScopeEvent(t, ctx, pool, scopeB, "2 seconds")
	idA := seedOutboxScopeEvent(t, ctx, pool, scopeA, "1 second")
	for _, scope := range []string{"", "all", "iso_wrong"} {
		if _, err := repository.ClaimOutboxForIsolationDomain(ctx, scope, "worker", time.Second); !errors.Is(err, persistence.ErrOutboxClaimInvalid) {
			t.Fatal("invalid scope selected global work")
		}
	}
	for _, input := range []struct {
		worker   string
		duration time.Duration
	}{{"", time.Second}, {"worker\n", time.Second}, {"worker", 0}, {"worker", time.Nanosecond}, {"worker", -time.Second}, {"worker", time.Hour + time.Second}} {
		if claim, err := repository.ClaimOutboxForIsolationDomain(ctx, scopeA, input.worker, input.duration); claim != nil || !errors.Is(err, persistence.ErrOutboxClaimInvalid) {
			t.Fatal("invalid outbox lease reached persistence")
		}
	}
	claim, err := repository.ClaimOutboxForIsolationDomain(ctx, scopeA, "worker-a", time.Second)
	if err != nil || claim == nil || claim.ID != idA || claim.IsolationDomainID != scopeA {
		t.Fatalf("scoped claim selected older foreign work: %v", err)
	}
	forged := *claim
	forged.IsolationDomainID = scopeB
	if !errors.Is(repository.CompleteOutbox(ctx, forged), persistence.ErrLeaseLost) || !errors.Is(repository.RetryOutbox(ctx, forged, time.Now()), persistence.ErrLeaseLost) {
		t.Fatal("cross-domain completion succeeded")
	}
	if err := repository.CompleteOutbox(ctx, *claim); err != nil {
		t.Fatal(err)
	}
	var foreignAttempts int
	if err := pool.QueryRow(ctx, `SELECT attempt FROM outbox_events WHERE id=$1`, idB).Scan(&foreignAttempts); err != nil || foreignAttempts != 0 {
		t.Fatal("scoped dispatch changed foreign work")
	}
	var wg sync.WaitGroup
	claims := make(chan *persistence.OutboxClaim, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			claim, err := repository.ClaimOutboxForIsolationDomain(ctx, scopeB, "worker-b", time.Minute)
			if err != nil {
				failures <- err
			} else if claim != nil {
				claims <- claim
			}
		})
	}
	wg.Wait()
	close(claims)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("concurrent claim winners: %d", len(claims))
	}
	old := <-claims
	if old.ID != idB {
		t.Fatal("claimed wrong event")
	}
	if _, err := pool.Exec(ctx, `UPDATE outbox_events SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, idB); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(repository.CompleteOutbox(ctx, *old), persistence.ErrLeaseLost) || !errors.Is(repository.RetryOutbox(ctx, *old, time.Now()), persistence.ErrLeaseLost) {
		t.Fatal("expired lease mutated outbox")
	}
	next, err := repository.ClaimOutboxForIsolationDomain(ctx, scopeB, "replacement", time.Minute)
	if err != nil || next == nil || next.FencingToken != old.FencingToken+1 || next.Attempt != 2 {
		t.Fatalf("lease replacement: %v", err)
	}
	if !errors.Is(repository.CompleteOutbox(ctx, *old), persistence.ErrLeaseLost) {
		t.Fatal("replaced lease completed")
	}
	next.Attempt = 20
	if err := repository.RetryOutbox(ctx, *next, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for attempt := 3; attempt <= 20; attempt++ {
		claim, err := repository.ClaimOutboxForIsolationDomain(ctx, scopeB, "replacement", time.Minute)
		if err != nil || claim == nil || claim.Attempt != attempt {
			t.Fatalf("durable retry %d: %v", attempt, err)
		}
		claim.Attempt = 0
		if err := repository.RetryOutbox(ctx, *claim, time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if claim, err := repository.ClaimOutbox(ctx, "reference", time.Minute); err != nil || claim != nil {
		t.Fatalf("dead letter or delivered event reclaimed: %v", err)
	}
}

func TestOutboxClaimRechecksDeliveryAfterWaitingForRowLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	scope := identity.New("iso")
	id := seedOutboxScopeEvent(t, ctx, pool, scope, "1 second")
	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback(ctx)
	if _, err := transaction.Exec(ctx, `UPDATE outbox_events SET status='delivered',delivered_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	result := make(chan *persistence.OutboxClaim, 1)
	failure := make(chan error, 1)
	go func() {
		claim, err := repository.ClaimOutboxForIsolationDomain(ctx, scope, "waiting-worker", time.Second)
		result <- claim
		failure <- err
	}()
	for {
		var blocked int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%UPDATE outbox_events AS event%'`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-failure; err != nil {
		t.Fatal(err)
	}
	if claim := <-result; claim != nil {
		t.Fatal("claim resurrected an event delivered while waiting for its row lock")
	}
}
