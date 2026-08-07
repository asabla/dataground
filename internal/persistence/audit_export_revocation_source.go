package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const AuditExportRevocationSourceAuthorizationContract = "dataground.audit-export-revocation-source-authorization/v1"

var (
	ErrAuditExportRevocationSourceInvalid      = errors.New("audit export revocation source change is invalid")
	ErrAuditExportRevocationSourceConflict     = errors.New("audit export revocation source change conflicts with durable state")
	ErrAuditExportRevocationSourceUnauthorized = errors.New("audit export revocation source is not authorized")
)

type AuditExportRevocationSourceChange struct {
	Contract             string
	Operation            string
	IsolationDomainID    string
	Purpose              string
	SourceID             string
	Generation           int64
	SourceRegistrySHA256 string
	ActorID              string
	ReasonDigest         []byte
	CorrelationID        string
}

func (change AuditExportRevocationSourceChange) Valid() bool {
	return change.Contract == AuditExportRevocationSourceAuthorizationContract &&
		(change.Operation == "activate" || change.Operation == "revoke") &&
		operatorAuditDomainPattern.MatchString(change.IsolationDomainID) &&
		(change.Purpose == AuditExportRevocationAuthorityPurposeRecipientProof ||
			change.Purpose == AuditExportRevocationAuthorityPurposeWorkloadIdentity) &&
		auditExportDeliveryRecipient.MatchString(change.SourceID) &&
		change.Generation > 0 && change.Generation <= math.MaxInt64 &&
		auditExportDeliveryDigest.MatchString(change.SourceRegistrySHA256) &&
		validOperatorAuditText(change.ActorID, 256) && len(change.ReasonDigest) == sha256.Size &&
		operatorAuditExportCorrelation.MatchString(change.CorrelationID)
}

func (repository *Repository) ChangeAuditExportRevocationSource(
	ctx context.Context,
	change AuditExportRevocationSourceChange,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !change.Valid() {
		return ErrAuditExportRevocationSourceInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	change.ReasonDigest = append([]byte(nil), change.ReasonDigest...)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit export revocation source change: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuditExportRevocationAuthorityPurpose(ctx, tx, change.Purpose, change.IsolationDomainID); err != nil {
		return err
	}
	existing, exists, err := readAuditExportRevocationSourceGeneration(
		ctx, tx, change.IsolationDomainID, change.Purpose, change.Generation,
	)
	if err != nil {
		return err
	}
	if exists {
		if !sameAuditExportRevocationSourceChange(existing, change) {
			return ErrAuditExportRevocationSourceConflict
		}
		return tx.Commit(ctx)
	}
	var correlatedDomain, correlatedPurpose, correlatedSource string
	var correlatedGeneration int64
	err = tx.QueryRow(ctx, `
		SELECT isolation_domain_id, purpose, source_id, generation
		FROM audit_export_revocation_source_events
		WHERE correlation_id = $1
	`, change.CorrelationID).Scan(
		&correlatedDomain, &correlatedPurpose, &correlatedSource, &correlatedGeneration,
	)
	if err == nil {
		return ErrAuditExportRevocationSourceConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect audit export revocation source correlation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_revocation_source_events (
			authorization_contract, isolation_domain_id, purpose, source_id,
			generation, operation, source_registry_sha256, actor_id,
			reason_digest, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, change.Contract, change.IsolationDomainID, change.Purpose, change.SourceID,
		change.Generation, change.Operation, change.SourceRegistrySHA256,
		change.ActorID, change.ReasonDigest, change.CorrelationID); err != nil {
		return mapAuditExportRevocationSourceWriteError(err)
	}
	resourceID := identity.Derived("ars", fmt.Sprintf(
		"%s\n%s\n%s\n%d", change.IsolationDomainID, change.Purpose,
		change.SourceID, change.Generation,
	))
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, $4, 'audit-export-revocation-source', $5,
			'accepted', $6, jsonb_build_object(
				'reasonDigest', $7::text,
				'revocationSourcePurpose', $8::text,
				'revocationSourceId', $9::text,
				'revocationSourceGeneration', $10::bigint,
				'revocationSourceRegistrySha256', $11::text
			), clock_timestamp()
		)
	`, identity.New("aud"), change.IsolationDomainID, change.ActorID,
		"audit-export-revocation-source."+change.Operation, resourceID,
		change.CorrelationID, digestBytes(change.ReasonDigest), change.Purpose,
		change.SourceID, change.Generation, change.SourceRegistrySHA256); err != nil {
		return fmt.Errorf("audit export revocation source change: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export revocation source change: %w", err)
	}
	return nil
}

func (repository *Repository) AuthorizeAuditExportRevocationSource(
	ctx context.Context,
	isolationDomainID string,
	purpose string,
	sourceID string,
	sourceRegistrySHA256 string,
) (int64, error) {
	if repository == nil || repository.pool == nil || ctx == nil ||
		!operatorAuditDomainPattern.MatchString(isolationDomainID) ||
		!auditExportDeliveryRecipient.MatchString(sourceID) ||
		!auditExportDeliveryDigest.MatchString(sourceRegistrySHA256) ||
		(purpose != AuditExportRevocationAuthorityPurposeRecipientProof &&
			purpose != AuditExportRevocationAuthorityPurposeWorkloadIdentity) {
		return 0, ErrAuditExportRevocationSourceInvalid
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return requireAuditExportRevocationSource(
		ctx, repository.pool, isolationDomainID, purpose, sourceID, sourceRegistrySHA256, 0,
	)
}

func requireAuditExportRevocationSource(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	purpose string,
	sourceID string,
	sourceRegistrySHA256 string,
	expectedGeneration int64,
) (int64, error) {
	var generation int64
	err := querier.QueryRow(ctx, `
		SELECT generation
		FROM audit_export_revocation_source_events
		WHERE isolation_domain_id = $1 AND purpose = $2
		  AND source_id = $3 AND source_registry_sha256 = $4
		  AND operation = 'activate'
		  AND generation = (
			SELECT max(latest.generation)
			FROM audit_export_revocation_source_events AS latest
			WHERE latest.isolation_domain_id = $1 AND latest.purpose = $2
		  )
	`, isolationDomainID, purpose, sourceID, sourceRegistrySHA256).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && expectedGeneration > 0 && generation != expectedGeneration) {
		return 0, ErrAuditExportRevocationSourceUnauthorized
	}
	if err != nil {
		return 0, fmt.Errorf("authorize audit export revocation source: %w", err)
	}
	return generation, nil
}

func readAuditExportRevocationSourceGeneration(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	purpose string,
	generation int64,
) (AuditExportRevocationSourceChange, bool, error) {
	var change AuditExportRevocationSourceChange
	err := querier.QueryRow(ctx, `
		SELECT authorization_contract, operation, isolation_domain_id, purpose,
		       source_id, generation, source_registry_sha256, actor_id,
		       reason_digest, correlation_id
		FROM audit_export_revocation_source_events
		WHERE isolation_domain_id = $1 AND purpose = $2 AND generation = $3
	`, isolationDomainID, purpose, generation).Scan(
		&change.Contract, &change.Operation, &change.IsolationDomainID, &change.Purpose,
		&change.SourceID, &change.Generation, &change.SourceRegistrySHA256,
		&change.ActorID, &change.ReasonDigest, &change.CorrelationID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditExportRevocationSourceChange{}, false, nil
	}
	if err != nil {
		return AuditExportRevocationSourceChange{}, false,
			fmt.Errorf("read audit export revocation source generation: %w", err)
	}
	if !change.Valid() {
		return AuditExportRevocationSourceChange{}, false, ErrAuditExportRevocationSourceConflict
	}
	return change, true, nil
}

func sameAuditExportRevocationSourceChange(left, right AuditExportRevocationSourceChange) bool {
	return left.Contract == right.Contract && left.Operation == right.Operation &&
		left.IsolationDomainID == right.IsolationDomainID && left.Purpose == right.Purpose &&
		left.SourceID == right.SourceID && left.Generation == right.Generation &&
		left.SourceRegistrySHA256 == right.SourceRegistrySHA256 && left.ActorID == right.ActorID &&
		bytes.Equal(left.ReasonDigest, right.ReasonDigest) && left.CorrelationID == right.CorrelationID
}

func mapAuditExportRevocationSourceWriteError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) &&
		(databaseError.Code == "23505" || databaseError.Code == "P0001") {
		return ErrAuditExportRevocationSourceConflict
	}
	return fmt.Errorf("change audit export revocation source: %w", err)
}
