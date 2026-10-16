package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5"
)

// PrepareWorkspaceChangeExport requires a database-authorized internal caller.
// It reserves one final artifact for the original prepared runtime workspace.
// A current replacement lease may recover identical intent without restarting
// the runtime. A completed exact replay is read-only after lease expiry.
func (repository *Repository) PrepareWorkspaceChangeExport(ctx context.Context, binding workspace.ChangeExportBinding) (workspace.ChangeExportEntry, error) {
	if !repository.Configured() || ctx == nil || !workspace.ValidChangeExportBinding(binding) {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportUnavailable
	}
	defer tx.Rollback(ctx)
	entry, found, err := readWorkspaceChangeExport(ctx, tx, binding.Record.Source.IsolationDomainID, binding.Record.OperationID)
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	if found && entry.Record != binding.Record {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	if found && entry.Ready {
		return entry, nil
	}
	encoded, err := lockWorkspaceChangeExport(ctx, tx, binding)
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	result, err := tx.Exec(ctx, `INSERT INTO invocation_workspace_change_exports(isolation_domain_id,operation_id,record,lease_owner,fencing_token,actor_id,correlation_id)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, binding.Record.Source.IsolationDomainID, binding.Record.OperationID, encoded, binding.LeaseOwner, binding.FencingToken, binding.ActorID, binding.CorrelationID)
	if err != nil {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportUnavailable
	}
	entry, found, err = readWorkspaceChangeExport(ctx, tx, binding.Record.Source.IsolationDomainID, binding.Record.OperationID)
	if err != nil || !found || entry.Record != binding.Record {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	if result.RowsAffected() == 1 {
		if err := writeOutboxAndAudit(ctx, tx, binding.Record.Source.IsolationDomainID, "workspace-change-export", binding.Record.ArtifactRecord().ID, "workspace.changes.export.prepared", binding.ActorID, binding.CorrelationID, "accepted", binding.Record.OperationID, repository.now()); err != nil {
			return workspace.ChangeExportEntry{}, workspace.ErrChangeExportUnavailable
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportUnavailable
	}
	return entry, nil
}

// CompleteWorkspaceChangeExport is the catalog side of verified immutable
// object finalization. Its caller must have read back the exact object bytes.
// Completion, the existing governed artifact descriptor, audit and outbox share
// one transaction. No completion record grants permission to destroy a sandbox.
func (repository *Repository) CompleteWorkspaceChangeExport(ctx context.Context, binding workspace.ChangeExportBinding) (workspace.ChangeExportEntry, error) {
	if !repository.Configured() || ctx == nil || !workspace.ValidChangeExportBinding(binding) {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportUnavailable
	}
	defer tx.Rollback(ctx)
	entry, found, err := readWorkspaceChangeExport(ctx, tx, binding.Record.Source.IsolationDomainID, binding.Record.OperationID)
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	if !found || entry.Record != binding.Record {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	if entry.Ready {
		return entry, nil
	}
	if _, err := lockWorkspaceChangeExport(ctx, tx, binding); err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	// Serialize on the operation, then observe again before any catalog mutation.
	entry, found, err = readWorkspaceChangeExport(ctx, tx, binding.Record.Source.IsolationDomainID, binding.Record.OperationID)
	if err != nil || !found || entry.Record != binding.Record {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	if entry.Ready {
		return entry, nil
	}
	artifactBinding := artifact.Binding{Record: binding.Record.ArtifactRecord(), ActorID: binding.ActorID, CorrelationID: binding.CorrelationID, LeaseOwner: binding.LeaseOwner, FencingToken: binding.FencingToken, StateMachineVersion: binding.StateMachineVersion}
	if _, err := repository.insertInvocationArtifact(ctx, tx, artifactBinding); err != nil {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	if _, err := tx.Exec(ctx, `INSERT INTO invocation_workspace_change_completions(isolation_domain_id,operation_id,artifact_id,lease_owner,fencing_token,actor_id,correlation_id)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, binding.Record.Source.IsolationDomainID, binding.Record.OperationID, artifactBinding.Record.ID, binding.LeaseOwner, binding.FencingToken, binding.ActorID, binding.CorrelationID); err != nil {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportUnavailable
	}
	if err := writeOutboxAndAudit(ctx, tx, binding.Record.Source.IsolationDomainID, "workspace-change-export", artifactBinding.Record.ID, "workspace.changes.export.completed", binding.ActorID, binding.CorrelationID, "succeeded", binding.Record.OperationID, repository.now()); err != nil {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportUnavailable
	}
	if err := tx.Commit(ctx); err != nil {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportUnavailable
	}
	return workspace.ChangeExportEntry{Record: binding.Record, Ready: true}, nil
}

func (repository *Repository) GetWorkspaceChangeExport(ctx context.Context, scope, operation string) (workspace.ChangeExportEntry, error) {
	if !repository.Configured() || ctx == nil {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportUnavailable
	}
	entry, found, err := readWorkspaceChangeExport(ctx, repository.pool, scope, operation)
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	if !found {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportMissing
	}
	return entry, nil
}

func lockWorkspaceChangeExport(ctx context.Context, tx pgx.Tx, binding workspace.ChangeExportBinding) ([]byte, error) {
	encoded, err := json.Marshal(binding.Record)
	if err != nil {
		return nil, workspace.ErrChangeExportConflict
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT lock_workspace_change_export_claim($1,$2,$3,$4,$5,$6,$7)`, binding.Record.Source.IsolationDomainID, binding.Record.OperationID, binding.LeaseOwner, binding.FencingToken, binding.ActorID, binding.CorrelationID, encoded).Scan(&allowed)
	if err != nil || !allowed {
		return nil, workspace.ErrChangeExportUnavailable
	}
	return encoded, nil
}
func readWorkspaceChangeExport(ctx context.Context, querier operationQuerier, scope, operation string) (workspace.ChangeExportEntry, bool, error) {
	var entry workspace.ChangeExportEntry
	var encoded []byte
	var artifactID string
	err := querier.QueryRow(ctx, `SELECT intent.record,completion.operation_id IS NOT NULL,COALESCE(completion.artifact_id,'')
 FROM invocation_workspace_change_exports intent LEFT JOIN invocation_workspace_change_completions completion USING(isolation_domain_id,operation_id)
 WHERE intent.isolation_domain_id=$1 AND intent.operation_id=$2`, scope, operation).Scan(&encoded, &entry.Ready, &artifactID)
	if errors.Is(err, pgx.ErrNoRows) {
		return entry, false, nil
	}
	if err != nil {
		return entry, false, workspace.ErrChangeExportUnavailable
	}
	if len(encoded) > 16<<10 {
		return workspace.ChangeExportEntry{}, false, workspace.ErrChangeExportConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&entry.Record) != nil || !workspace.ValidChangeExportRecord(entry.Record) || entry.Record.Source.IsolationDomainID != scope || entry.Record.OperationID != operation {
		return workspace.ChangeExportEntry{}, false, workspace.ErrChangeExportConflict
	}
	if entry.Ready {
		record, found, err := findInvocationArtifactRecord(ctx, querier, scope, artifactID)
		if err != nil || !found || record != entry.Record.ArtifactRecord() {
			return workspace.ChangeExportEntry{}, false, workspace.ErrChangeExportConflict
		}
	}
	return entry, true, nil
}

var _ workspace.ChangeExportCatalog = (*Repository)(nil)
