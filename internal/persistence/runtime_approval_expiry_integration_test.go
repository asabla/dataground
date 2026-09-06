package persistence_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func newExpiringApprovalFixture(t *testing.T, ctx context.Context) *runtimeQuestionFixture {
	t.Helper()
	fixture := newRuntimeQuestionFixture(t, ctx)
	t.Cleanup(func() {
		if _, err := fixture.pool.Exec(context.Background(), `TRUNCATE invocation_runtime_approvals`); err != nil {
			t.Error(err)
		}
	})
	return fixture
}

func requestExpiringApproval(t *testing.T, ctx context.Context, fixture *runtimeQuestionFixture, lifetime time.Duration) persistence.InvocationRuntimeApproval {
	t.Helper()
	fixture.sequence++
	var expiry time.Time
	if err := fixture.pool.QueryRow(ctx, `SELECT clock_timestamp()+$1::interval`, lifetime.String()).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	value, err := fixture.repository.RecordInvocationRuntimeApprovalRequest(ctx, fixture.claim, fixture.effect, fixture.target, persistence.InvocationRuntimeApprovalRequest{SourceSequence: fixture.sequence, RequestedAction: "workspace.change", ExpiresAt: expiry})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func awaitApprovalExpiry(t *testing.T, ctx context.Context, fixture *runtimeQuestionFixture, value persistence.InvocationRuntimeApproval) {
	t.Helper()
	if _, err := fixture.pool.Exec(ctx, `SELECT pg_sleep(GREATEST(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.01)`, value.ExpiresAt); err != nil {
		t.Fatal(err)
	}
}

func expiringApprovalResolution(value persistence.InvocationRuntimeApproval) persistence.InvocationRuntimeApprovalResolution {
	return persistence.InvocationRuntimeApprovalResolution{IsolationDomainID: value.IsolationDomainID, InvocationID: value.InvocationID, ApprovalID: value.ID, ExpectedVersion: 1, Decision: "deny", ActorID: "controller", CorrelationID: identity.New("cor")}
}

func TestInvocationApprovalExpirySurvivesLeaseLossAndNeverRepeatsDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newExpiringApprovalFixture(t, ctx)
	var values []persistence.InvocationRuntimeApproval
	for _, state := range []string{"pending", "resolved", "delivering"} {
		value := requestExpiringApproval(t, ctx, fixture, 2*time.Second)
		if state != "pending" {
			if _, err := fixture.repository.ResolveInvocationRuntimeApproval(ctx, expiringApprovalResolution(value)); err != nil {
				t.Fatal(err)
			}
		}
		if state == "delivering" {
			if _, err := fixture.repository.BeginInvocationRuntimeApprovalDelivery(ctx, fixture.claim, fixture.effect, value.ID, "deny"); err != nil {
				t.Fatal(err)
			}
		}
		values = append(values, value)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp() WHERE isolation_domain_id=$1 AND id=$2`, fixture.claim.IsolationDomainID, fixture.claim.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.CloseInvocationRuntimeApproval(ctx, fixture.claim, fixture.effect, values[0].ID, "cancelled"); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatalf("stale closure: %v", err)
	}
	awaitApprovalExpiry(t, ctx, fixture, values[2])
	if _, err := fixture.repository.ResolveInvocationRuntimeApproval(ctx, expiringApprovalResolution(values[0])); !errors.Is(err, persistence.ErrInvocationRuntimeApprovalExpired) {
		t.Fatalf("expired resolution: %v", err)
	}
	if n, err := fixture.repository.ExpireInvocationRuntimeApprovals(ctx, identity.New("iso"), "worker", identity.New("cor"), 100); err != nil || n != 0 {
		t.Fatalf("cross-domain expiry: %d, %v", n, err)
	}
	locked, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback(ctx)
	if _, err := locked.Exec(ctx, `SELECT id FROM invocation_runtime_approvals WHERE id=$1 FOR UPDATE`, values[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{1, 1, 0} {
		if n, err := fixture.repository.ExpireInvocationRuntimeApprovals(ctx, fixture.claim.IsolationDomainID, "worker", identity.New("cor"), 1); err != nil || n != want {
			t.Fatalf("bounded skip-locked expiry: %d, %v", n, err)
		}
	}
	if err := locked.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := fixture.repository.ExpireInvocationRuntimeApprovals(ctx, fixture.claim.IsolationDomainID, "worker", identity.New("cor"), 100); err != nil || n != 1 {
		t.Fatalf("unlocked expiry: %d, %v", n, err)
	}
	for i, value := range values {
		actual, err := persistence.NewRepository(fixture.pool).GetInvocationRuntimeApproval(ctx, value.IsolationDomainID, value.ID)
		want := "expired"
		if i == 2 {
			want = "delivery_unknown"
		}
		if err != nil || actual.State != want || actual.Version != int64(i+2) || actual.CloseReason != "expired" || actual.ClosedAt.Before(actual.ExpiresAt) {
			t.Fatalf("retained expiry: %s version %d, %v", actual.State, actual.Version, err)
		}
		public, err := fixture.repository.GetInvocationApproval(ctx, value.IsolationDomainID, value.InvocationID, value.ID)
		if err != nil || public.SchemaVersion != domain.InvocationApprovalSchemaV2 || public.ExpiresAt == nil || public.ClosedAt == nil {
			t.Fatalf("expiry projection: %v", err)
		}
		if _, err := fixture.repository.BeginInvocationRuntimeApprovalDelivery(ctx, fixture.claim, fixture.effect, value.ID, "deny"); err == nil {
			t.Fatal("expired delivery repeated")
		}
		var audits, events int
		if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE resource_id=$1 AND action=$2`, value.ID, "invocation-approval."+want).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE aggregate_id=$1 AND event_type=$2`, value.ID, "invocation-approval."+want).Scan(&events); err != nil {
			t.Fatal(err)
		}
		if audits != 1 || events != 1 {
			t.Fatal("expiry lost atomic evidence")
		}
	}
	if n, err := fixture.repository.ExpireInvocationRuntimeApprovals(ctx, fixture.claim.IsolationDomainID, "worker", identity.New("cor"), 100); err != nil || n != 0 {
		t.Fatalf("expiry replay: %d, %v", n, err)
	}
}

func TestInvocationApprovalClosureAndExpiryBounds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newExpiringApprovalFixture(t, ctx)
	for _, state := range []string{"pending", "resolved", "delivering", "delivered"} {
		value := requestExpiringApproval(t, ctx, fixture, 20*time.Second)
		if state != "pending" {
			if _, err := fixture.repository.ResolveInvocationRuntimeApproval(ctx, expiringApprovalResolution(value)); err != nil {
				t.Fatal(err)
			}
		}
		if state == "delivering" || state == "delivered" {
			if _, err := fixture.repository.BeginInvocationRuntimeApprovalDelivery(ctx, fixture.claim, fixture.effect, value.ID, "deny"); err != nil {
				t.Fatal(err)
			}
		}
		if state == "delivered" {
			if _, err := fixture.repository.CompleteInvocationRuntimeApprovalDelivery(ctx, fixture.claim, fixture.effect, value.ID); err != nil {
				t.Fatal(err)
			}
		}
		closed, err := fixture.repository.CloseInvocationRuntimeApproval(ctx, fixture.claim, fixture.effect, value.ID, "runtime-request-cleared")
		want := "closed"
		if state == "delivering" {
			want = "delivery_unknown"
		}
		if state == "delivered" {
			want = "delivered"
		}
		if err != nil || closed.State != want {
			t.Fatalf("close %s: %s, %v", state, closed.State, err)
		}
		replay, err := fixture.repository.CloseInvocationRuntimeApproval(ctx, fixture.claim, fixture.effect, value.ID, "runtime-ended")
		if err != nil || replay.Version != closed.Version || replay.CloseReason != closed.CloseReason {
			t.Fatalf("closure replay changed history: %v", err)
		}
	}
	request := persistence.InvocationRuntimeApprovalRequest{SourceSequence: 99, RequestedAction: "process.execute"}
	value, err := fixture.repository.RecordInvocationRuntimeApprovalRequest(ctx, fixture.claim, fixture.effect, fixture.target, request)
	if err != nil || value.Contract != persistence.InvocationRuntimeApprovalContract || !value.ExpiresAt.Equal(fixture.claim.DeadlineAt) {
		t.Fatalf("default operation deadline: %v", err)
	}
	request.ExpiresAt = value.ExpiresAt.Add(time.Minute)
	if _, err := fixture.repository.RecordInvocationRuntimeApprovalRequest(ctx, fixture.claim, fixture.effect, fixture.target, request); !errors.Is(err, persistence.ErrInvocationRuntimeApprovalConflict) {
		t.Fatalf("extended replay: %v", err)
	}
	request.SourceSequence++
	forged := fixture.claim
	forged.DeadlineAt = request.ExpiresAt
	if _, err := fixture.repository.RecordInvocationRuntimeApprovalRequest(ctx, forged, fixture.effect, fixture.target, request); !errors.Is(err, persistence.ErrInvocationRuntimeApprovalExpired) {
		t.Fatalf("caller deadline extension: %v", err)
	}
	request.ExpiresAt = time.Now().Add(time.Second)
	forgedTarget := fixture.target
	forgedTarget.ServiceID = identity.New("svc")
	if _, err := fixture.repository.RecordInvocationRuntimeApprovalRequest(ctx, fixture.claim, fixture.effect, forgedTarget, request); !errors.Is(err, persistence.ErrInvocationRuntimeApprovalConflict) {
		t.Fatalf("forged target: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_runtime_approvals SET state='resolved',version=2,decision='deny',resolved_by='controller',resolution_correlation_id=$2,resolved_at=clock_timestamp(),expires_at=expires_at+interval '1 second' WHERE id=$1`, value.ID, identity.New("cor")); err == nil {
		t.Fatal("approval expiry was mutable")
	}
	for _, limit := range []int{0, -1, 101} {
		if _, err := fixture.repository.ExpireInvocationRuntimeApprovals(ctx, value.IsolationDomainID, "worker", identity.New("cor"), limit); !errors.Is(err, persistence.ErrInvocationRuntimeApprovalInvalid) {
			t.Fatal("unbounded expiry accepted")
		}
	}
}

func TestInvocationApprovalExpiryRechecksSlowAuthorizationAndRollsBackEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newExpiringApprovalFixture(t, ctx)
	value := requestExpiringApproval(t, ctx, fixture, 500*time.Millisecond)
	_, err := fixture.repository.ResolveInvocationRuntimeApprovalCommand(ctx, testIdempotency(value.IsolationDomainID, "approval-expiry-slow"), expiringApprovalResolution(value), func(context.Context, persistence.InvocationRuntimeApproval) error {
		awaitApprovalExpiry(t, ctx, fixture, value)
		return nil
	})
	if !errors.Is(err, persistence.ErrInvocationRuntimeApprovalExpired) {
		t.Fatalf("late authorization resolved approval: %v", err)
	}
	// Force only the terminal outbox insertion to fail. Its preceding state and
	// audit changes must roll back in the same transaction.
	if _, err := fixture.pool.Exec(ctx, `CREATE FUNCTION reject_test_approval_expiry() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='invocation-approval.expired' THEN RAISE EXCEPTION 'test outbox unavailable'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_test_approval_expiry BEFORE INSERT ON outbox_events FOR EACH ROW EXECUTE FUNCTION reject_test_approval_expiry()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := fixture.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS reject_test_approval_expiry ON outbox_events; DROP FUNCTION IF EXISTS reject_test_approval_expiry()`); err != nil {
			t.Error(err)
		}
	})
	if n, err := fixture.repository.ExpireInvocationRuntimeApprovals(ctx, value.IsolationDomainID, "worker", identity.New("cor"), 100); err == nil || n != 0 {
		t.Fatal("failed outbox accepted expiry")
	}
	actual, err := fixture.repository.GetInvocationRuntimeApproval(ctx, value.IsolationDomainID, value.ID)
	if err != nil || actual.State != "pending" || actual.Version != 1 {
		t.Fatalf("failed expiry mutated approval: %v", err)
	}
	var count int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE resource_id=$1 AND action='invocation-approval.expired'`, value.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed expiry retained audit: %d, %v", count, err)
	}
	if _, err := fixture.pool.Exec(ctx, `DROP TRIGGER reject_test_approval_expiry ON outbox_events; DROP FUNCTION reject_test_approval_expiry()`); err != nil {
		t.Fatal(err)
	}
	if n, err := fixture.repository.ExpireInvocationRuntimeApprovals(ctx, value.IsolationDomainID, "worker", identity.New("cor"), 100); err != nil || n != 1 {
		t.Fatalf("expiry recovery: %d, %v", n, err)
	}
}

func TestInvocationApprovalExpiryMigrationPreservesLegacyRecords(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newExpiringApprovalFixture(t, ctx)
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	defer func() {
		if err := persistence.MigrateUp(context.Background(), database); err != nil {
			t.Error(err)
		}
	}()
	if err := persistence.MigrateDownTo(ctx, database, 53); err != nil {
		t.Fatal(err)
	}
	id := identity.New("apr")
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO invocation_runtime_approvals (contract,isolation_domain_id,id,operation_id,invocation_id,service_id,revision_id,effect_id,source_sequence,requested_action,state,version,created_at,updated_at) VALUES ('dataground.invocation-runtime-approval/v1',$1,$2,$3,$4,$5,$6,$7,99,'process.execute','pending',1,clock_timestamp(),clock_timestamp())`, fixture.claim.IsolationDomainID, id, fixture.claim.ID, fixture.target.InvocationID, fixture.target.ServiceID, fixture.target.RevisionID, fixture.effect.EffectID); err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		t.Fatal(err)
	}
	value, err := fixture.repository.GetInvocationRuntimeApproval(ctx, fixture.claim.IsolationDomainID, id)
	if err != nil || value.Contract != persistence.InvocationRuntimeApprovalLegacyContract || !value.ExpiresAt.IsZero() || value.State != "pending" {
		t.Fatalf("legacy approval changed: %v", err)
	}
	public, err := fixture.repository.GetInvocationApproval(ctx, value.IsolationDomainID, value.InvocationID, id)
	if err != nil || public.SchemaVersion != domain.InvocationApprovalSchemaV1 || public.ExpiresAt != nil {
		t.Fatalf("legacy projection changed: %v", err)
	}
	if n, err := fixture.repository.ExpireInvocationRuntimeApprovals(ctx, value.IsolationDomainID, "worker", identity.New("cor"), 100); err != nil || n != 0 {
		t.Fatalf("invented legacy expiry: %d, %v", n, err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 53); err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		t.Fatal(err)
	}
	requestExpiringApproval(t, ctx, fixture, 20*time.Second)
	if err := persistence.MigrateDownTo(ctx, database, 53); err == nil {
		t.Fatal("downgrade discarded v2 expiry evidence")
	}
}
