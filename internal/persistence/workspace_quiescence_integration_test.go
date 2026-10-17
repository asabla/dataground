package persistence_test

import (
	"context"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testDurableWorkspaceQuiescence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, source workspace.SourceRecord, service string, objects workspace.SourceObjects) {
	t.Helper()
	repo := persistence.NewRepository(pool)
	invocationID := identity.New("inv")
	if _, err := repo.AcceptInvocation(ctx, testIdempotency(source.IsolationDomainID, "workspace-cleanup-invocation"), persistence.AcceptInvocationInput{ID: invocationID, ServiceID: service, Alias: "source", Input: map[string]any{"prompt": "cleanup source"}, ActorID: "source-requester", CorrelationID: identity.New("cor"), Deadline: time.Now().Add(2 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	worker := reconcile.New(repo, reconcile.NewReferenceDriver(pool), "cleanup-setup")
	for range 2 {
		if ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil || !ran {
			t.Fatal("advance cleanup invocation", ran, err)
		}
	}
	claim, err := repo.ClaimNext(ctx, persistence.OperationKindInvocation, "cleanup-owner", time.Minute)
	if err != nil || claim == nil || claim.ResourceID != invocationID {
		t.Fatal("claim cleanup invocation", err)
	}
	effect, err := repo.PrepareEffect(ctx, *claim, "run-invocation", sha256.Sum256([]byte("cleanup-runtime")))
	if err != nil {
		t.Fatal(err)
	}
	executions := executionpostgres.New(pool)
	placement, err := executions.ReservePlacement(ctx, execution.PlacementRequest{IsolationDomainID: source.IsolationDomainID, OperationID: claim.ID, RequiredCapabilities: []string{"codex.app-server"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.ExecutionRef{IsolationDomainID: source.IsolationDomainID, ID: identity.New("exe")}
	if err := executions.SaveExecution(ctx, execution.ExecutionRecord{Execution: execution.Execution{IsolationDomainID: ref.IsolationDomainID, ID: ref.ID, GatewayID: placement.GatewayID, State: "ready"}, OperationID: claim.ID, PlacementID: placement.ID, SandboxName: "cleanup-recovery"}); err != nil {
		t.Fatal(err)
	}
	preparation, err := repo.BeginInvocationWorkspacePreparation(ctx, *claim, effect, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	base, _, err := repo.ReadPreparingWorkspaceSource(ctx, *claim, effect, objects, preparation, &reconcile.SourceCedarEvaluator{})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := base.Materialize(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteInvocationWorkspacePreparation(ctx, *claim, effect, preparation, receipt, &reconcile.SourceCedarEvaluator{}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BeginInvocationWorkspaceQuiescence(ctx, *claim, effect, preparation.Execution, "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("cleanup without runtime reservation")
	}
	if _, err := repo.BeginInvocationRuntimeAttempt(ctx, *claim, effect); err != nil {
		t.Fatal(err)
	}
	testWorkspaceQuiescence(t, ctx, pool, *claim, effect, preparation.Execution)
}

// The real source/claim fixture supplies a completed preparation and a reserved
// runtime attempt. The controller tests separately require a terminal exchange;
// these tests exercise the database-authorized observation boundary.
func testWorkspaceQuiescence(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claim persistence.OperationClaim, effect persistence.EffectRecord, ref execution.ExecutionRef) {
	t.Helper()
	repo := persistence.NewRepository(pool)
	binding := "sha256:" + strings.Repeat("a", 64)
	begin := func(c persistence.OperationClaim, e persistence.EffectRecord, r execution.ExecutionRef, b string) (persistence.InvocationWorkspaceQuiescenceEntry, error) {
		return repo.BeginInvocationWorkspaceQuiescence(ctx, c, e, r, b)
	}
	for name, mutate := range map[string]func(*persistence.OperationClaim, *persistence.EffectRecord, *execution.ExecutionRef, *string){
		"scope": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *execution.ExecutionRef, _ *string) {
			c.IsolationDomainID = identity.New("iso")
		},
		"invocation": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *execution.ExecutionRef, _ *string) {
			c.ResourceID = identity.New("inv")
		},
		"owner": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *execution.ExecutionRef, _ *string) {
			c.LeaseOwner = "replacement"
		},
		"token": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *execution.ExecutionRef, _ *string) {
			c.FencingToken++
		},
		"actor": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *execution.ExecutionRef, _ *string) {
			c.ActorID = "other"
		},
		"correlation": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *execution.ExecutionRef, _ *string) {
			c.CorrelationID = identity.New("cor")
		},
		"version": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *execution.ExecutionRef, _ *string) {
			c.StateMachineVersion++
		},
		"effect": func(_ *persistence.OperationClaim, e *persistence.EffectRecord, _ *execution.ExecutionRef, _ *string) {
			e.RequestDigest[0]++
		},
		"execution": func(_ *persistence.OperationClaim, _ *persistence.EffectRecord, r *execution.ExecutionRef, _ *string) {
			r.ID = identity.New("exe")
		},
		"binding": func(_ *persistence.OperationClaim, _ *persistence.EffectRecord, _ *execution.ExecutionRef, b *string) {
			*b = "invalid"
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, e, r, b := claim, effect, ref, binding
			mutate(&c, &e, &r, &b)
			if _, err := begin(c, e, r, b); err == nil {
				t.Fatal("substituted cleanup intent accepted")
			}
		})
	}
	inject := func(action string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_cleanup_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='`+action+`' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_cleanup_audit BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_cleanup_audit()`); err != nil {
			t.Fatal(err)
		}
	}
	clearFailure := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `DROP FUNCTION reject_cleanup_audit() CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_cleanup_audit() CASCADE`)
	inject("workspace.cleanup.prepared")
	if _, err := begin(claim, effect, ref, binding); err == nil {
		t.Fatal("intent survived failed audit")
	}
	if _, err := repo.GetInvocationWorkspaceQuiescence(ctx, claim.IsolationDomainID, claim.ID); err == nil {
		t.Fatal("rolled back intent remained")
	}
	clearFailure()
	entry, err := begin(claim, effect, ref, binding)
	if err != nil || entry.Confirmed || !entry.ExpiresAt.After(time.Now()) || entry.ExpiresAt.After(claim.DeadlineAt) {
		t.Fatal("cleanup intent", entry, err)
	}
	if again, err := begin(claim, effect, ref, binding); err != nil || again.Record != entry.Record || again.Confirmed {
		t.Fatal("intent replay", err)
	}
	if _, err := begin(claim, effect, ref, "sha256:"+strings.Repeat("b", 64)); err == nil {
		t.Fatal("another connection replaced original intent")
	}
	wrong := entry.Record
	wrong.PlanDigest = "sha256:" + strings.Repeat("b", 64)
	if _, err := repo.CompleteInvocationWorkspaceQuiescence(ctx, claim, effect, wrong); err == nil {
		t.Fatal("substituted completion")
	}
	wrong = entry.Record
	wrong.OwnerBinding = "sha256:" + strings.Repeat("b", 64)
	if _, err := repo.CompleteInvocationWorkspaceQuiescence(ctx, claim, effect, wrong); err == nil {
		t.Fatal("new owner's acknowledgement accepted")
	}
	// Expiry is database-owned; caller-carried future timestamps cannot restore it.
	var expires time.Time
	if err := pool.QueryRow(ctx, `SELECT lease_expires_at FROM invocation_execution_operations WHERE isolation_domain_id=$1 AND id=$2`, claim.IsolationDomainID, claim.ID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, claim.IsolationDomainID, claim.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CompleteInvocationWorkspaceQuiescence(ctx, claim, effect, entry.Record); err == nil {
		t.Fatal("expired owner confirmed cleanup")
	}
	if pending, err := persistence.NewRepository(pool).GetInvocationWorkspaceQuiescence(ctx, claim.IsolationDomainID, claim.ID); err != nil || pending.Confirmed {
		t.Fatal("lost acknowledgement became confirmation", err)
	}
	replacementClaim := claim
	replacementClaim.LeaseOwner = "replacement-cleanup-owner"
	replacementClaim.FencingToken++
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_owner=$3,lease_token=$4,lease_expires_at=clock_timestamp()+interval '1 minute' WHERE isolation_domain_id=$1 AND id=$2`, claim.IsolationDomainID, claim.ID, replacementClaim.LeaseOwner, replacementClaim.FencingToken); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CompleteInvocationWorkspaceQuiescence(ctx, claim, effect, entry.Record); err == nil {
		t.Fatal("stale original owner completed pending cleanup")
	}
	if _, err := begin(replacementClaim, effect, ref, binding); err == nil {
		t.Fatal("replacement adopted pending owner connection")
	}
	if _, err := repo.CompleteInvocationWorkspaceQuiescence(ctx, replacementClaim, effect, entry.Record); err == nil {
		t.Fatal("replacement confirmed pending cleanup")
	}
	// Restore the test lease so the same original owner can exercise commit loss.
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=$3,lease_owner=$4,lease_token=$5 WHERE isolation_domain_id=$1 AND id=$2`, claim.IsolationDomainID, claim.ID, expires, claim.LeaseOwner, claim.FencingToken); err != nil {
		t.Fatal(err)
	}
	inject("workspace.cleanup.confirmed")
	if _, err := repo.CompleteInvocationWorkspaceQuiescence(ctx, claim, effect, entry.Record); err == nil {
		t.Fatal("completion survived failed audit")
	}
	if pending, err := repo.GetInvocationWorkspaceQuiescence(ctx, claim.IsolationDomainID, claim.ID); err != nil || pending.Confirmed {
		t.Fatal("failed completion retained", err)
	}
	clearFailure()
	completed, err := repo.CompleteInvocationWorkspaceQuiescence(ctx, claim, effect, entry.Record)
	if err != nil || !completed.Confirmed || completed.Record != entry.Record {
		t.Fatal("completion", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, claim.IsolationDomainID, claim.ID); err != nil {
		t.Fatal(err)
	}
	if historical, err := begin(claim, effect, ref, binding); err != nil || !historical.Confirmed || historical.Record != entry.Record {
		t.Fatal("expired exact intent replay", err)
	}
	for range 3 {
		if again, err := repo.CompleteInvocationWorkspaceQuiescence(ctx, claim, effect, entry.Record); err != nil || !again.Confirmed || again.Record != entry.Record {
			t.Fatal("completion replay", err)
		}
	}
	replacement := claim
	replacement.LeaseOwner = "replacement"
	replacement.FencingToken++
	if _, err := repo.CompleteInvocationWorkspaceQuiescence(ctx, replacement, effect, entry.Record); err == nil {
		t.Fatal("replacement confirmed original connection")
	}
	if observed, err := persistence.NewRepository(pool).GetInvocationWorkspaceQuiescence(ctx, claim.IsolationDomainID, claim.ID); err != nil || !observed.Confirmed || observed.Record != entry.Record {
		t.Fatal("replacement cannot observe completion", err)
	}
	if _, err := repo.GetInvocationWorkspaceQuiescence(ctx, identity.New("iso"), claim.ID); err == nil {
		t.Fatal("cross-domain completion")
	}
	for _, table := range []string{"invocation_workspace_quiescence_intents", "invocation_workspace_quiescence_completions"} {
		for _, statement := range []string{"UPDATE " + table + " SET actor_id=actor_id", "DELETE FROM " + table} {
			if _, err := pool.Exec(ctx, statement); err == nil {
				t.Fatal("cleanup evidence mutable", statement)
			}
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND resource_id=$2 AND action IN ('workspace.cleanup.prepared','workspace.cleanup.confirmed')`, claim.IsolationDomainID, claim.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("cleanup audit count", count, err)
	}
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 72); err == nil || !strings.Contains(err.Error(), "cannot remove workspace cleanup evidence") {
		t.Fatal("downgrade lost cleanup evidence", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=$3 WHERE isolation_domain_id=$1 AND id=$2`, claim.IsolationDomainID, claim.ID, expires); err != nil {
		t.Fatal(err)
	}
}
