package persistence

import (
	"context"
	"errors"

	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5"
)

func (repository *Repository) GetWorkspaceSource(ctx context.Context, scope, workspaceID, id string) (workspace.SourceCatalogEntry, error) {
	if !repository.Configured() {
		return workspace.SourceCatalogEntry{}, workspace.ErrSourceUnavailable
	}
	return findWorkspaceSource(ctx, repository.pool, scope, workspaceID, id)
}

type sourceQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func findWorkspaceSource(ctx context.Context, q sourceQuerier, scope, workspaceID, id string) (workspace.SourceCatalogEntry, error) {
	var entry workspace.SourceCatalogEntry
	r := &entry.Installation.Record
	err := q.QueryRow(ctx, `
  SELECT i.schema_version, i.isolation_domain_id, i.workspace_id, i.id,
         i.archive_digest, i.storage_profile_digest, i.archive_bytes,
         i.entry_count, i.source_bytes, i.actor_id, i.correlation_id,
         EXISTS (
           SELECT 1 FROM workspace_source_completions c
           WHERE c.isolation_domain_id = i.isolation_domain_id
             AND c.workspace_id = i.workspace_id AND c.id = i.id
         )
  FROM workspace_source_installations i
  WHERE i.isolation_domain_id = $1 AND i.workspace_id = $2 AND i.id = $3
 `, scope, workspaceID, id).Scan(&r.SchemaVersion, &r.IsolationDomainID, &r.WorkspaceID, &r.ID, &r.ArchiveDigest, &r.StorageProfileDigest, &r.ArchiveBytes, &r.EntryCount, &r.SourceBytes, &entry.Installation.ActorID, &entry.Installation.CorrelationID, &entry.Ready)
	if errors.Is(err, pgx.ErrNoRows) {
		return entry, workspace.ErrSourceMissing
	}
	if err != nil {
		return entry, workspace.ErrSourceUnavailable
	}
	if !workspace.ValidSourceInstallation(entry.Installation) {
		return workspace.SourceCatalogEntry{}, workspace.ErrSourceConflict
	}
	return entry, nil
}
func (repository *Repository) PrepareWorkspaceSource(ctx context.Context, input workspace.SourceInstallation) (workspace.SourceCatalogEntry, error) {
	return repository.bindWorkspaceSource(ctx, input, false)
}
func (repository *Repository) CompleteWorkspaceSource(ctx context.Context, input workspace.SourceInstallation) (workspace.SourceCatalogEntry, error) {
	return repository.bindWorkspaceSource(ctx, input, true)
}
func (repository *Repository) bindWorkspaceSource(ctx context.Context, input workspace.SourceInstallation, complete bool) (workspace.SourceCatalogEntry, error) {
	if !workspace.ValidSourceInstallation(input) {
		return workspace.SourceCatalogEntry{}, workspace.ErrSourceInvalid
	}
	if !repository.Configured() {
		return workspace.SourceCatalogEntry{}, workspace.ErrSourceUnavailable
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return workspace.SourceCatalogEntry{}, workspace.ErrSourceUnavailable
	}
	defer tx.Rollback(ctx)
	r := input.Record
	// This exact workspace lock serializes creation, replay and completion and
	// prevents deletion of the parent while an installation is being recorded.
	var parent string
	err = tx.QueryRow(ctx, `SELECT id FROM workspaces WHERE isolation_domain_id=$1 AND id=$2 FOR UPDATE`, r.IsolationDomainID, r.WorkspaceID).Scan(&parent)
	if errors.Is(err, pgx.ErrNoRows) {
		return workspace.SourceCatalogEntry{}, workspace.ErrSourceMissing
	}
	if err != nil {
		return workspace.SourceCatalogEntry{}, workspace.ErrSourceUnavailable
	}
	entry, err := findWorkspaceSource(ctx, tx, r.IsolationDomainID, r.WorkspaceID, r.ID)
	if err != nil && !errors.Is(err, workspace.ErrSourceMissing) {
		return workspace.SourceCatalogEntry{}, err
	}
	found := err == nil
	if found && entry.Installation != input {
		return workspace.SourceCatalogEntry{}, workspace.ErrSourceConflict
	}
	event := ""
	if !found {
		if complete {
			return workspace.SourceCatalogEntry{}, workspace.ErrSourceMissing
		}
		_, err = tx.Exec(ctx, `
   INSERT INTO workspace_source_installations (
     schema_version, isolation_domain_id, workspace_id, id,
     archive_digest, storage_profile_digest, archive_bytes,
     entry_count, source_bytes, actor_id, correlation_id
   ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
  `, r.SchemaVersion, r.IsolationDomainID, r.WorkspaceID, r.ID, r.ArchiveDigest, r.StorageProfileDigest, r.ArchiveBytes, r.EntryCount, r.SourceBytes, input.ActorID, input.CorrelationID)
		event = "workspace.source.prepared"
		entry = workspace.SourceCatalogEntry{Installation: input}
	} else if complete && !entry.Ready {
		_, err = tx.Exec(ctx, `INSERT INTO workspace_source_completions (isolation_domain_id,workspace_id,id) VALUES ($1,$2,$3)`, r.IsolationDomainID, r.WorkspaceID, r.ID)
		event = "workspace.source.completed"
		entry.Ready = true
	}
	if err != nil {
		return workspace.SourceCatalogEntry{}, workspace.ErrSourceUnavailable
	}
	if event != "" {
		// Scope the deterministic outbox identity to both workspace and source.
		err = writeOutboxAndAudit(ctx, tx, r.IsolationDomainID, "workspace-source", r.WorkspaceID+":"+r.ID, event, input.ActorID, input.CorrelationID, "accepted", "", repository.now())
		if err != nil {
			return workspace.SourceCatalogEntry{}, workspace.ErrSourceUnavailable
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return workspace.SourceCatalogEntry{}, workspace.ErrSourceUnavailable
	}
	return entry, nil
}
