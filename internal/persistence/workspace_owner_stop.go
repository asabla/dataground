package persistence

import (
	"context"
)

const WorkspaceOwnerStopContract = "dataground.invocation-workspace-owner-stop/v1"

// RecordInvocationWorkspaceOwnerStop is a database-authorized observation
// boundary. Its caller must verify the original native connection's terminal
// exchange. An exact retained intent is required even after cancellation or
// expiry. This receipt supplies no current lease, export or deletion authority.
func (repository *Repository) RecordInvocationWorkspaceOwnerStop(ctx context.Context, expected InvocationWorkspaceQuiescence) error {
	if !repository.Configured() || ctx == nil || expected.SchemaVersion != WorkspaceQuiescenceContract || !workspaceOwnerBindingPattern.MatchString(expected.OwnerBinding) {
		return ErrWorkspaceQuiescence
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return ErrWorkspaceQuiescence
	}
	defer tx.Rollback(ctx)
	// Serialize exact replay without acquiring or extending an execution lease.
	var operation string
	if err := tx.QueryRow(ctx, `SELECT operation_id FROM invocation_workspace_quiescence_intents WHERE isolation_domain_id=$1 AND operation_id=$2 FOR UPDATE`, expected.Source.IsolationDomainID, expected.OperationID).Scan(&operation); err != nil {
		return ErrWorkspaceQuiescence
	}
	entry, found, err := readWorkspaceQuiescence(ctx, tx, expected.Source.IsolationDomainID, expected.OperationID)
	if err != nil || !found || entry.Record != expected {
		return ErrWorkspaceQuiescence
	}
	result, err := tx.Exec(ctx, `INSERT INTO invocation_workspace_owner_stop_receipts(isolation_domain_id,operation_id,contract,owner_binding,runtime_token,actor_id,correlation_id)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, expected.Source.IsolationDomainID, expected.OperationID, WorkspaceOwnerStopContract, expected.OwnerBinding, expected.RuntimeToken, expected.ActorID, expected.CorrelationID)
	if err != nil {
		return ErrWorkspaceQuiescence
	}
	if result.RowsAffected() == 1 {
		if err := writeOutboxAndAudit(ctx, tx, expected.Source.IsolationDomainID, "workspace-quiescence", expected.OperationID, "workspace.owner-stop.recorded", expected.ActorID, expected.CorrelationID, "succeeded", expected.OperationID, repository.now()); err != nil {
			return ErrWorkspaceQuiescence
		}
	}
	if tx.Commit(ctx) != nil {
		return ErrWorkspaceQuiescence
	}
	return nil
}

// GetInvocationWorkspaceOwnerStop returns historical evidence only. A new
// worker cannot turn a missing acknowledgement into a receipt by reconnecting.
func (repository *Repository) GetInvocationWorkspaceOwnerStop(ctx context.Context, scope, operation string) (InvocationWorkspaceQuiescence, error) {
	if !repository.Configured() || ctx == nil {
		return InvocationWorkspaceQuiescence{}, ErrWorkspaceQuiescence
	}
	var binding, actor, correlation string
	var token int64
	err := repository.pool.QueryRow(ctx, `SELECT owner_binding,runtime_token,actor_id,correlation_id FROM invocation_workspace_owner_stop_receipts WHERE isolation_domain_id=$1 AND operation_id=$2 AND contract=$3`, scope, operation, WorkspaceOwnerStopContract).Scan(&binding, &token, &actor, &correlation)
	if err != nil {
		return InvocationWorkspaceQuiescence{}, ErrWorkspaceQuiescence
	}
	entry, found, err := readWorkspaceQuiescence(ctx, repository.pool, scope, operation)
	if err != nil || !found || entry.Record.OwnerBinding != binding || entry.Record.RuntimeToken != token || entry.Record.ActorID != actor || entry.Record.CorrelationID != correlation {
		return InvocationWorkspaceQuiescence{}, ErrWorkspaceQuiescence
	}
	return entry.Record, nil
}
