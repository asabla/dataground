package reconcile

import (
	"context"
	"errors"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
)

type InvocationWorkspaceCancellationExportStore interface {
	GetInvocationWorkspaceOwnerStop(context.Context, string, string) (persistence.InvocationWorkspaceQuiescence, error)
	AuthorizeCancellationWorkspaceChangeExport(context.Context, persistence.OperationClaim, persistence.EffectRecord, persistence.InvocationWorkspaceQuiescence, string, workspace.ChangeSetReceipt, persistence.WorkspaceChangeExportEvaluator) (persistence.WorkspaceChangeExportPermit, error)
	ReadCancellationWorkspaceSource(context.Context, persistence.OperationClaim, persistence.EffectRecord, workspace.SourceObjectReader, persistence.InvocationWorkspaceQuiescence, int64, persistence.SourceReadEvaluator) (*workspace.Snapshot, time.Time, error)
	PrepareCancellationWorkspaceChangeExport(context.Context, persistence.OperationClaim, persistence.EffectRecord, persistence.InvocationWorkspaceQuiescence, workspace.ChangeExportBinding) (workspace.ChangeExportEntry, error)
	CompleteCancellationWorkspaceChangeExport(context.Context, persistence.OperationClaim, persistence.EffectRecord, persistence.InvocationWorkspaceQuiescence, workspace.ChangeExportBinding) (workspace.ChangeExportEntry, error)
}

// RetainCancellation exports only the original stopped workspace. The caller
// supplies current cancellation and accepted-image checks. Its current claim
// bounds each read and retention effect; it cannot restore execution authority.
func (value *InvocationWorkspaceCompletion) RetainCancellation(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, ref execution.ExecutionRef, provider InvocationWorkspaceChangeProvider, check func(context.Context) error) (workspace.ChangeExportEntry, error) {
	if !value.valid() || ctx == nil || ctx.Err() != nil || check == nil || governedInvocationDependencyMissing(provider) || !workspaceCancellationEffectMatchesClaim(claim, effect) {
		return workspace.ChangeExportEntry{}, ErrInvocationCancellationRetentionRequired
	}
	e := value.exporter
	store, ok := e.store.(InvocationWorkspaceCancellationExportStore)
	if !ok || governedInvocationDependencyMissing(store) || claim.IsolationDomainID != e.config.Scope.IsolationDomainID || ref.IsolationDomainID != claim.IsolationDomainID || ref.ID == "" {
		return workspace.ChangeExportEntry{}, ErrInvocationCancellationRetentionRequired
	}
	if err := check(ctx); err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	stopped, err := store.GetInvocationWorkspaceOwnerStop(ctx, claim.IsolationDomainID, claim.ID)
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	if stopped.SchemaVersion != persistence.WorkspaceQuiescenceContract || stopped.Source != e.config.Source || stopped.PlanDigest != e.config.PlanDigest || stopped.InvocationID != claim.ResourceID || stopped.OperationID != claim.ID || stopped.ExecutionID != ref.ID {
		return workspace.ChangeExportEntry{}, persistence.ErrWorkspaceQuiescence
	}
	guard := &workspaceExportGuard{exporter: e, claim: claim, effect: effect, cleanup: stopped, check: check, cancellation: store}
	return guard.export(ctx, ref, provider)
}
func workspaceCancellationEffectMatchesClaim(claim persistence.OperationClaim, effect persistence.EffectRecord) bool {
	return effectMatchesClaim(effect, claim) && claim.Kind == persistence.OperationKindInvocation &&
		claim.Command == "cancel" && claim.ObservedState == "cancelling" && claim.StateMachineVersion == 2 &&
		effect.Phase == "cancel-invocation" && effect.EffectID != "" &&
		(effect.Status == "prepared" || effect.Status == "failed" || effect.Status == "unknown" || effect.Status == "succeeded")
}

// NewOwnedInvocationCancellationDriver adds retention to the explicit owned
// worker. A successful cancellation stops native work and retains its changes;
// it deliberately leaves the sandbox for separately authorized teardown.
func NewOwnedInvocationCancellationDriver(targets InvocationCancellationTargetStore, authorizer InvocationCancellationAuthorizer, executions executionByOperationSource, provider invocationCancellationProvider, completion *InvocationWorkspaceCompletion, ready func(context.Context) error) (*InvocationCancellationDriver, error) {
	driver, err := NewInvocationCancellationDriver(targets, authorizer, executions, provider)
	if err != nil {
		return nil, err
	}
	if !completion.valid() || ready == nil {
		return nil, ErrInvocationCancellationRetentionRequired
	}
	if store, ok := completion.exporter.store.(InvocationWorkspaceCancellationExportStore); !ok || governedInvocationDependencyMissing(store) {
		return nil, ErrInvocationCancellationRetentionRequired
	}
	if capture, ok := provider.(InvocationWorkspaceChangeProvider); !ok || governedInvocationDependencyMissing(capture) {
		return nil, ErrInvocationCancellationRetentionRequired
	}
	driver.workspaceCompletion, driver.readiness = completion, ready
	return driver, nil
}
func (driver *InvocationCancellationDriver) ObserveClaimed(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord) (map[string]any, bool, error) {
	if driver.workspaceCompletion == nil {
		return driver.Observe(ctx, effect)
	}
	if !workspaceCancellationEffectMatchesClaim(claim, effect) {
		return nil, false, ErrEffectInvalid
	}
	target, err := driver.target(ctx, effect)
	if err != nil {
		return nil, false, err
	}
	if !target.WorkspaceSourceSelected {
		return driver.Observe(ctx, effect)
	}
	// Apply performs exact immutable-object observation before any capture/write.
	// A catalog receipt alone cannot confirm the stored private bytes still exist.
	return nil, false, nil
}
func (driver *InvocationCancellationDriver) ApplyClaimed(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord) (map[string]any, error) {
	if driver.workspaceCompletion == nil {
		return driver.Apply(ctx, effect)
	}
	if !workspaceCancellationEffectMatchesClaim(claim, effect) {
		return nil, ErrEffectInvalid
	}
	target, err := driver.target(ctx, effect)
	if err != nil {
		return nil, err
	}
	if !target.WorkspaceSourceSelected {
		return driver.Apply(ctx, effect)
	}
	if target.InvocationID != claim.ResourceID || target.ActorID != claim.ActorID || target.CorrelationID != claim.CorrelationID {
		return nil, ErrInvocationCancellationTargetMismatch
	}
	value, found, err := driver.execution(ctx, target)
	if err != nil {
		return nil, err
	}
	if !found {
		return invocationCancellationAbsentObservation(), nil
	}
	check := func(ctx context.Context) error {
		if err := driver.readiness(ctx); err != nil {
			return err
		}
		return driver.authorizer.AuthorizeInvocationCancellation(ctx, target)
	}
	entry, err := driver.workspaceCompletion.RetainCancellation(ctx, claim, effect, execution.ExecutionRef{IsolationDomainID: value.IsolationDomainID, ID: value.ID}, driver.provider.(InvocationWorkspaceChangeProvider), check)
	if err != nil {
		return nil, errors.Join(ErrAmbiguousEffect, ErrInvocationCancellationRetentionRequired, err)
	}
	return map[string]any{"executionId": value.ID, "state": "stopped", "workspaceRetained": true, "workspaceChangesArtifactId": entry.Record.ArtifactRecord().ID}, nil
}

var _ ClaimedEffectDriver = (*InvocationCancellationDriver)(nil)

var _ InvocationWorkspaceCancellationExportStore = (*persistence.Repository)(nil)
