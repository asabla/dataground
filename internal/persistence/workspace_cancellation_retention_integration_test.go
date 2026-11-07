package persistence_test

import (
	"context"
	"crypto/sha256"
	"errors"
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

func testWorkspaceCancellationRetention(t *testing.T, ctx context.Context, pool *pgxpool.Pool, original persistence.OperationClaim, runtimeEffect persistence.EffectRecord, stopped persistence.InvocationWorkspaceQuiescence, service, revision string, source *exportSourceObjects, base *workspace.Snapshot, version string) {
	t.Helper()
	repo := persistence.NewRepository(pool)
	scope := original.IsolationDomainID
	if _, err := repo.AcceptCancellation(ctx, testIdempotency(scope, "retention-cancel"), persistence.AcceptCancellationInput{InvocationID: original.ResourceID, ActorID: "retention-requester", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	claim, err := repo.ClaimNextInIsolationDomain(ctx, persistence.OperationKindInvocation, scope, "retention-owner", time.Minute)
	if err != nil || claim == nil || claim.ID != original.ID {
		t.Fatal("cancellation claim", err)
	}
	effect, err := repo.PrepareEffect(ctx, *claim, "cancel-invocation", sha256.Sum256([]byte(scope+":"+claim.ID+":cancel-invocation")))
	if err != nil {
		t.Fatal(err)
	}
	authorize := func(c persistence.OperationClaim, e persistence.EffectRecord, q persistence.InvocationWorkspaceQuiescence) (persistence.WorkspaceChangeExportPermit, error) {
		return repo.AuthorizeCancellationWorkspaceChangeExport(ctx, c, e, q, "capture", workspace.ChangeSetReceipt{}, &reconcile.WorkspaceExportCedarEvaluator{})
	}
	if _, err := authorize(*claim, effect, stopped); err == nil {
		t.Fatal("intent without original shutdown authorized export")
	}
	before := source.reads
	if _, _, err := repo.ReadCancellationWorkspaceSource(ctx, *claim, effect, source, stopped, 1, &reconcile.SourceCedarEvaluator{}); err == nil || source.reads != before {
		t.Fatal("unconfirmed stop reached source")
	}
	if err := repo.RecordInvocationWorkspaceOwnerStop(ctx, stopped); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CompleteInvocationWorkspaceQuiescence(ctx, original, runtimeEffect, stopped); err == nil {
		t.Fatal("restored cancelled execution authority")
	}
	permit, err := authorize(*claim, effect, stopped)
	if version == "v6" {
		if !errors.Is(err, persistence.ErrWorkspaceExportDenied) {
			t.Fatal("old wildcard granted cancellation export", err)
		}
		if _, _, err := repo.ReadCancellationWorkspaceSource(ctx, *claim, effect, source, stopped, 1, &reconcile.SourceCedarEvaluator{}); err == nil || source.reads != before {
			t.Fatal("old wildcard read cancellation source")
		}
		return
	}
	if err != nil || permit.Request.EffectID != runtimeEffect.EffectID || permit.Request.FencingToken != claim.FencingToken || permit.Request.RuntimeToken != original.FencingToken || permit.Request.CorrelationID != claim.CorrelationID || permit.Request.ActorID != "retention-requester" || claim.ActorID == original.ActorID {
		t.Fatal("wrong authority binding", err)
	}
	for _, kind := range []string{"scope", "lease", "token", "actor", "correlation", "effect", "execution", "source", "owner"} {
		c, e, q := *claim, effect, stopped
		switch kind {
		case "scope":
			c.IsolationDomainID = identity.New("iso")
		case "lease":
			c.LeaseOwner = "replacement"
		case "token":
			c.FencingToken++
		case "actor":
			c.ActorID = "other"
		case "correlation":
			c.CorrelationID = identity.New("cor")
		case "effect":
			e.RequestDigest[0]++
		case "execution":
			q.ExecutionID = identity.New("exe")
		case "source":
			q.Source.ID = identity.New("src")
		case "owner":
			q.OwnerBinding = "sha256:" + strings.Repeat("d", 64)
		}
		if _, err := authorize(c, e, q); err == nil {
			t.Fatal("substitution accepted", kind)
		}
	}
	if _, err := repo.AuthorizeInvocationWorkspaceChangeExport(ctx, *claim, runtimeEffect, stopped, "capture", workspace.ChangeSetReceipt{}, &reconcile.WorkspaceExportCedarEvaluator{}); err == nil {
		t.Fatal("cancellation acquired normal export authority")
	}
	result, _ := exportSnapshot(t, "cancelled changes\x00\xfe")
	changes, err := workspace.Compare(base, result)
	if err != nil {
		t.Fatal(err)
	}
	objects := &exportArtifactObjects{loseWrite: true}
	provider := &exportCaptureProvider{ref: execution.ExecutionRef{IsolationDomainID: scope, ID: stopped.ExecutionID}, base: base, changes: changes}
	config := reconcile.InvocationWorkspaceConfig{Scope: reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: scope, ServiceID: service, RevisionID: revision}, Source: stopped.Source, PlanDigest: stopped.PlanDigest, GrantGeneration: 1}
	exporter, err := reconcile.NewInvocationWorkspaceExporter(repo, source, objects, objects, config)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := reconcile.NewInvocationWorkspaceCompletion(repo, exporter)
	if err != nil {
		t.Fatal(err)
	}
	check := func(context.Context) error { return nil }
	retain := func(c persistence.OperationClaim) (workspace.ChangeExportEntry, error) {
		return completion.RetainCancellation(ctx, c, effect, provider.ref, provider, check)
	}
	if _, err := completion.RetainCancellation(ctx, *claim, effect, provider.ref, provider, func(context.Context) error { return errors.New("acceptance unavailable") }); err == nil || provider.calls != 0 {
		t.Fatal("missing acceptance captured files")
	}

	source.beforeRead = func() {
		source.beforeRead = nil
		if err := repo.ChangeWorkspaceSourceGrant(ctx, workspace.SourceGrantChange{Contract: workspace.SourceGrantV1, Record: stopped.Source, RevisionID: revision, Generation: 2, Operation: "revoke", ActorID: "operator", ReasonDigest: "sha256:" + strings.Repeat("a", 64), CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := retain(*claim); err == nil || provider.calls != 0 {
		t.Fatal("revoked delivery reached capture")
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.ChangeWorkspaceSourceGrant(ctx, workspace.SourceGrantChange{Contract: workspace.SourceGrantV1, Record: stopped.Source, RevisionID: revision, Generation: 3, Operation: "activate", ActivatedAt: now.Add(-time.Second), ExpiresAt: now.Add(30 * time.Minute), ActorID: "operator", ReasonDigest: "sha256:" + strings.Repeat("a", 64), CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	config.GrantGeneration = 3
	exporter, err = reconcile.NewInvocationWorkspaceExporter(repo, source, objects, objects, config)
	if err != nil {
		t.Fatal(err)
	}
	completion, err = reconcile.NewInvocationWorkspaceCompletion(repo, exporter)
	if err != nil {
		t.Fatal(err)
	}
	before = source.reads
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_cancel_export_decision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END; $$; CREATE TRIGGER reject_cancel_export_decision BEFORE INSERT ON workspace_cancellation_export_decisions FOR EACH ROW EXECUTE FUNCTION reject_cancel_export_decision()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_cancel_export_decision() CASCADE`)
	if _, err := retain(*claim); err == nil || provider.calls != 0 || source.reads != before {
		t.Fatal("failed decision reached files")
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_cancel_export_decision() CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_cancel_export_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='workspace.changes.export.completed' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_cancel_export_completion BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_cancel_export_completion()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_cancel_export_completion() CASCADE`)
	if _, err := retain(*claim); err == nil {
		t.Fatal("failed completion audit returned success")
	}
	if objects.writes != 1 || provider.calls != 1 {
		t.Fatal("capture/write count", objects.writes, provider.calls)
	}
	if entry, err := repo.GetWorkspaceChangeExport(ctx, scope, claim.ID); err != nil || entry.Ready {
		t.Fatal("failed audit published artifact", err)
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_cancel_export_completion() CASCADE`); err != nil {
		t.Fatal(err)
	}
	if err := repo.ScheduleRetry(ctx, *claim, "unknown", "WORKSPACE_RETENTION_REQUIRED", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	replacement, err := repo.ClaimNextInIsolationDomain(ctx, persistence.OperationKindInvocation, scope, "retention-replacement", time.Minute)
	if err != nil || replacement == nil || replacement.FencingToken <= claim.FencingToken {
		t.Fatal("replacement claim", err)
	}
	if _, err := retain(*claim); err == nil {
		t.Fatal("stale cancellation lease retained output")
	}
	entry, err := retain(*replacement)
	if err != nil || !entry.Ready || entry.Record.Changes != changes.Receipt() || objects.writes != 1 || provider.calls != 1 {
		t.Fatal("replacement retention", err)
	}

	binding := workspace.ChangeExportBinding{Record: entry.Record, ActorID: replacement.ActorID, CorrelationID: replacement.CorrelationID, LeaseOwner: replacement.LeaseOwner, FencingToken: replacement.FencingToken, StateMachineVersion: replacement.StateMachineVersion}
	wrong := *replacement
	wrong.ActorID = "other"
	if _, err := repo.PrepareCancellationWorkspaceChangeExport(ctx, wrong, effect, stopped, binding); err == nil {
		t.Fatal("completed replay ignored current caller binding")
	}
	if _, err := repo.CompleteCancellationWorkspaceChangeExport(ctx, wrong, effect, stopped, binding); err == nil {
		t.Fatal("completed replay ignored current caller binding")
	}
	if q, err := repo.GetInvocationWorkspaceQuiescence(ctx, scope, claim.ID); err != nil || q.Confirmed {
		t.Fatal("retention rewrote execution cleanup authority", err)
	}
	var reads int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_source_use_decisions WHERE isolation_domain_id=$1 AND operation_id=$2 AND claim_contract='dataground.invocation-source-read/v6' AND stopped_operation_id=$2 AND cleanup_operation_id IS NULL AND outcome='allowed'`, scope, claim.ID).Scan(&reads); err != nil || reads < 4 {
		t.Fatal("missing purpose-bound source decisions", reads, err)
	}
	bytes := append([]byte(nil), objects.data...)
	objects.data = []byte("corrupt")
	if _, err := retain(*replacement); err == nil || provider.calls != 1 || objects.writes != 1 {
		t.Fatal("corrupt completed output replayed or recaptured")
	}
	objects.data = bytes

	// The actual claimed driver uses all three independent policy actions. Its
	// provider cannot start an owner or delete scratch, including on exact replay.
	policySource, err := reconcile.NewDurableInvocationAuthorizationPolicySource(repo)
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := reconcile.NewAuditedCedarInvocationAuthorizer(policySource, repo)
	if err != nil {
		t.Fatal(err)
	}
	transport := &cancellationRetentionTransport{exportCaptureProvider: provider, t: t}
	ready := true
	driver, err := reconcile.NewOwnedInvocationCancellationDriver(repo, authorizer, executionpostgres.New(pool), transport, completion, func(context.Context) error {
		if !ready {
			return errors.New("acceptance unavailable")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Apply(ctx, effect); !errors.Is(err, reconcile.ErrInvocationCancellationRetentionRequired) {
		t.Fatal("claimless retention", err)
	}
	ready = false
	before = source.reads
	if _, err := driver.ApplyClaimed(ctx, *replacement, effect); !errors.Is(err, reconcile.ErrInvocationCancellationRetentionRequired) || source.reads != before {
		t.Fatal("unready driver read files", err)
	}
	ready = true
	observation, err := driver.ApplyClaimed(ctx, *replacement, effect)
	if err != nil || observation["state"] != "stopped" || observation["workspaceRetained"] != true || observation["workspaceChangesArtifactId"] != entry.Record.ArtifactRecord().ID {
		t.Fatal("owned cancellation result", observation, err)
	}
	// Lose the acknowledgement after effect recording but before operation advance.
	if err := repo.RecordEffect(ctx, effect, "succeeded", observation, ""); err != nil {
		t.Fatal(err)
	}
	effect, err = repo.GetEffect(ctx, scope, effect.EffectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.ApplyClaimed(ctx, *replacement, effect); err != nil {
		t.Fatal("recorded effect could not recover", err)
	}
	for _, statement := range []string{
		`UPDATE workspace_cancellation_export_decisions SET outcome='denied' WHERE isolation_domain_id=$1`,
		`DELETE FROM workspace_cancellation_export_decisions WHERE isolation_domain_id=$1`,
	} {
		if _, err := pool.Exec(ctx, statement, scope); err == nil {
			t.Fatal("cancellation decisions were mutable")
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET deadline_at=clock_timestamp()-interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, scope, claim.ID); err != nil {
		t.Fatal(err)
	}
	before = source.reads
	if _, err := retain(*replacement); err == nil || source.reads != before {
		t.Fatal("expired cancellation acquired retention authority")
	}
	db, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := persistence.MigrateDownTo(ctx, db, 84); err == nil || !strings.Contains(err.Error(), "cannot remove cancellation retention evidence") {
		t.Fatal("downgrade discarded recovery purpose", err)
	}
	// Restore only this fixture deadline to exercise the final reconciler step.
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET deadline_at=clock_timestamp()+interval '1 minute' WHERE isolation_domain_id=$1 AND id=$2`, scope, claim.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.ScheduleRetry(ctx, *replacement, "unknown", "WORKSPACE_RETENTION_REQUIRED", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	worker := reconcile.New(&cancellationRetentionStore{Repository: repo, scope: scope}, driver, "retention-final-owner")
	if ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil || !ran {
		t.Fatal("final cancellation reconciliation", ran, err)
	}
	invocation, err := repo.GetInvocation(ctx, scope, claim.ResourceID)
	if err != nil || invocation.State != "cancelled" {
		t.Fatal("cancellation did not finish", invocation.State, err)
	}
	instance, err := executionpostgres.New(pool).GetExecutionByOperation(ctx, scope, claim.ID)
	if err != nil || instance.State != "ready" || instance.ID != stopped.ExecutionID || provider.calls != 1 || objects.writes != 1 {
		t.Fatal("retention deleted or repeated execution", err)
	}
}

type cancellationRetentionTransport struct {
	*exportCaptureProvider
	t *testing.T
}

func (p *cancellationRetentionTransport) Observe(context.Context, execution.ExecutionRef) (execution.Observation, error) {
	p.t.Fatal("retention must use the original owner receipt")
	return execution.Observation{}, errors.New("unexpected observation")
}
func (p *cancellationRetentionTransport) Terminate(context.Context, execution.ExecutionRef) error {
	p.t.Fatal("retention must not delete the sandbox")
	return errors.New("unexpected termination")
}

type cancellationRetentionStore struct {
	*persistence.Repository
	scope string
}

func (s *cancellationRetentionStore) ClaimNext(ctx context.Context, kind, owner string, lease time.Duration) (*persistence.OperationClaim, error) {
	return s.ClaimNextInIsolationDomain(ctx, kind, s.scope, owner, lease)
}
