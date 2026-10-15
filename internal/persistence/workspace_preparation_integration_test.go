package persistence_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testWorkspacePreparation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claim *persistence.OperationClaim, effect persistence.EffectRecord, record workspace.SourceRecord, objects workspace.SourceObjects) persistence.InvocationWorkspacePreparation {
	t.Helper()
	repo := persistence.NewRepository(pool)
	store := executionpostgres.New(pool)
	gateway, err := store.RegisterGateway(ctx, execution.GatewayRegistration{IsolationDomainID: claim.IsolationDomainID, ID: identity.New("gtw"), Endpoint: "http://127.0.0.1:8080", Driver: "docker", Capabilities: []string{"codex.app-server"}})
	if err != nil {
		t.Fatal(err)
	}
	placement, err := store.ReservePlacement(ctx, execution.PlacementRequest{IsolationDomainID: claim.IsolationDomainID, OperationID: claim.ID, RequiredCapabilities: []string{"codex.app-server"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := execution.ExecutionRef{IsolationDomainID: claim.IsolationDomainID, ID: identity.New("exe")}
	if err := store.SaveExecution(ctx, execution.ExecutionRecord{Execution: execution.Execution{IsolationDomainID: ref.IsolationDomainID, ID: ref.ID, GatewayID: gateway.ID, State: "ready"}, OperationID: claim.ID, PlacementID: placement.ID, SandboxName: "source-preparation"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BeginInvocationRuntimeAttempt(ctx, *claim, effect); err == nil {
		t.Fatal("source runtime reserved before preparation")
	}
	if _, err := repo.CheckInvocationWorkspacePreparation(ctx, *claim, effect); err == nil {
		t.Fatal("source runtime passed without preparation")
	}
	preparation, err := repo.BeginInvocationWorkspacePreparation(ctx, *claim, effect, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := repo.BeginInvocationWorkspacePreparation(ctx, *claim, effect, ref, 1)
	if err != nil || replay.Source != record || replay.Execution != ref || replay.PlanDigest != preparation.PlanDigest {
		t.Fatal("preparation replay", err)
	}
	if _, err := repo.BeginInvocationWorkspacePreparation(ctx, *claim, effect, ref, 2); err == nil {
		t.Fatal("preparation grant changed")
	}
	if _, err := repo.BeginInvocationRuntimeAttempt(ctx, *claim, effect); err == nil {
		t.Fatal("runtime reserved from incomplete preparation")
	}
	old := *claim
	claim.LeaseOwner = "source-preparation-replacement"
	claim.FencingToken++
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_owner=$3,lease_token=$4,lease_expires_at=clock_timestamp()+interval '1 minute' WHERE isolation_domain_id=$1 AND id=$2`, claim.IsolationDomainID, claim.ID, claim.LeaseOwner, claim.FencingToken); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ReadPreparingWorkspaceSource(ctx, old, effect, objects, preparation, &reconcile.SourceCedarEvaluator{}); err == nil {
		t.Fatal("stale preparation read source")
	}
	preparation, err = repo.BeginInvocationWorkspacePreparation(ctx, *claim, effect, ref, 1)
	if err != nil {
		t.Fatal("replacement preparation", err)
	}
	var shortened time.Time
	observed := &sourceReadObserver{SourceObjects: objects, beforeRead: func() {
		if err := pool.QueryRow(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()+interval '30 seconds' WHERE isolation_domain_id=$1 AND id=$2 RETURNING lease_expires_at`, claim.IsolationDomainID, claim.ID).Scan(&shortened); err != nil {
			t.Fatal(err)
		}
	}}
	snapshot, expires, err := repo.ReadPreparingWorkspaceSource(ctx, *claim, effect, observed, preparation, &reconcile.SourceCedarEvaluator{})
	if expires.After(shortened) {
		t.Fatal("transfer deadline ignored shortened lease")
	}

	if err != nil || snapshot == nil || !expires.After(time.Now()) || expires.After(preparation.ExpiresAt) {
		t.Fatal("preparation source read", err)
	}
	parent := t.TempDir()
	receipt, err := snapshot.Materialize(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if verified, err := snapshot.Verify(ctx, parent); err != nil || verified != receipt {
		t.Fatal("lost preparation acknowledgement could not be observed", err)
	}
	if err := repo.CompleteInvocationWorkspacePreparation(ctx, *claim, effect, preparation, receipt, unavailableSourceEvaluator{}); err == nil {
		t.Fatal("unavailable Cedar completed preparation")
	}
	if _, err := repo.CheckInvocationWorkspacePreparation(ctx, *claim, effect); err == nil {
		t.Fatal("unavailable authorization opened runtime gate")
	}
	var admission string
	if err := pool.QueryRow(ctx, `SELECT id FROM workspace_source_use_decisions WHERE isolation_domain_id=$1 AND operation_id=$2 AND claim_token=$3 AND claim_contract='dataground.invocation-source-read/v4' AND phase='read-admission' AND outcome='allowed' LIMIT 1`, claim.IsolationDomainID, claim.ID, claim.FencingToken).Scan(&admission); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO invocation_workspace_completions(isolation_domain_id,operation_id,fencing_token,decision_id) VALUES($1,$2,$3,$4)`, claim.IsolationDomainID, claim.ID, claim.FencingToken, admission); err == nil {
		t.Fatal("admission decision substituted for completed delivery")
	}
	wrong := receipt
	wrong.SourceBytes++
	if err := repo.CompleteInvocationWorkspacePreparation(ctx, *claim, effect, preparation, wrong, &reconcile.SourceCedarEvaluator{}); err == nil {
		t.Fatal("wrong helper receipt accepted")
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_workspace_completion_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='workspace.source.preparation.completed' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_workspace_completion_audit BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_workspace_completion_audit()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_workspace_completion_audit() CASCADE`)
	if err := repo.CompleteInvocationWorkspacePreparation(ctx, *claim, effect, preparation, receipt, &reconcile.SourceCedarEvaluator{}); err == nil {
		t.Fatal("completion survived failed audit")
	}
	if _, err := repo.CheckInvocationWorkspacePreparation(ctx, *claim, effect); err == nil {
		t.Fatal("failed completion audit opened runtime gate")
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_workspace_completion_audit() CASCADE`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := repo.CompleteInvocationWorkspacePreparation(ctx, *claim, effect, preparation, receipt, &reconcile.SourceCedarEvaluator{}); err != nil {
			t.Fatal("complete preparation", err)
		}
	}
	if required, err := repo.CheckInvocationWorkspacePreparation(ctx, *claim, effect); err != nil || !required {
		t.Fatal("completed source gate", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND event_type='workspace.source.preparation.started'`, claim.IsolationDomainID).Scan(&count); err != nil || count != 2 {
		t.Fatal("replacement preparation outbox missing", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='workspace.source.preparation.completed'`, claim.IsolationDomainID).Scan(&count); err != nil || count != 1 {
		t.Fatal("completion audit duplicated", count, err)
	}
	for _, sql := range []string{`DELETE FROM invocation_workspace_preparations WHERE isolation_domain_id=$1`, `DELETE FROM invocation_workspace_completions WHERE isolation_domain_id=$1`, `UPDATE invocation_workspace_preparations SET grant_generation=2 WHERE isolation_domain_id=$1`} {
		if _, err := pool.Exec(ctx, sql, claim.IsolationDomainID); err == nil {
			t.Fatal("preparation evidence mutable")
		}
	}
	previous := *claim
	claim.LeaseOwner = "source-preparation-completed-replacement"
	claim.FencingToken++
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_owner=$3,lease_token=$4,lease_expires_at=clock_timestamp()+interval '1 minute' WHERE isolation_domain_id=$1 AND id=$2`, claim.IsolationDomainID, claim.ID, claim.LeaseOwner, claim.FencingToken); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CheckInvocationWorkspacePreparation(ctx, *claim, effect); err == nil {
		t.Fatal("replacement reused prior completion without read-back")
	}
	if err := repo.CompleteInvocationWorkspacePreparation(ctx, previous, effect, preparation, receipt, &reconcile.SourceCedarEvaluator{}); err == nil {
		t.Fatal("replaced completed worker retained authority")
	}
	preparation, err = repo.BeginInvocationWorkspacePreparation(ctx, *claim, effect, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AuthorizeInvocationWorkspaceTransfer(ctx, *claim, effect, preparation, unavailableSourceEvaluator{}); err == nil {
		t.Fatal("unavailable source policy permitted transfer")
	}
	testWorkspaceProvisioningDriver(t, ctx, repo, store, *claim, effect, preparation, objects, parent)
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND event_type='workspace.source.preparation.completed'`, claim.IsolationDomainID).Scan(&count); err != nil || count != 2 {
		t.Fatal("replacement completion outbox missing", count, err)
	}
	return preparation

}

// The real source catalog, policy, grants, lease and completion transaction are
// composed here. The transport verifies the actual local tree; SSH and Linux
// helper execution remain covered by the separate OpenShell transport suite.
func testWorkspaceProvisioningDriver(t *testing.T, ctx context.Context, repo *persistence.Repository, executions *executionpostgres.Store, claim persistence.OperationClaim, effect persistence.EffectRecord, preparation persistence.InvocationWorkspacePreparation, objects workspace.SourceObjects, parent string) {
	t.Helper()
	target, err := repo.GetClaimedInvocationRuntimeTarget(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := reconcile.NewInvocationWorkspacePreparer(repo, objects, reconcile.InvocationWorkspaceConfig{Scope: reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: target.IsolationDomainID, ServiceID: target.ServiceID, RevisionID: target.RevisionID}, Source: preparation.Source, PlanDigest: preparation.PlanDigest, GrantGeneration: preparation.GrantGeneration})
	if err != nil {
		t.Fatal(err)
	}
	transport := &workspacePreparationTransport{parent: parent, loseAcknowledgement: true}
	driver, err := reconcile.NewInvocationRuntimeDriver(repo, transport, reconcile.InvocationRuntimeRequestBuilderFunc(func(persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error) {
		return dgruntime.StartRequest{Prompt: "source fixture", ApprovalMode: dgruntime.ApprovalLocked, SandboxMode: dgruntime.SandboxWorkspaceWrite}, nil
	}), executions, transport, transport, transport, reconcile.InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second, Readiness: func(context.Context) error { return nil }, WorkspacePreparer: preparer})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.ApplyClaimed(ctx, claim, effect); !errors.Is(err, persistence.ErrWorkspacePreparation) {
		t.Fatal("lost acknowledgement accepted", err)
	}
	if _, err := repo.CheckInvocationWorkspacePreparation(ctx, claim, effect); err == nil {
		t.Fatal("unconfirmed transfer completed preparation")
	}
	transport.loseAcknowledgement = false
	transport.authorizations = 0
	if _, err := driver.ApplyClaimed(ctx, claim, effect); !errors.Is(err, reconcile.ErrEffectDenied) {
		t.Fatal("runtime authority change ignored", err)
	}
	if required, err := repo.CheckInvocationWorkspacePreparation(ctx, claim, effect); err != nil || !required {
		t.Fatal("recovered source not completed", err)
	}
	if _, err := repo.GetInvocationRuntimeAttempt(ctx, claim.IsolationDomainID, claim.ID); !errors.Is(err, persistence.ErrInvocationRuntimeAttemptMissing) {
		t.Fatal("denied startup reserved a turn", err)
	}
	if transport.transfers != 2 {
		t.Fatal("transfer recovery count", transport.transfers)
	}
}

type workspacePreparationTransport struct {
	questionBridgeTransport
	parent                    string
	loseAcknowledgement       bool
	authorizations, transfers int
}

func (t *workspacePreparationTransport) AuthorizeInvocationRuntime(context.Context, persistence.InvocationRuntimeTarget, dgruntime.StartRequest) error {
	t.authorizations++
	if t.authorizations == 4 {
		return reconcile.ErrInvocationRuntimeDenied
	}
	return nil
}
func (t *workspacePreparationTransport) MaterializeWorkspace(ctx context.Context, _ execution.ExecutionRef, snapshot *workspace.Snapshot) (workspace.Receipt, error) {
	t.transfers++
	receipt, err := snapshot.Verify(ctx, t.parent)
	if err == nil && t.loseAcknowledgement {
		return workspace.Receipt{}, errors.New("lost helper acknowledgement")
	}
	return receipt, err
}
func (*workspacePreparationTransport) StartRuntime(context.Context, execution.ExecutionRef) (execution.RuntimeSession, error) {
	panic("source preparation must not bypass changed runtime authority")
}
