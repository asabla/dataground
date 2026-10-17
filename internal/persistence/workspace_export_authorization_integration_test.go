package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5/pgxpool"
)

type exportEvaluatorFunc func(context.Context, persistence.InvocationAuthorizationPolicyRecord, authz.WorkspaceChangeExportRequest) authz.Outcome

func (f exportEvaluatorFunc) EvaluateWorkspaceChangeExport(c context.Context, p persistence.InvocationAuthorizationPolicyRecord, r authz.WorkspaceChangeExportRequest) authz.Outcome {
	return f(c, p, r)
}

// This fixture supplies real source bytes and a trusted cleanup observation.
// It does not establish filesystem quiescence, image acceptance, or publication readiness.
func TestWorkspaceExportAuthorizationRequiresCurrentClaimAndConfirmedCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	defer func() {
		_, err := pool.Exec(context.Background(), `TRUNCATE workspace_change_export_decisions,invocation_workspace_quiescence_completions,invocation_workspace_quiescence_intents,invocation_workspace_change_completions,invocation_workspace_change_exports,invocation_workspace_completions,invocation_workspace_preparations,service_revision_execution_plans,workspace_source_grants,workspace_source_use_decisions,workspace_source_completions,workspace_source_installations,workspaces,invocation_authorization_entity_activations,invocation_authorization_entity_generations,invocation_authorization_policy_withdrawals,invocation_authorization_policies`)
		if err != nil {
			t.Error(err)
		}
	}()
	for _, version := range []string{"v7", "v6"} {
		t.Run(version, func(t *testing.T) { testWorkspaceExportAuthorization(t, ctx, pool, version) })
	}
}

func testWorkspaceExportAuthorization(t *testing.T, ctx context.Context, pool *pgxpool.Pool, version string) {
	repo := persistence.NewRepository(pool)
	scope, service, revision := identity.New("iso"), identity.New("svc"), identity.New("rev")
	source, sourceObjects, base := prepareExportSourceFixture(t, ctx, repo, scope)
	if _, err := pool.Exec(ctx, `INSERT INTO agent_services(isolation_domain_id,id,name,created_at,updated_at,created_by) VALUES($1,$2,'Export authorization',now(),now(),'fixture')`, scope, service); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO service_revisions(isolation_domain_id,id,service_id,revision_number,state,runtime_profile,created_at,updated_at,created_by) VALUES($1,$2,$3,1,'draft','codex',now(),now(),'fixture')`, scope, revision, service); err != nil {
		t.Fatal(err)
	}
	store := executionpostgres.New(pool)
	plan := durableExecutionPlan(scope, revision)
	plan.SchemaVersion = execution.ExecutionPlanSchemaV2
	plan.WorkspaceSource = &source
	plan.RuntimeProfile = "codex"
	plan.RequiredCapabilities = []string{}
	if _, err := store.BindExecutionPlan(ctx, execution.ExecutionPlanBinding{Plan: plan, ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='published' WHERE isolation_domain_id=$1 AND id=$2`, scope, revision); err != nil {
		t.Fatal(err)
	}
	policy := installWorkspaceExportPolicy(t, ctx, pool, scope, service, revision, version)
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.ChangeWorkspaceSourceGrant(ctx, workspace.SourceGrantChange{Contract: workspace.SourceGrantV1, Record: source, RevisionID: revision, Generation: 1, Operation: "activate", ActivatedAt: now.Add(-time.Second), ExpiresAt: now.Add(30 * time.Minute), ActorID: "operator", ReasonDigest: "sha256:" + strings.Repeat("a", 64), CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AssignAlias(ctx, testIdempotency(scope, "export-alias"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: service, Name: "source", RevisionID: revision, ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	invocation := identity.New("inv")
	if _, err := repo.AcceptInvocation(ctx, testIdempotency(scope, "export-invocation"), persistence.AcceptInvocationInput{ID: invocation, ServiceID: service, Alias: "source", Input: map[string]any{"prompt": "database fixture"}, ActorID: "source-requester", CorrelationID: identity.New("cor"), Deadline: time.Now().Add(3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	worker := reconcile.New(repo, reconcile.NewReferenceDriver(pool), "export-setup")
	for range 2 {
		if ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil || !ran {
			t.Fatal(ran, err)
		}
	}
	claim, err := repo.ClaimNext(ctx, persistence.OperationKindInvocation, "export-owner", time.Minute)
	if err != nil || claim == nil || claim.ResourceID != invocation {
		t.Fatal("claim", err)
	}
	effect, err := repo.PrepareEffect(ctx, *claim, "run-invocation", sha256.Sum256([]byte("export-runtime")))
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := store.RegisterGateway(ctx, execution.GatewayRegistration{IsolationDomainID: scope, ID: identity.New("gtw"), Endpoint: "http://127.0.0.1:8080", Driver: "docker", Capabilities: []string{"codex.app-server"}})
	if err != nil {
		t.Fatal(err)
	}
	placement, err := store.ReservePlacement(ctx, execution.PlacementRequest{IsolationDomainID: scope, OperationID: claim.ID, RequiredCapabilities: []string{"codex.app-server"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.ExecutionRef{IsolationDomainID: scope, ID: identity.New("exe")}
	if err := store.SaveExecution(ctx, execution.ExecutionRecord{Execution: execution.Execution{IsolationDomainID: scope, ID: ref.ID, GatewayID: gateway.ID, State: "ready"}, OperationID: claim.ID, PlacementID: placement.ID, SandboxName: "export-fixture"}); err != nil {
		t.Fatal(err)
	}
	preparation, err := repo.BeginInvocationWorkspacePreparation(ctx, *claim, effect, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	receipt := workspace.Receipt{SchemaVersion: "dataground.workspace-source-materialization/v1", ArchiveDigest: source.ArchiveDigest, EntryCount: source.EntryCount, SourceBytes: source.SourceBytes}
	if err := repo.CompleteInvocationWorkspacePreparation(ctx, *claim, effect, preparation, receipt, &reconcile.SourceCedarEvaluator{}); err != nil {
		t.Fatal("complete source", err)
	}
	if _, err := repo.BeginInvocationRuntimeAttempt(ctx, *claim, effect); err != nil {
		t.Fatal(err)
	}
	entry, err := repo.BeginInvocationWorkspaceQuiescence(ctx, *claim, effect, ref, "sha256:"+strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	evaluator := &reconcile.WorkspaceExportCedarEvaluator{}
	authorize := func(c persistence.OperationClaim, e persistence.EffectRecord, q persistence.InvocationWorkspaceQuiescence, phase string, changes workspace.ChangeSetReceipt, eval persistence.WorkspaceChangeExportEvaluator) (persistence.WorkspaceChangeExportPermit, error) {
		return persistence.NewRepository(pool).AuthorizeInvocationWorkspaceChangeExport(ctx, c, e, q, phase, changes, eval)
	}
	capture := func() (persistence.WorkspaceChangeExportPermit, error) {
		return authorize(*claim, effect, entry.Record, "capture", workspace.ChangeSetReceipt{}, evaluator)
	}
	if _, err := capture(); !errors.Is(err, persistence.ErrWorkspaceExportDenied) {
		t.Fatal("pending cleanup authorized", err)
	}
	if _, _, err := repo.ReadExportWorkspaceSource(ctx, *claim, effect, sourceObjects, entry.Record, 1, &reconcile.SourceCedarEvaluator{}); err == nil || sourceObjects.reads != 0 {
		t.Fatal("pending cleanup reached source", err)
	}
	if _, err := repo.CompleteInvocationWorkspaceQuiescence(ctx, *claim, effect, entry.Record); err != nil {
		t.Fatal(err)
	}
	permit, err := capture()
	if version == "v6" {
		if !errors.Is(err, persistence.ErrWorkspaceExportDenied) {
			t.Fatal("old wildcard exported", err)
		}
		if _, _, err := repo.ReadExportWorkspaceSource(ctx, *claim, effect, sourceObjects, entry.Record, 1, &reconcile.SourceCedarEvaluator{}); err == nil || sourceObjects.reads != 0 {
			t.Fatal("old policy granted export source read", err)
		}
		return
	}
	if err != nil || permit.DecisionID == "" || !permit.ExpiresAt.After(time.Now()) || permit.Request.RuntimeToken != claim.FencingToken {
		t.Fatal("capture", permit, err)
	}
	var recorded authz.WorkspaceChangeExportRequest
	var data []byte
	var outcome, contract, digest string
	if err := pool.QueryRow(ctx, `SELECT request,outcome,policy_contract,policy_digest FROM workspace_change_export_decisions WHERE isolation_domain_id=$1 AND id=$2`, scope, permit.DecisionID).Scan(&data, &outcome, &contract, &digest); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &recorded); err != nil || recorded != permit.Request || outcome != "allowed" || contract != policy.Contract || digest == "" {
		t.Fatal("decision did not bind exact request", err)
	}
	changes := workspace.ChangeSetReceipt{SchemaVersion: workspace.ChangeSetContract, Digest: "sha256:" + strings.Repeat("d", 64), SizeBytes: 256, BaseArchiveDigest: source.ArchiveDigest, ResultArchiveDigest: "sha256:" + strings.Repeat("e", 64), ChangeCount: 1, EntryCount: 1, SourceBytes: 1}
	if p, err := authorize(*claim, effect, entry.Record, "retain", changes, evaluator); err != nil || p.Request.Changes != changes || p.DecisionID == permit.DecisionID {
		t.Fatal("retention receipt", err)
	}
	for name, mutate := range map[string]func(*persistence.OperationClaim, *persistence.EffectRecord, *persistence.InvocationWorkspaceQuiescence){
		"scope": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *persistence.InvocationWorkspaceQuiescence) {
			c.IsolationDomainID = identity.New("iso")
		},
		"owner": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *persistence.InvocationWorkspaceQuiescence) {
			c.LeaseOwner = "other"
		},
		"token": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *persistence.InvocationWorkspaceQuiescence) {
			c.FencingToken++
		},
		"actor": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *persistence.InvocationWorkspaceQuiescence) {
			c.ActorID = "other"
		},
		"correlation": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *persistence.InvocationWorkspaceQuiescence) {
			c.CorrelationID = identity.New("cor")
		},
		"effect": func(_ *persistence.OperationClaim, e *persistence.EffectRecord, _ *persistence.InvocationWorkspaceQuiescence) {
			e.RequestDigest[0]++
		},
		"execution": func(_ *persistence.OperationClaim, _ *persistence.EffectRecord, q *persistence.InvocationWorkspaceQuiescence) {
			q.ExecutionID = identity.New("exe")
		},
		"owner binding": func(_ *persistence.OperationClaim, _ *persistence.EffectRecord, q *persistence.InvocationWorkspaceQuiescence) {
			q.OwnerBinding = "sha256:" + strings.Repeat("f", 64)
		},
		"plan": func(_ *persistence.OperationClaim, _ *persistence.EffectRecord, q *persistence.InvocationWorkspaceQuiescence) {
			q.PlanDigest = "sha256:" + strings.Repeat("f", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, e, q := *claim, effect, entry.Record
			mutate(&c, &e, &q)
			if _, err := authorize(c, e, q, "capture", workspace.ChangeSetReceipt{}, evaluator); err == nil {
				t.Fatal("substitution authorized")
			}
		})
	}
	for _, phase := range []string{"unknown", "retain"} {
		if _, err := authorize(*claim, effect, entry.Record, phase, workspace.ChangeSetReceipt{}, evaluator); err == nil {
			t.Fatal("invalid phase or receipt authorized")
		}
	}
	bad := changes
	bad.BaseArchiveDigest = "sha256:" + strings.Repeat("f", 64)
	if _, err := authorize(*claim, effect, entry.Record, "retain", bad, evaluator); err == nil {
		t.Fatal("wrong base authorized")
	}
	unavailable := exportEvaluatorFunc(func(context.Context, persistence.InvocationAuthorizationPolicyRecord, authz.WorkspaceChangeExportRequest) authz.Outcome {
		return authz.OutcomeUnavailable
	})
	if _, err := authorize(*claim, effect, entry.Record, "capture", workspace.ChangeSetReceipt{}, unavailable); !errors.Is(err, persistence.ErrWorkspaceExportUnavailable) {
		t.Fatal("unavailable", err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_export_decision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END; $$; CREATE TRIGGER reject_export_decision BEFORE INSERT ON workspace_change_export_decisions FOR EACH ROW EXECUTE FUNCTION reject_export_decision()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_export_decision() CASCADE`)
	if _, err := capture(); !errors.Is(err, persistence.ErrWorkspaceExportUnavailable) {
		t.Fatal("audit failure granted permit", err)
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_export_decision() CASCADE`); err != nil {
		t.Fatal(err)
	}
	changeSourceReadMembership(t, ctx, pool, policy, 1, false)
	if _, err := capture(); !errors.Is(err, persistence.ErrWorkspaceExportDenied) {
		t.Fatal("removed member exported", err)
	}
	changeSourceReadMembership(t, ctx, pool, policy, 2, true)
	if _, err := capture(); err != nil {
		t.Fatal("refreshed membership", err)
	}
	recoverController := beginWorkspaceExportController(t, ctx, pool, *claim, effect, entry.Record, service, revision, sourceObjects, base)
	// Claim replacement must not replace the original stopped process owner.
	old := *claim
	claim.LeaseOwner = "replacement-export-owner"
	claim.FencingToken++
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_owner=$3,lease_token=$4,lease_expires_at=clock_timestamp()+interval '1 minute' WHERE isolation_domain_id=$1 AND id=$2`, scope, claim.ID, claim.LeaseOwner, claim.FencingToken); err != nil {
		t.Fatal(err)
	}
	if _, err := authorize(old, effect, entry.Record, "capture", workspace.ChangeSetReceipt{}, evaluator); err == nil {
		t.Fatal("stale original claim exported")
	}
	if p, err := capture(); err != nil || p.Request.RuntimeToken != old.FencingToken || p.Request.FencingToken != claim.FencingToken {
		t.Fatal("replacement lost original owner", err)
	}
	recoverController(*claim)
	// Expiry during evaluation must commit a denial, even when Cedar allowed it.
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()+interval '100 milliseconds' WHERE isolation_domain_id=$1 AND id=$2`, scope, claim.ID); err != nil {
		t.Fatal(err)
	}
	slow := exportEvaluatorFunc(func(c context.Context, p persistence.InvocationAuthorizationPolicyRecord, r authz.WorkspaceChangeExportRequest) authz.Outcome {
		time.Sleep(150 * time.Millisecond)
		return evaluator.EvaluateWorkspaceChangeExport(c, p, r)
	})
	if _, err := authorize(*claim, effect, entry.Record, "capture", workspace.ChangeSetReceipt{}, slow); !errors.Is(err, persistence.ErrWorkspaceExportDenied) {
		t.Fatal("expired evaluation authorized", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_change_export_decisions WHERE isolation_domain_id=$1 AND policy_outcome='allowed' AND outcome='denied'`, scope).Scan(&count); err != nil || count != 1 {
		t.Fatal("expiry denial missing", count, err)
	}
	if _, err := capture(); err == nil {
		t.Fatal("expired claim exported")
	}
	if q, err := repo.GetInvocationWorkspaceQuiescence(ctx, scope, claim.ID); err != nil || !q.Confirmed {
		t.Fatal("historical cleanup lost", err)
	}
	for _, sql := range []string{`UPDATE workspace_change_export_decisions SET outcome='denied' WHERE isolation_domain_id=$1`, `DELETE FROM workspace_change_export_decisions WHERE isolation_domain_id=$1`} {
		if _, err := pool.Exec(ctx, sql, scope); err == nil {
			t.Fatal("decision evidence mutable")
		}
	}
	db, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := persistence.MigrateDownTo(ctx, db, 73); err == nil {
		t.Fatal("downgrade removed export authority evidence")
	}

	// Restore only the test lease to isolate current policy withdrawal from expiry.
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()+interval '1 minute' WHERE isolation_domain_id=$1 AND id=$2`, scope, claim.ID); err != nil {
		t.Fatal(err)
	}
	reason := sha256.Sum256([]byte("withdraw export fixture"))
	if err := repo.WithdrawInvocationAuthorizationPolicy(ctx, persistence.InvocationAuthorizationPolicyWithdrawal{Contract: persistence.InvocationAuthorizationPolicyWithdrawalContract, IsolationDomainID: scope, ServiceID: service, RevisionID: revision, PolicyDigest: policy.PolicyDigest, WithdrawnBy: "operator", ReasonDigest: reason[:], CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(); !errors.Is(err, persistence.ErrWorkspaceExportDenied) {
		t.Fatal("withdrawn policy exported", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_change_export_decisions WHERE isolation_domain_id=$1 AND policy_contract IS NULL AND policy_outcome='denied' AND outcome='denied'`, scope).Scan(&count); err != nil || count != 1 {
		t.Fatal("withdrawal denial missing", count, err)
	}
	// Keep this intentionally unfinished fixture from being leased by the next subtest.
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()+interval '1 minute' WHERE isolation_domain_id=$1 AND id=$2`, scope, claim.ID); err != nil {
		t.Fatal(err)
	}
}

func installWorkspaceExportPolicy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, scope, service, revision, version string) persistence.InvocationAuthorizationPolicyRecord {
	t.Helper()
	constructor, schema, flag := reconcile.NewInvocationAuthorizationPolicyWithWorkspaceExportEntities, reconcile.CanonicalWorkspaceExportCedarSchema(), "-workspace-export-capable"
	text := `permit(principal in DataGround::Role::"source-reader",action in [DataGround::Action::"read-source",DataGround::Action::"export-changes"],resource);`
	if version == "v6" {
		constructor, schema, flag = reconcile.NewInvocationAuthorizationPolicyWithSourceEntities, reconcile.CanonicalSourceCedarSchema(), "-source-capable"
		text = `permit(principal,action,resource);`
	}
	policy, err := constructor(reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: scope, ServiceID: service, RevisionID: revision}, "export-fixture", schema, []byte(text), sourceReadEntities(t, true))
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	policyPath, entityPath := filepath.Join(directory, "policy.cedar"), filepath.Join(directory, "entities.json")
	if err := os.WriteFile(policyPath, policy.Policies, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entityPath, policy.Entities, 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, "go", "run", "../../cmd/dataground-policy-install", flag, "-isolation-domain", scope, "-service", service, "-revision", revision, "-policy-set", policy.PolicySetID, "-policy-file", policyPath, "-entity-file", entityPath, "-actor", "operator", "-reason", "export authorization fixture", "-correlation-id", identity.New("cor"))
	command.Env = append(os.Environ(), "DATAGROUND_DATABASE_URL="+testDatabaseURL(t))
	if output, err := command.CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatal("export policy installer failed or emitted output", err)
	}
	record, err := persistence.NewRepository(pool).GetActiveInvocationAuthorizationPolicy(ctx, scope, service, revision)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
