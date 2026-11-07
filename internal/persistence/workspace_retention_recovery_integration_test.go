package persistence_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testWorkspaceRetentionRecovery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, previous persistence.OperationClaim, stopped persistence.InvocationWorkspaceQuiescence) *persistence.OperationClaim {
	t.Helper()
	repo := persistence.NewRepository(pool)
	input := persistence.WorkspaceRetentionRecoveryInput{IsolationDomainID: previous.IsolationDomainID, OperationID: previous.ID, ExpectedGeneration: 1, OwnerBinding: stopped.OwnerBinding, ActorID: "recovery-operator", CorrelationID: identity.New("cor"), ReasonDigest: "sha256:" + strings.Repeat("a", 64), Deadline: time.Now().UTC().Truncate(time.Microsecond).Add(5 * time.Minute)}
	if err := repo.RecoverWorkspaceRetention(ctx, input); err == nil {
		t.Fatal("nonterminal operation recovered")
	}
	if err := repo.Fail(ctx, previous, persistence.OperationFailureDeadline); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT generation FROM invocation_execution_operations WHERE isolation_domain_id=$1 AND id=$2`, previous.IsolationDomainID, previous.ID).Scan(&input.ExpectedGeneration); err != nil {
		t.Fatal(err)
	}
	if err := repo.RepairOperation(ctx, previous.Kind, previous.IsolationDomainID, previous.ID, "operator", "retry", "generic-source-repair", input.Deadline); err == nil {
		t.Fatal("generic repair restored source execution")
	}
	// Retention recovery preserves the original cancellation intent. It cannot
	// turn a different failed execution into a cancellation or reuse torn-down scratch.
	for _, statements := range [][2]string{
		{`UPDATE invocation_execution_operations SET command='invoke' WHERE isolation_domain_id=$1`, `UPDATE invocation_execution_operations SET command='cancel' WHERE isolation_domain_id=$1`},
		{`UPDATE execution_instances SET observed_state='terminated',terminated_at=clock_timestamp() WHERE isolation_domain_id=$1`, `UPDATE execution_instances SET observed_state='ready',terminated_at=NULL WHERE isolation_domain_id=$1`},
		{`UPDATE invocation_execution_operations SET error=jsonb_set(error,'{code}','"OPERATION_EFFECT_DENIED"') WHERE isolation_domain_id=$1`, `UPDATE invocation_execution_operations SET error=jsonb_set(error,'{code}','"OPERATION_DEADLINE_EXCEEDED"') WHERE isolation_domain_id=$1`},
	} {
		if _, err := pool.Exec(ctx, statements[0], input.IsolationDomainID); err != nil {
			t.Fatal(err)
		}
		if err := repo.RecoverWorkspaceRetention(ctx, input); err == nil {
			t.Fatal("ineligible workspace recovered")
		}
		if _, err := pool.Exec(ctx, statements[1], input.IsolationDomainID); err != nil {
			t.Fatal(err)
		}
	}
	for name, change := range map[string]func(*persistence.WorkspaceRetentionRecoveryInput){
		"scope":      func(v *persistence.WorkspaceRetentionRecoveryInput) { v.IsolationDomainID = identity.New("iso") },
		"operation":  func(v *persistence.WorkspaceRetentionRecoveryInput) { v.OperationID = identity.New("op") },
		"generation": func(v *persistence.WorkspaceRetentionRecoveryInput) { v.ExpectedGeneration-- },
		"binding": func(v *persistence.WorkspaceRetentionRecoveryInput) {
			v.OwnerBinding = "sha256:" + strings.Repeat("f", 64)
		},
		"actor":       func(v *persistence.WorkspaceRetentionRecoveryInput) { v.ActorID = "" },
		"correlation": func(v *persistence.WorkspaceRetentionRecoveryInput) { v.CorrelationID = "" },
		"reason":      func(v *persistence.WorkspaceRetentionRecoveryInput) { v.ReasonDigest = "private reason" },
		"expired":     func(v *persistence.WorkspaceRetentionRecoveryInput) { v.Deadline = time.Now().Add(-time.Second) },
		"unbounded":   func(v *persistence.WorkspaceRetentionRecoveryInput) { v.Deadline = time.Now().Add(time.Hour) },
	} {
		t.Run("recovery "+name, func(t *testing.T) {
			v := input
			change(&v)
			if err := repo.RecoverWorkspaceRetention(ctx, v); err == nil {
				t.Fatal("invalid recovery accepted")
			}
		})
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_retention_recovery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='workspace.retention-recovery.accepted' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_retention_recovery_audit BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_retention_recovery_audit()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_retention_recovery_audit() CASCADE`)
	if err := repo.RecoverWorkspaceRetention(ctx, input); err == nil {
		t.Fatal("recovery survived failed audit")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_retention_recoveries WHERE isolation_domain_id=$1`, input.IsolationDomainID).Scan(&count); err != nil || count != 0 {
		t.Fatal("rolled back recovery remained", err)
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_retention_recovery_audit() CASCADE`); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	results := make(chan error, 6)
	for range 6 {
		group.Add(1)
		go func() { defer group.Done(); results <- repo.RecoverWorkspaceRetention(ctx, input) }()
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("exact concurrent recovery", err)
		}
	}
	otherRequest := input
	otherRequest.CorrelationID = identity.New("cor")
	if err := repo.RecoverWorkspaceRetention(ctx, otherRequest); err == nil {
		t.Fatal("another request reused a consumed failed generation")
	}
	for _, change := range []func(*persistence.WorkspaceRetentionRecoveryInput){func(v *persistence.WorkspaceRetentionRecoveryInput) { v.ActorID = "other" }, func(v *persistence.WorkspaceRetentionRecoveryInput) { v.Deadline = v.Deadline.Add(time.Second) }, func(v *persistence.WorkspaceRetentionRecoveryInput) {
		v.ReasonDigest = "sha256:" + strings.Repeat("b", 64)
	}} {
		v := input
		change(&v)
		if err := repo.RecoverWorkspaceRetention(ctx, v); err == nil {
			t.Fatal("changed recovery reused identifier")
		}
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='workspace.retention-recovery.accepted'`, input.IsolationDomainID).Scan(&count); err != nil || count != 1 {
		t.Fatal("replay duplicated recovery audit", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2 AND correlation_id=$3 AND actor_id=$4 AND event_type='lifecycle.cancellation.requested'`, input.IsolationDomainID, previous.ResourceID, input.CorrelationID, input.ActorID).Scan(&count); err != nil || count != 1 {
		t.Fatal("recovery event lost its request attribution", err)
	}
	for _, sql := range []string{`UPDATE workspace_retention_recoveries SET actor_id=actor_id`, `DELETE FROM workspace_retention_recoveries`} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Fatal("recovery evidence mutable")
		}
	}
	if _, err := repo.RenewLease(ctx, previous, time.Minute); err == nil {
		t.Fatal("original execution lease revived")
	}
	claim, err := repo.ClaimNextInIsolationDomain(ctx, previous.Kind, input.IsolationDomainID, "recovered-retention-owner", time.Minute)
	if err != nil || claim == nil || claim.Command != "cancel" || claim.ObservedState != "cancelling" || claim.ActorID != input.ActorID || claim.CorrelationID != input.CorrelationID || claim.FencingToken <= previous.FencingToken || !claim.DeadlineAt.Equal(input.Deadline) {
		t.Fatal("recovery did not bind a fresh cancellation claim", err)
	}
	invocation, err := repo.GetInvocation(ctx, input.IsolationDomainID, previous.ResourceID)
	if err != nil || invocation.State != "cancelling" || invocation.CompletedAt != nil || invocation.Error != nil {
		t.Fatal("recovery retained stale terminal state", err)
	}
	db, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := persistence.MigrateDownTo(ctx, db, 85); err == nil || !strings.Contains(err.Error(), "cannot remove workspace retention recovery evidence") {
		t.Fatal("downgrade discarded recovery evidence", err)
	}
	return claim
}
