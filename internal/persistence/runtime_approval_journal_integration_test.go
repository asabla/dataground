package persistence_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestInvocationApprovalResolutionWaitsForInvocationJournalOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fixture := newExpiringApprovalFixture(t, ctx)
	approval := requestExpiringApproval(t, ctx, fixture, 20*time.Second)
	transaction, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback(ctx)
	if _, err := transaction.Exec(ctx, `SELECT id FROM invocations WHERE isolation_domain_id=$1 AND id=$2 FOR UPDATE`, approval.IsolationDomainID, approval.InvocationID); err != nil {
		t.Fatal(err)
	}
	var released atomic.Bool
	premature := errors.New("approval authorization entered before acquiring the invocation journal lock")
	result := make(chan error, 1)
	joined := make(chan struct{})
	go func() {
		defer close(joined)
		_, err := fixture.repository.ResolveInvocationRuntimeApprovalCommand(ctx, testIdempotency(approval.IsolationDomainID, "approval-journal-order"), expiringApprovalResolution(approval), func(context.Context, persistence.InvocationRuntimeApproval) error {
			if !released.Load() {
				return premature
			}
			return nil
		})
		result <- err
	}()
	defer func() {
		cancel()
		transaction.Rollback(context.Background())
		select {
		case <-joined:
		case <-time.After(time.Second):
			t.Error("resolution did not stop")
		}
	}()
	for {
		var blocked int
		if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%FROM invocations%FOR UPDATE%'`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked > 0 {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("resolution did not serialize with runtime journal ownership: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	runtimeEventID := identity.New("evt")
	if _, err := transaction.Exec(ctx, `INSERT INTO invocation_events(isolation_domain_id,invocation_id,id,sequence,schema_version,event_type,occurred_at,recorded_at,correlation_id,actor_id,service_id,revision_id,payload,source_kind,source_sequence)
 SELECT $1,$2,$3,COALESCE(max(sequence),0)+1,'dataground.event/v1','output.text.delta',clock_timestamp(),clock_timestamp(),$4,$5,$6,$7,'{"text":"concurrent output"}','runtime',100 FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2`, approval.IsolationDomainID, approval.InvocationID, runtimeEventID, fixture.claim.CorrelationID, fixture.claim.ActorID, approval.ServiceID, approval.RevisionID); err != nil {
		t.Fatal(err)
	}
	released.Store(true)
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	var runtimeSequence, resolutionSequence uint64
	if err := fixture.pool.QueryRow(ctx, `SELECT sequence FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2 AND id=$3`, approval.IsolationDomainID, approval.InvocationID, runtimeEventID).Scan(&runtimeSequence); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT sequence FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2 AND event_type='interaction.approval.resolved' AND payload->>'approvalId'=$3`, approval.IsolationDomainID, approval.InvocationID, approval.ID).Scan(&resolutionSequence); err != nil {
		t.Fatal(err)
	}
	if resolutionSequence != runtimeSequence+1 {
		t.Fatal("approval journal sequence did not follow the serialized runtime event")
	}
}

func TestConcurrentInvocationApprovalResolutionsShareOneJournalSequence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newExpiringApprovalFixture(t, ctx)
	var approvals []persistence.InvocationRuntimeApproval
	for range 8 {
		approvals = append(approvals, requestExpiringApproval(t, ctx, fixture, 15*time.Second))
	}
	start := make(chan struct{})
	failures := make(chan error, len(approvals))
	var joined sync.WaitGroup
	for index, approval := range approvals {
		joined.Go(func() {
			<-start
			_, err := fixture.repository.ResolveInvocationRuntimeApprovalCommand(ctx, testIdempotency(approval.IsolationDomainID, "concurrent-approval-"+strconv.Itoa(index)), expiringApprovalResolution(approval), func(context.Context, persistence.InvocationRuntimeApproval) error { return nil })
			if err != nil {
				failures <- err
			}
		})
	}
	close(start)
	joined.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var total, distinct, resolved int
	var first, last int64
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*),count(DISTINCT sequence),min(sequence),max(sequence),count(*) FILTER (WHERE event_type='interaction.approval.resolved') FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2`, fixture.target.IsolationDomainID, fixture.target.InvocationID).Scan(&total, &distinct, &first, &last, &resolved); err != nil {
		t.Fatal(err)
	}
	if resolved != len(approvals) || total != distinct || last-first+1 != int64(total) {
		t.Fatal("concurrent approvals collided or lost journal entries")
	}
	for _, approval := range approvals {
		value, err := fixture.repository.GetInvocationRuntimeApproval(ctx, approval.IsolationDomainID, approval.ID)
		if err != nil || value.State != "resolved" || value.Version != 2 || value.Decision != "deny" {
			t.Fatalf("concurrent approval outcome: %v", err)
		}
	}
}

func TestInvocationApprovalResolutionRejectsReplacedOrContendedRuntimeOwnership(t *testing.T) {
	for _, boundary := range []string{"replacement", "operation lock"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			fixture := newExpiringApprovalFixture(t, ctx)
			approval := requestExpiringApproval(t, ctx, fixture, 20*time.Second)
			if boundary == "replacement" {
				if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=LEAST(clock_timestamp(),$3::timestamptz)-interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, fixture.claim.IsolationDomainID, fixture.claim.ID, time.Now()); err != nil {
					t.Fatal(err)
				}
				replacement, err := fixture.repository.ClaimNext(ctx, persistence.OperationKindInvocation, "replacement", time.Minute)
				if err != nil || replacement == nil || replacement.ID != fixture.claim.ID || replacement.FencingToken <= fixture.claim.FencingToken {
					t.Fatalf("replace worker claim: %v", err)
				}
			} else {
				transaction, err := fixture.pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer transaction.Rollback(ctx)
				if _, err := transaction.Exec(ctx, `SELECT id FROM invocation_execution_operations WHERE isolation_domain_id=$1 AND id=$2 FOR UPDATE`, fixture.claim.IsolationDomainID, fixture.claim.ID); err != nil {
					t.Fatal(err)
				}
			}
			authorized := false
			_, err := fixture.repository.ResolveInvocationRuntimeApprovalCommand(ctx, testIdempotency(approval.IsolationDomainID, "approval-owner"), expiringApprovalResolution(approval), func(context.Context, persistence.InvocationRuntimeApproval) error { authorized = true; return nil })
			if !errors.Is(err, persistence.ErrInvocationRuntimeApprovalConflict) || authorized {
				t.Fatalf("unowned approval reached authorization: %v", err)
			}
			value, err := fixture.repository.GetInvocationRuntimeApproval(ctx, approval.IsolationDomainID, approval.ID)
			if err != nil || value.State != "pending" || value.Version != 1 {
				t.Fatal("failed ownership check mutated approval")
			}
		})
	}
}

func TestInvocationApprovalResolutionRechecksLeaseAfterAuthorization(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fixture := newExpiringApprovalFixture(t, ctx)
	approval := requestExpiringApproval(t, ctx, fixture, 20*time.Second)
	var expiry time.Time
	if err := fixture.pool.QueryRow(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()+interval '500 milliseconds' WHERE isolation_domain_id=$1 AND id=$2 RETURNING lease_expires_at`, fixture.claim.IsolationDomainID, fixture.claim.ID).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	authorized := false
	_, err := fixture.repository.ResolveInvocationRuntimeApprovalCommand(ctx, testIdempotency(approval.IsolationDomainID, "approval-lease-expiry"), expiringApprovalResolution(approval), func(context.Context, persistence.InvocationRuntimeApproval) error {
		authorized = true
		_, err := fixture.pool.Exec(ctx, `SELECT pg_sleep(GREATEST(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.01)`, expiry)
		return err
	})
	if !errors.Is(err, persistence.ErrInvocationRuntimeApprovalConflict) || !authorized {
		t.Fatalf("lease expired during authorization: %v", err)
	}
	value, err := fixture.repository.GetInvocationRuntimeApproval(ctx, approval.IsolationDomainID, approval.ID)
	if err != nil || value.State != "pending" || value.Version != 1 || value.Decision != "" {
		t.Fatal("late authorization persisted a resolution")
	}
	var events, audits int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2 AND event_type='interaction.approval.resolved'`, approval.IsolationDomainID, approval.InvocationID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND resource_id=$2 AND action='invocation-approval.resolve'`, approval.IsolationDomainID, approval.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if events != 0 || audits != 0 {
		t.Fatal("late authorization retained resolution evidence")
	}
}
