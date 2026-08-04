package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// EnforcementBundlePersistenceStage is a safe, bounded classification of the
// database operation that failed. It intentionally excludes SQL text,
// connection details, database messages, and values from the attempted record.
type EnforcementBundlePersistenceStage string

const (
	EnforcementBundlePersistenceBegin        EnforcementBundlePersistenceStage = "begin"
	EnforcementBundlePersistenceRevisionRead EnforcementBundlePersistenceStage = "revision-read"
	EnforcementBundlePersistenceInsert       EnforcementBundlePersistenceStage = "insert"
	EnforcementBundlePersistenceRecordRead   EnforcementBundlePersistenceStage = "record-read"
	EnforcementBundlePersistenceRecordCheck  EnforcementBundlePersistenceStage = "record-check"
	EnforcementBundlePersistenceAudit        EnforcementBundlePersistenceStage = "audit"
	EnforcementBundlePersistenceCommit       EnforcementBundlePersistenceStage = "commit"
)

type enforcementBundlePersistenceError struct {
	stage EnforcementBundlePersistenceStage
}

func (err *enforcementBundlePersistenceError) Error() string {
	return "enforcement bundle persistence failed during " + string(err.stage)
}

func (err *enforcementBundlePersistenceError) Unwrap() error {
	return ErrPersistence
}

// EnforcementBundlePersistenceFailure returns a safe failure stage without
// exposing the PostgreSQL error that established it.
func EnforcementBundlePersistenceFailure(err error) (EnforcementBundlePersistenceStage, bool) {
	var persistenceErr *enforcementBundlePersistenceError
	if !errors.As(err, &persistenceErr) {
		return "", false
	}
	return persistenceErr.stage, true
}

func enforcementBundlePersistenceFailure(stage EnforcementBundlePersistenceStage) error {
	return &enforcementBundlePersistenceError{stage: stage}
}

func (store *Store) BindEnforcementBundle(
	ctx context.Context,
	binding execution.EnforcementBundleBinding,
) (execution.EnforcementBundleRecord, error) {
	normalized, err := execution.NormalizeEnforcementBundleBinding(binding)
	if err != nil {
		return execution.EnforcementBundleRecord{}, err
	}
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return execution.EnforcementBundleRecord{}, enforcementBundlePersistenceFailure(EnforcementBundlePersistenceBegin)
	}
	defer transaction.Rollback(ctx)
	var revisionExists bool
	err = transaction.QueryRow(ctx, `
		SELECT true
		FROM service_revisions
		WHERE isolation_domain_id = $1 AND id = $2
		FOR SHARE
	`, normalized.Record.IsolationDomainID, normalized.Record.RevisionID).Scan(&revisionExists)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.EnforcementBundleRecord{}, execution.ErrEnforcementBundleRevisionMissing
	}
	if err != nil {
		return execution.EnforcementBundleRecord{}, enforcementBundlePersistenceFailure(EnforcementBundlePersistenceRevisionRead)
	}
	provenance := normalized.Record.Provenance
	now := store.now()
	result, err := transaction.Exec(ctx, `
		INSERT INTO service_revision_enforcement_bundles (
			isolation_domain_id, id, revision_id, schema_version, artifact_digest,
			media_type, size_bytes, object_key, producer, source_revision,
			compiler_version, catalog_version, target_contract_version,
			compilation_mode, input_digest, binding_digest,
			bound_by, correlation_id, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, $17, $18, $19
		)
		ON CONFLICT (isolation_domain_id, id) DO NOTHING
	`,
		normalized.Record.IsolationDomainID,
		normalized.Record.ID,
		normalized.Record.RevisionID,
		normalized.Record.SchemaVersion,
		normalized.Record.Digest,
		normalized.Record.MediaType,
		normalized.Record.SizeBytes,
		normalized.Record.ObjectKey,
		provenance.Producer,
		provenance.SourceRevision,
		provenance.CompilerVersion,
		provenance.CatalogVersion,
		provenance.TargetContractVersion,
		provenance.Mode,
		provenance.InputDigest,
		provenance.BindingDigest,
		normalized.ActorID,
		normalized.CorrelationID,
		now,
	)
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && databaseError.Code == "23503" {
			return execution.EnforcementBundleRecord{}, execution.ErrEnforcementBundleRevisionMissing
		}
		return execution.EnforcementBundleRecord{}, enforcementBundlePersistenceFailure(EnforcementBundlePersistenceInsert)
	}
	existing, err := getEnforcementBundleRecord(
		ctx,
		transaction,
		normalized.Record.IsolationDomainID,
		normalized.Record.ID,
	)
	if err != nil {
		return execution.EnforcementBundleRecord{}, err
	}
	if !execution.EqualEnforcementBundleRecords(existing, normalized.Record) {
		return execution.EnforcementBundleRecord{}, execution.ErrEnforcementBundleConflict
	}
	if result.RowsAffected() == 1 {
		metadata, err := json.Marshal(map[string]string{
			"artifactDigest": normalized.Record.Digest,
			"bindingDigest":  provenance.BindingDigest,
		})
		if err != nil {
			return execution.EnforcementBundleRecord{}, enforcementBundlePersistenceFailure(EnforcementBundlePersistenceAudit)
		}
		_, err = transaction.Exec(ctx, `
			INSERT INTO audit_records (
				id, isolation_domain_id, actor_id, action, resource_type,
				resource_id, outcome, correlation_id, safe_metadata, occurred_at
			) VALUES ($1, $2, $3, 'enforcement-bundle.bind', 'enforcement-bundle', $4,
			          'succeeded', $5, $6, $7)
		`,
			identity.Derived("aud", normalized.Record.IsolationDomainID+":"+normalized.Record.ID+":enforcement-bundle"),
			normalized.Record.IsolationDomainID,
			normalized.ActorID,
			normalized.Record.ID,
			normalized.CorrelationID,
			metadata,
			now,
		)
		if err != nil {
			return execution.EnforcementBundleRecord{}, enforcementBundlePersistenceFailure(EnforcementBundlePersistenceAudit)
		}
	}
	if err := transaction.Commit(ctx); err != nil {
		return execution.EnforcementBundleRecord{}, enforcementBundlePersistenceFailure(EnforcementBundlePersistenceCommit)
	}
	return existing, nil
}

func (store *Store) GetEnforcementBundleRecord(
	ctx context.Context,
	isolationDomainID string,
	bundleID string,
) (execution.EnforcementBundleRecord, error) {
	return getEnforcementBundleRecord(ctx, store.pool, isolationDomainID, bundleID)
}

func getEnforcementBundleRecord(
	ctx context.Context,
	querier interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	isolationDomainID string,
	bundleID string,
) (execution.EnforcementBundleRecord, error) {
	var record execution.EnforcementBundleRecord
	provenance := &record.Provenance
	err := querier.QueryRow(ctx, `
		SELECT schema_version, isolation_domain_id, id, revision_id, artifact_digest,
		       media_type, size_bytes, object_key, producer, source_revision,
		       compiler_version, catalog_version, target_contract_version,
		       compilation_mode, input_digest, binding_digest
		FROM service_revision_enforcement_bundles
		WHERE isolation_domain_id = $1 AND id = $2
	`, isolationDomainID, bundleID).Scan(
		&record.SchemaVersion,
		&record.IsolationDomainID,
		&record.ID,
		&record.RevisionID,
		&record.Digest,
		&record.MediaType,
		&record.SizeBytes,
		&record.ObjectKey,
		&provenance.Producer,
		&provenance.SourceRevision,
		&provenance.CompilerVersion,
		&provenance.CatalogVersion,
		&provenance.TargetContractVersion,
		&provenance.Mode,
		&provenance.InputDigest,
		&provenance.BindingDigest,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.EnforcementBundleRecord{}, execution.ErrEnforcementBundleMissing
	}
	if err != nil {
		return execution.EnforcementBundleRecord{}, enforcementBundlePersistenceFailure(EnforcementBundlePersistenceRecordRead)
	}
	normalized, err := execution.NormalizeEnforcementBundleRecord(record)
	if err != nil {
		return execution.EnforcementBundleRecord{}, enforcementBundlePersistenceFailure(EnforcementBundlePersistenceRecordCheck)
	}
	return normalized, nil
}

var _ execution.EnforcementBundleStore = (*Store)(nil)
