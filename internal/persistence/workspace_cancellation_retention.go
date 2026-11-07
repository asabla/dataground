package persistence

import (
	"context"
	"encoding/json"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5"
)

const WorkspaceCancellationExportContract = "dataground.workspace-cancellation-export-authorization/v1"

// Cancellation retention uses the current cancellation claim, never a revived
// execution claim. The original stop receipt supplies evidence, not authority.
func validWorkspaceCancellationClaim(claim OperationClaim, effect EffectRecord, stopped InvocationWorkspaceQuiescence) bool {
	return claim.Kind == OperationKindInvocation && claim.StateMachineVersion == 2 &&
		claim.Command == "cancel" && claim.ObservedState == "cancelling" &&
		claim.LeaseOwner != "" && claim.FencingToken > 0 && claim.ActorID != "" && claim.CorrelationID != "" &&
		stopped.SchemaVersion == WorkspaceQuiescenceContract && workspace.ValidSourceRecord(stopped.Source) && stopped.Source.IsolationDomainID == claim.IsolationDomainID && stopped.OperationID == claim.ID && stopped.InvocationID == claim.ResourceID &&
		effect.IsolationDomainID == claim.IsolationDomainID && effect.OperationKind == claim.Kind && effect.OperationID == claim.ID && effect.Phase == "cancel-invocation" && workspaceCancellationEffectStatus(effect.Status) &&
		effect.EffectID == identity.Derived("eff", claim.IsolationDomainID+":"+claim.Kind+":"+claim.ID+":cancel-invocation")
}

func lockWorkspaceCancellationTarget(ctx context.Context, tx pgx.Tx, claim OperationClaim, effect EffectRecord, stopped InvocationWorkspaceQuiescence) (InvocationRuntimeTarget, time.Time, error) {
	if !validWorkspaceCancellationClaim(claim, effect, stopped) {
		return InvocationRuntimeTarget{}, time.Time{}, ErrWorkspaceExportDenied
	}
	retained, err := getEffect(ctx, tx, effect.IsolationDomainID, effect.EffectID)
	if err != nil || retained.OperationID != effect.OperationID || retained.OperationKind != effect.OperationKind || retained.Phase != effect.Phase || retained.RequestDigest != effect.RequestDigest || retained.Status != effect.Status || !workspaceCancellationEffectStatus(retained.Status) {
		return InvocationRuntimeTarget{}, time.Time{}, ErrWorkspaceExportDenied
	}
	encoded, err := json.Marshal(stopped)
	if err != nil {
		return InvocationRuntimeTarget{}, time.Time{}, ErrWorkspaceExportUnavailable
	}
	var expires time.Time
	if err := tx.QueryRow(ctx, `SELECT lock_workspace_cancellation_retention($1,$2,$3,$4,$5,$6,$7)`, claim.IsolationDomainID, claim.ID, claim.LeaseOwner, claim.FencingToken, claim.ActorID, claim.CorrelationID, encoded).Scan(&expires); err != nil {
		return InvocationRuntimeTarget{}, time.Time{}, ErrWorkspaceExportDenied
	}
	var service, revision string
	if err := tx.QueryRow(ctx, `SELECT service_id,revision_id FROM invocations WHERE isolation_domain_id=$1 AND id=$2`, claim.IsolationDomainID, claim.ResourceID).Scan(&service, &revision); err != nil {
		return InvocationRuntimeTarget{}, time.Time{}, ErrWorkspaceExportUnavailable
	}
	return InvocationRuntimeTarget{IsolationDomainID: claim.IsolationDomainID, OperationID: claim.ID, InvocationID: claim.ResourceID, ServiceID: service, RevisionID: revision, ActorID: claim.ActorID, CorrelationID: claim.CorrelationID, StateMachineVersion: claim.StateMachineVersion}, expires, nil
}

// PrepareCancellationWorkspaceChangeExport requires an internal database-authorized
// caller. Historical completed replay is read-only; effect callers must separately
// reauthorize and verify the stored object before reporting retention success.
func (repository *Repository) PrepareCancellationWorkspaceChangeExport(ctx context.Context, claim OperationClaim, effect EffectRecord, stopped InvocationWorkspaceQuiescence, binding workspace.ChangeExportBinding) (workspace.ChangeExportEntry, error) {
	if !cancellationExportBindingMatches(claim, effect, stopped, binding) {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	return repository.prepareWorkspaceChangeExport(ctx, binding, cancellationExportLocker(claim, effect, stopped))
}

// CompleteCancellationWorkspaceChangeExport shares normal immutable publication
// and exact historical replay, but gates new completion on the cancellation claim.
func (repository *Repository) CompleteCancellationWorkspaceChangeExport(ctx context.Context, claim OperationClaim, effect EffectRecord, stopped InvocationWorkspaceQuiescence, binding workspace.ChangeExportBinding) (workspace.ChangeExportEntry, error) {
	if !cancellationExportBindingMatches(claim, effect, stopped, binding) {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	return repository.completeWorkspaceChangeExport(ctx, binding, cancellationExportLocker(claim, effect, stopped))
}
func cancellationExportLocker(claim OperationClaim, effect EffectRecord, stopped InvocationWorkspaceQuiescence) workspaceChangeExportLocker {
	return func(ctx context.Context, tx pgx.Tx, b workspace.ChangeExportBinding) ([]byte, error) {
		r := b.Record
		if !cancellationExportBindingMatches(claim, effect, stopped, b) {
			return nil, workspace.ErrChangeExportConflict
		}
		if _, _, err := lockWorkspaceCancellationTarget(ctx, tx, claim, effect, stopped); err != nil {
			return nil, err
		}
		return json.Marshal(r)
	}
}

func cancellationExportBindingMatches(claim OperationClaim, effect EffectRecord, stopped InvocationWorkspaceQuiescence, b workspace.ChangeExportBinding) bool {
	r := b.Record
	return validWorkspaceCancellationClaim(claim, effect, stopped) &&
		r.Source == stopped.Source && r.InvocationID == stopped.InvocationID && r.OperationID == stopped.OperationID &&
		r.EffectID == stopped.EffectID && r.ExecutionID == stopped.ExecutionID && r.PlanDigest == stopped.PlanDigest &&
		b.ActorID == claim.ActorID && b.CorrelationID == claim.CorrelationID &&
		b.LeaseOwner == claim.LeaseOwner && b.FencingToken == claim.FencingToken && b.StateMachineVersion == claim.StateMachineVersion
}

// A recorded success can precede the final operation transition. Its replacement
// still verifies the original stop, current authority and retained object bytes.
func workspaceCancellationEffectStatus(status string) bool {
	return invocationRuntimeEffectMayStartAttempt(status) || status == "succeeded"
}
