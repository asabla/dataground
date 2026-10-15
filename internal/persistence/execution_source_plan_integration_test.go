package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testRevisionSourceBinding(t *testing.T, ctx context.Context, pool *pgxpool.Pool, record workspace.SourceRecord, service, revision string) {
	scope := record.IsolationDomainID
	store := executionpostgres.New(pool)
	plan := durableExecutionPlan(scope, revision)
	plan.SchemaVersion, plan.WorkspaceSource = execution.ExecutionPlanSchemaV2, &record
	plan.RuntimeProfile, plan.RequiredCapabilities = "codex", []string{}
	binding := execution.ExecutionPlanBinding{Plan: plan, ActorID: "operator", CorrelationID: identity.New("cor")}
	for name, change := range map[string]func(*workspace.SourceRecord){
		"missing":      func(r *workspace.SourceRecord) { r.ID = identity.New("src") },
		"workspace":    func(r *workspace.SourceRecord) { r.WorkspaceID = identity.New("wsp") },
		"digest":       func(r *workspace.SourceRecord) { r.ArchiveDigest = "sha256:" + strings.Repeat("d", 64) },
		"storage":      func(r *workspace.SourceRecord) { r.StorageProfileDigest = "sha256:" + strings.Repeat("e", 64) },
		"archive size": func(r *workspace.SourceRecord) { r.ArchiveBytes += 512 },
		"entry count":  func(r *workspace.SourceRecord) { r.EntryCount++ },
		"source size":  func(r *workspace.SourceRecord) { r.SourceBytes++ },
	} {
		t.Run(name, func(t *testing.T) {
			b := binding
			b.Plan = execution.CloneExecutionPlan(plan)
			change(b.Plan.WorkspaceSource)
			if _, err := store.BindExecutionPlan(ctx, b); err == nil {
				t.Fatal("unmatched source bound")
			}
		})
	}
	pending := record
	pending.ID = identity.New("src")
	if _, err := persistence.NewRepository(pool).PrepareWorkspaceSource(ctx, workspace.SourceInstallation{Record: pending, ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	pendingBinding := binding
	pendingBinding.Plan = execution.CloneExecutionPlan(plan)
	pendingBinding.Plan.WorkspaceSource = &pending
	if _, err := store.BindExecutionPlan(ctx, pendingBinding); err == nil {
		t.Fatal("incomplete source bound")
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_source_plan_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='execution-plan.bind' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_source_plan_audit BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_source_plan_audit()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_source_plan_audit() CASCADE`)
	if _, err := store.BindExecutionPlan(ctx, binding); err == nil {
		t.Fatal("failed audit accepted source plan")
	}
	if _, err := store.GetExecutionPlan(ctx, scope, revision); !errors.Is(err, execution.ErrExecutionPlanMissing) {
		t.Fatal("audit failure retained plan", err)
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_source_plan_audit() CASCADE`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := store.BindExecutionPlan(ctx, binding); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("concurrent source binding", err)
		}
	}
	loaded, err := executionpostgres.New(pool).GetExecutionPlan(ctx, scope, revision)
	if err != nil || !execution.EqualExecutionPlans(loaded, plan) {
		t.Fatal("source plan did not survive restart", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='execution-plan.bind' AND resource_id=$2`, scope, revision).Scan(&count); err != nil || count != 1 {
		t.Fatal("plan audit duplicated", count, err)
	}
	for _, sql := range []string{`UPDATE service_revision_execution_plans SET workspace_source=NULL WHERE isolation_domain_id=$1 AND revision_id=$2`, `UPDATE service_revision_execution_plans SET plan_digest='sha256:0000000000000000000000000000000000000000000000000000000000000000' WHERE isolation_domain_id=$1 AND revision_id=$2`, `DELETE FROM service_revision_execution_plans WHERE isolation_domain_id=$1 AND revision_id=$2`, `DELETE FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2`} {
		if _, err := pool.Exec(ctx, sql, scope, revision); err == nil {
			t.Fatal("source plan evidence was mutable")
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='published' WHERE isolation_domain_id=$1 AND id=$2`, scope, revision); err != nil {
		t.Fatal(err)
	}
	// The operator command uses the same canonical v2 contract, including replay
	// after publication. A successful retry must not rebind or add another audit.
	normalized, err := execution.NormalizeExecutionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source-plan.json")
	if err := os.WriteFile(path, append(content, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := execution.DigestExecutionPlan(normalized)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "go", "run", "../../cmd/dataground-execution-plan-install", "-plan-file", path, "-plan-digest", digest, "-isolation-domain", scope, "-revision", revision, "-actor", binding.ActorID, "-correlation-id", binding.CorrelationID)
	command.Env = append(os.Environ(), "DATAGROUND_DATABASE_URL="+testDatabaseURL(t))
	if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatal("source plan command failed or emitted output", err)
	}
	changed := binding
	changed.Plan = execution.CloneExecutionPlan(plan)
	changed.Plan.WorkspaceSource = &pending
	if _, err := store.BindExecutionPlan(ctx, changed); !errors.Is(err, execution.ErrExecutionPlanConflict) {
		t.Fatal("source replacement accepted", err)
	}
	publishedRevision := identity.New("rev")
	if _, err := pool.Exec(ctx, `INSERT INTO service_revisions (isolation_domain_id,id,service_id,revision_number,state,runtime_profile,created_at,updated_at,created_by,published_at) VALUES ($1,$2,$3,2,'published','codex',now(),now(),'fixture',now())`, scope, publishedRevision, service); err != nil {
		t.Fatal(err)
	}
	changed = binding
	changed.Plan = execution.CloneExecutionPlan(plan)
	changed.Plan.RevisionID = publishedRevision
	if _, err := store.BindExecutionPlan(ctx, changed); err == nil {
		t.Fatal("late source binding accepted")
	}
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 67); err == nil {
		t.Fatal("downgrade removed source selection")
	}
}
