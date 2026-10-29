package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/nativehost"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This replacement has no native session. Source, policy, claim and retention
// checks use PostgreSQL; the original cleanup remains a trusted fixture proof.
func recoverWorkspaceThroughRuntimeDriver(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claim persistence.OperationClaim, effect persistence.EffectRecord, config reconcile.InvocationWorkspaceConfig, source *exportSourceObjects, objects *exportArtifactObjects, provider *exportCaptureProvider) {
	t.Helper()
	repo := persistence.NewRepository(pool)
	preparer, err := reconcile.NewInvocationWorkspacePreparer(repo, source, config)
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := reconcile.NewInvocationWorkspaceExporter(repo, source, objects, objects, config)
	if err != nil {
		t.Fatal(err)
	}
	completion, err := reconcile.NewInvocationWorkspaceCompletion(repo, exporter)
	if err != nil {
		t.Fatal(err)
	}
	transport := &workspaceCompletionRecoveryTransport{exportCaptureProvider: provider}
	driver, err := reconcile.NewInvocationRuntimeDriver(repo, transport, reconcile.InvocationRuntimeRequestBuilderFunc(func(persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error) {
		return dgruntime.StartRequest{Prompt: "recovery fixture", ApprovalMode: dgruntime.ApprovalLocked, SandboxMode: dgruntime.SandboxWorkspaceWrite}, nil
	}), executionpostgres.New(pool), transport, transport, transport, reconcile.InvocationRuntimeDriverConfig{WorkspacePreparer: preparer, WorkspaceCompletion: completion, Readiness: func(context.Context) error { return nil }, LeaseDuration: time.Minute, RenewInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, found, err := driver.ObserveClaimed(ctx, claim, effect)
	if err != nil || !found || result["status"] != "succeeded" {
		t.Fatal("driver failed to recover retained native result and workspace export", found, err)
	}
}

type workspaceCompletionRecoveryTransport struct {
	questionBridgeTransport
	*exportCaptureProvider
}

func (*workspaceCompletionRecoveryTransport) AuthorizeInvocationRuntime(context.Context, persistence.InvocationRuntimeTarget, dgruntime.StartRequest) error {
	return nil
}
func (*workspaceCompletionRecoveryTransport) StartOwnedRuntime(context.Context, execution.ExecutionRef) (*nativehost.Session, error) {
	panic("replacement must not start a native owner")
}
func (*workspaceCompletionRecoveryTransport) StartRuntime(context.Context, execution.ExecutionRef) (execution.RuntimeSession, error) {
	panic("replacement must not start an unowned runtime")
}
func (*workspaceCompletionRecoveryTransport) MaterializeWorkspace(context.Context, execution.ExecutionRef, *workspace.Snapshot) (workspace.Receipt, error) {
	panic("replacement must not rematerialize the source")
}
