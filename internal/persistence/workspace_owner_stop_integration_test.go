package persistence_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testWorkspaceOwnerStop(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claim persistence.OperationClaim, effect persistence.EffectRecord, ref execution.ExecutionRef) {
	t.Helper()
	repo := persistence.NewRepository(pool)
	entry, err := repo.BeginInvocationWorkspaceQuiescence(ctx, claim, effect, ref, "sha256:"+strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetInvocationWorkspaceOwnerStop(ctx, claim.IsolationDomainID, claim.ID); err == nil {
		t.Fatal("intent alone supplied a stop receipt")
	}
	if _, err := repo.AcceptCancellation(ctx, testIdempotency(claim.IsolationDomainID, "owner-stop-cancel"), persistence.AcceptCancellationInput{InvocationID: claim.ResourceID, ActorID: "cancelling-user", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CompleteInvocationWorkspaceQuiescence(ctx, claim, effect, entry.Record); err == nil {
		t.Fatal("cancelled execution retained its completion authority")
	}
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET deadline_at=clock_timestamp()-interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, claim.IsolationDomainID, claim.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO invocation_workspace_owner_stop_receipts(isolation_domain_id,operation_id,contract,owner_binding,runtime_token,actor_id,correlation_id) SELECT isolation_domain_id,operation_id,$3,$4,runtime_token,actor_id,correlation_id FROM invocation_workspace_quiescence_intents WHERE isolation_domain_id=$1 AND operation_id=$2`, claim.IsolationDomainID, claim.ID, persistence.WorkspaceOwnerStopContract, "sha256:"+strings.Repeat("e", 64)); err == nil {
		t.Fatal("database accepted another connection's stop receipt")
	}
	for name, mutate := range map[string]func(*persistence.InvocationWorkspaceQuiescence){
		"scope":     func(r *persistence.InvocationWorkspaceQuiescence) { r.Source.IsolationDomainID = identity.New("iso") },
		"operation": func(r *persistence.InvocationWorkspaceQuiescence) { r.OperationID = identity.New("op") },
		"execution": func(r *persistence.InvocationWorkspaceQuiescence) { r.ExecutionID = identity.New("exe") },
		"owner":     func(r *persistence.InvocationWorkspaceQuiescence) { r.RuntimeOwner = "replacement" },
		"token":     func(r *persistence.InvocationWorkspaceQuiescence) { r.RuntimeToken++ },
		"connection": func(r *persistence.InvocationWorkspaceQuiescence) {
			r.OwnerBinding = "sha256:" + strings.Repeat("d", 64)
		},
		"source":      func(r *persistence.InvocationWorkspaceQuiescence) { r.Source.ID = identity.New("src") },
		"actor":       func(r *persistence.InvocationWorkspaceQuiescence) { r.ActorID = "cancelling-user" },
		"correlation": func(r *persistence.InvocationWorkspaceQuiescence) { r.CorrelationID = identity.New("cor") },
	} {
		t.Run(name, func(t *testing.T) {
			r := entry.Record
			mutate(&r)
			if err := repo.RecordInvocationWorkspaceOwnerStop(ctx, r); err == nil {
				t.Fatal("substituted original-owner observation accepted")
			}
		})
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_stop_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='workspace.owner-stop.recorded' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_stop_audit BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_stop_audit()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_stop_audit() CASCADE`)
	if err := repo.RecordInvocationWorkspaceOwnerStop(ctx, entry.Record); err == nil {
		t.Fatal("receipt survived failed audit")
	}
	if _, err := repo.GetInvocationWorkspaceOwnerStop(ctx, claim.IsolationDomainID, claim.ID); err == nil {
		t.Fatal("rolled back receipt remained")
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_stop_audit() CASCADE`); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan error, 6)
	for range 6 {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- persistence.NewRepository(pool).RecordInvocationWorkspaceOwnerStop(ctx, entry.Record)
		}()
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("concurrent exact receipt", err)
		}
	}
	retained, err := persistence.NewRepository(pool).GetInvocationWorkspaceOwnerStop(ctx, claim.IsolationDomainID, claim.ID)
	if err != nil || retained != entry.Record {
		t.Fatal("replacement could not read original stop evidence", err)
	}
	if _, err := repo.GetInvocationWorkspaceOwnerStop(ctx, identity.New("iso"), claim.ID); err == nil {
		t.Fatal("cross-domain receipt read")
	}
	if observed, err := repo.GetInvocationWorkspaceQuiescence(ctx, claim.IsolationDomainID, claim.ID); err != nil || observed.Confirmed {
		t.Fatal("historical receipt granted normal cleanup completion", err)
	}
	invocation, err := repo.GetInvocation(ctx, claim.IsolationDomainID, claim.ResourceID)
	if err != nil || invocation.State != "cancelling" {
		t.Fatal("stop receipt changed invocation lifecycle", err)
	}
	for _, query := range []string{`UPDATE invocation_workspace_owner_stop_receipts SET actor_id=actor_id`, `DELETE FROM invocation_workspace_owner_stop_receipts`} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatal("stop evidence mutable")
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND resource_id=$2 AND action='workspace.owner-stop.recorded'`, claim.IsolationDomainID, claim.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("receipt replay duplicated audit", count, err)
	}
	db, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := persistence.MigrateDownTo(ctx, db, 83); err == nil || !strings.Contains(err.Error(), "cannot remove workspace owner stop evidence") {
		t.Fatal("downgrade discarded stop evidence", err)
	}
}
