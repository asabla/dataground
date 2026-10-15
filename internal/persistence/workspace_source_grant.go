package persistence

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func lockWorkspaceSourceGrant(ctx context.Context, tx pgx.Tx, r workspace.SourceRecord, revision string) error {
	// The database sequence trigger takes the same lock, including for direct
	// administrative SQL, so reads serialize with every grant mutation.
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "workspace-source-grant\n"+r.IsolationDomainID+"\n"+r.WorkspaceID+"\n"+r.ID+"\n"+revision)
	return err
}
func (repository *Repository) ChangeWorkspaceSourceGrant(ctx context.Context, v workspace.SourceGrantChange) error {
	if ctx == nil || !v.Valid() {
		return workspace.ErrSourceInvalid
	}
	if !repository.Configured() {
		return workspace.ErrSourceUnavailable
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return workspace.ErrSourceUnavailable
	}
	defer tx.Rollback(ctx)
	if err := lockWorkspaceSourceGrant(ctx, tx, v.Record, v.RevisionID); err != nil {
		return workspace.ErrSourceUnavailable
	}
	source, err := findWorkspaceSource(ctx, tx, v.Record.IsolationDomainID, v.Record.WorkspaceID, v.Record.ID)
	if err != nil {
		return err
	}
	if !source.Ready || source.Installation.Record != v.Record {
		return workspace.ErrSourceConflict
	}
	existing, found, err := readWorkspaceSourceGrant(ctx, tx, v.Record, v.RevisionID, v.Generation)
	if err != nil {
		return err
	}
	if found {
		if !sameWorkspaceSourceGrant(existing, v) {
			return workspace.ErrSourceConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return workspace.ErrSourceUnavailable
		}
		return nil
	}
	var activated, expires *time.Time
	if v.Operation == "activate" {
		activated, expires = &v.ActivatedAt, &v.ExpiresAt
	}
	_, err = tx.Exec(ctx, `
  INSERT INTO workspace_source_grants (
   contract,isolation_domain_id,workspace_id,source_id,revision_id,
   archive_digest,storage_profile_digest,generation,operation,
   activated_at,expires_at,actor_id,reason_digest,correlation_id
  ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
 `, v.Contract, v.Record.IsolationDomainID, v.Record.WorkspaceID, v.Record.ID, v.RevisionID, v.Record.ArchiveDigest, v.Record.StorageProfileDigest, v.Generation, v.Operation, activated, expires, v.ActorID, v.ReasonDigest, v.CorrelationID)
	if err != nil {
		return sourceGrantWriteError(err)
	}
	resource := v.Record.WorkspaceID + ":" + v.Record.ID + ":" + v.RevisionID + ":" + strconv.FormatInt(v.Generation, 10)
	if err := writeOutboxAndAudit(ctx, tx, v.Record.IsolationDomainID, "workspace-source-grant", resource, "workspace.source.grant."+v.Operation, v.ActorID, v.CorrelationID, "accepted", "", repository.now()); err != nil {
		return workspace.ErrSourceUnavailable
	}
	if err := tx.Commit(ctx); err != nil {
		return workspace.ErrSourceUnavailable
	}
	return nil
}
func readWorkspaceSourceGrant(ctx context.Context, tx pgx.Tx, record workspace.SourceRecord, revision string, generation int64) (workspace.SourceGrantChange, bool, error) {
	var v workspace.SourceGrantChange
	v.Record = record
	var activated, expires *time.Time
	err := tx.QueryRow(ctx, `
  SELECT contract,revision_id,archive_digest,storage_profile_digest,
         generation,operation,activated_at,expires_at,actor_id,reason_digest,correlation_id
  FROM workspace_source_grants
  WHERE isolation_domain_id=$1 AND workspace_id=$2 AND source_id=$3 AND revision_id=$4
    AND ($5::bigint=0 OR generation=$5)
  ORDER BY generation DESC LIMIT 1
 `, record.IsolationDomainID, record.WorkspaceID, record.ID, revision, generation).Scan(&v.Contract, &v.RevisionID, &v.Record.ArchiveDigest, &v.Record.StorageProfileDigest, &v.Generation, &v.Operation, &activated, &expires, &v.ActorID, &v.ReasonDigest, &v.CorrelationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, workspace.ErrSourceUnavailable
	}
	if activated != nil {
		v.ActivatedAt = *activated
	}
	if expires != nil {
		v.ExpiresAt = *expires
	}
	if !v.Valid() {
		return v, false, workspace.ErrSourceConflict
	}
	return v, true, nil
}
func sameWorkspaceSourceGrant(a, b workspace.SourceGrantChange) bool {
	return a.Contract == b.Contract && a.Record == b.Record && a.RevisionID == b.RevisionID && a.Generation == b.Generation && a.Operation == b.Operation && a.ActivatedAt.Equal(b.ActivatedAt) && a.ExpiresAt.Equal(b.ExpiresAt) && a.ActorID == b.ActorID && a.ReasonDigest == b.ReasonDigest && a.CorrelationID == b.CorrelationID
}
func sourceGrantWriteError(err error) error {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) {
		switch pgerr.Code {
		case "23505", "23514", "23503", "P0001":
			return workspace.ErrSourceConflict
		}
	}
	return workspace.ErrSourceUnavailable
}
func (repository *Repository) AuthorizeWorkspaceSource(ctx context.Context, use workspace.SourceUse, phase string) (workspace.SourcePermit, error) {
	return repository.authorizeWorkspaceSource(ctx, use, phase, nil)
}
func (repository *Repository) authorizeWorkspaceSource(ctx context.Context, use workspace.SourceUse, phase string, bound *invocationSourceAuthorizer) (workspace.SourcePermit, error) {
	if ctx == nil || !use.Valid() || (phase != workspace.SourceReadAdmission && phase != workspace.SourceReadDelivery) {
		return workspace.SourcePermit{}, workspace.ErrSourceInvalid
	}
	if !repository.Configured() {
		return workspace.SourcePermit{}, workspace.ErrSourceUnavailable
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return workspace.SourcePermit{}, workspace.ErrSourceUnavailable
	}
	defer tx.Rollback(ctx)
	var claimContract, invocationID, effectID, claimOwner, claimToken any
	var claimExpires *time.Time
	if bound != nil {
		target, expires, err := bound.lock(ctx, tx)
		if err != nil {
			return workspace.SourcePermit{}, err
		}
		if target.IsolationDomainID != use.Record.IsolationDomainID || target.OperationID != use.OperationID || target.RevisionID != use.RevisionID || target.ActorID != use.ActorID || target.CorrelationID != use.CorrelationID {
			return workspace.SourcePermit{}, workspace.ErrSourceUnauthorized
		}
		claimContract = "dataground.invocation-source-read/v1"
		invocationID, effectID = target.InvocationID, bound.effect.EffectID
		claimOwner, claimToken, claimExpires = bound.claim.LeaseOwner, bound.claim.FencingToken, &expires
	}
	if err := lockWorkspaceSourceGrant(ctx, tx, use.Record, use.RevisionID); err != nil {
		return workspace.SourcePermit{}, workspace.ErrSourceUnavailable
	}
	source, sourceErr := findWorkspaceSource(ctx, tx, use.Record.IsolationDomainID, use.Record.WorkspaceID, use.Record.ID)
	if sourceErr != nil && !errors.Is(sourceErr, workspace.ErrSourceMissing) {
		return workspace.SourcePermit{}, sourceErr
	}
	var revision string
	err = tx.QueryRow(ctx, `SELECT id FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2 AND state='published' FOR SHARE`, use.Record.IsolationDomainID, use.RevisionID).Scan(&revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return workspace.SourcePermit{}, workspace.ErrSourceUnavailable
	}
	published := err == nil
	grant, found, err := readWorkspaceSourceGrant(ctx, tx, use.Record, use.RevisionID, 0)
	if err != nil {
		return workspace.SourcePermit{}, err
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return workspace.SourcePermit{}, workspace.ErrSourceUnavailable
	}
	allowed := sourceErr == nil && source.Ready && source.Installation.Record == use.Record && published && found && grant.Record == use.Record && grant.Generation == use.GrantGeneration && grant.Operation == "activate" && !now.Before(grant.ActivatedAt) && now.Before(grant.ExpiresAt)
	var observed any
	var expires *time.Time
	if found {
		observed = grant.Generation
		if grant.Operation == "activate" {
			expires = &grant.ExpiresAt
		}
	}
	var outcome string
	// Recheck expiry in the audit INSERT, after all reads and lock waits. An
	// allowed result is released only after this append-only decision commits.
	err = tx.QueryRow(ctx, `
  INSERT INTO workspace_source_use_decisions (
   isolation_domain_id,id,contract,workspace_id,source_id,revision_id,operation_id,
   archive_digest,storage_profile_digest,requested_generation,observed_generation,
   expires_at,phase,outcome,actor_id,correlation_id,
   claim_contract,invocation_id,effect_id,claim_owner,claim_token,claim_expires_at
  ) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,
    CASE WHEN $14::boolean AND $12::timestamptz>clock_timestamp()
     AND ($17::text IS NULL OR $22::timestamptz>clock_timestamp())
     THEN 'allowed' ELSE 'denied' END,$15,$16,$17,$18,$19,$20,$21,$22)
  RETURNING outcome
 `, use.Record.IsolationDomainID, identity.New("sud"), use.Contract, use.Record.WorkspaceID, use.Record.ID, use.RevisionID, use.OperationID, use.Record.ArchiveDigest, use.Record.StorageProfileDigest, use.GrantGeneration, observed, expires, phase, allowed, use.ActorID, use.CorrelationID, claimContract, invocationID, effectID, claimOwner, claimToken, claimExpires).Scan(&outcome)
	if err != nil {
		return workspace.SourcePermit{}, workspace.ErrSourceUnavailable
	}
	if err := tx.Commit(ctx); err != nil {
		return workspace.SourcePermit{}, workspace.ErrSourceUnavailable
	}
	if outcome != "allowed" || expires == nil || !time.Now().Before(*expires) || (claimExpires != nil && !time.Now().Before(*claimExpires)) {
		return workspace.SourcePermit{}, workspace.ErrSourceUnauthorized
	}
	return workspace.SourcePermit{Generation: grant.Generation, ExpiresAt: *expires}, nil
}
