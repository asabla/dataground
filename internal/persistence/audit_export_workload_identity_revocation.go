package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	AuditExportWorkloadIdentityRevocationRecordContract = "dataground.audit-export-workload-identity-revocation-record/v1"
	auditExportWorkloadIdentityRevocationContract       = "dataground.audit-export-workload-identity-revocation/ed25519/v1"
)

var (
	ErrAuditExportWorkloadIdentityRevocationInvalid  = errors.New("audit export workload identity revocation is invalid")
	ErrAuditExportWorkloadIdentityRevocationConflict = errors.New("audit export workload identity revocation conflicts with durable state")
)

type AuditExportWorkloadIdentityRevocationRecord struct {
	Contract                           string
	RevocationContract                 string
	RevocationSHA256                   string
	IsolationDomainID                  string
	Scope                              string
	WorkloadIdentityAuthorityID        string
	WorkloadIdentityTrustProfileSHA256 string
	WorkloadIdentitySigningKeyID       string
	ExternalReasonSHA256               string
	RevocationAuthorityID              string
	RevocationTrustProfileSHA256       string
	RevocationSigningKeyID             string
	IssuedAt                           time.Time
	EffectiveAt                        time.Time
	ActorID                            string
	ReasonDigest                       []byte
	CorrelationID                      string
}

func (record AuditExportWorkloadIdentityRevocationRecord) Valid() bool {
	return record.Contract == AuditExportWorkloadIdentityRevocationRecordContract &&
		record.RevocationContract == auditExportWorkloadIdentityRevocationContract &&
		auditExportDeliveryDigest.MatchString(record.RevocationSHA256) &&
		operatorAuditDomainPattern.MatchString(record.IsolationDomainID) &&
		validAuditExportWorkloadIdentityRevocationScope(record.Scope, record.WorkloadIdentitySigningKeyID) &&
		auditExportDeliveryRecipient.MatchString(record.WorkloadIdentityAuthorityID) &&
		auditExportDeliveryDigest.MatchString(record.WorkloadIdentityTrustProfileSHA256) &&
		auditExportDeliveryDigest.MatchString(record.ExternalReasonSHA256) &&
		auditExportDeliveryRecipient.MatchString(record.RevocationAuthorityID) &&
		record.RevocationAuthorityID != record.WorkloadIdentityAuthorityID &&
		auditExportDeliveryDigest.MatchString(record.RevocationTrustProfileSHA256) &&
		auditExportDeliveryKeyID.MatchString(record.RevocationSigningKeyID) &&
		canonicalAuditExportRecipientTrustTime(record.IssuedAt) &&
		canonicalAuditExportRecipientTrustTime(record.EffectiveAt) &&
		validOperatorAuditText(record.ActorID, 256) && len(record.ReasonDigest) == sha256.Size &&
		operatorAuditExportCorrelation.MatchString(record.CorrelationID)
}

func (repository *Repository) RecordAuditExportWorkloadIdentityRevocation(
	ctx context.Context,
	record AuditExportWorkloadIdentityRevocationRecord,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !record.Valid() {
		return ErrAuditExportWorkloadIdentityRevocationInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	record.ReasonDigest = append([]byte(nil), record.ReasonDigest...)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit export workload identity revocation: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuditExportWorkloadIdentityRevocations(ctx, tx, record.IsolationDomainID); err != nil {
		return err
	}
	existing, exists, err := readAuditExportWorkloadIdentityRevocation(
		ctx, tx, record.RevocationSHA256,
	)
	if err != nil {
		return err
	}
	if exists {
		if !sameAuditExportWorkloadIdentityRevocation(existing, record) {
			return ErrAuditExportWorkloadIdentityRevocationConflict
		}
		return tx.Commit(ctx)
	}
	var correlatedSHA256 string
	err = tx.QueryRow(ctx, `
		SELECT revocation_sha256
		FROM audit_export_workload_identity_revocations
		WHERE correlation_id = $1
	`, record.CorrelationID).Scan(&correlatedSHA256)
	if err == nil {
		return ErrAuditExportWorkloadIdentityRevocationConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect audit export workload identity revocation correlation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_workload_identity_revocations (
			record_contract, revocation_contract, revocation_sha256, isolation_domain_id,
			scope, workload_identity_authority_id, workload_identity_trust_profile_sha256,
			workload_identity_signing_key_id, external_reason_sha256, revocation_authority_id,
			revocation_trust_profile_sha256, revocation_signing_key_id,
			issued_at, effective_at, actor_id, reason_digest, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`, record.Contract, record.RevocationContract, record.RevocationSHA256,
		record.IsolationDomainID, record.Scope, record.WorkloadIdentityAuthorityID,
		record.WorkloadIdentityTrustProfileSHA256, nullAuditExportRecipientTrustText(record.WorkloadIdentitySigningKeyID),
		record.ExternalReasonSHA256, record.RevocationAuthorityID,
		record.RevocationTrustProfileSHA256, record.RevocationSigningKeyID,
		record.IssuedAt, record.EffectiveAt, record.ActorID, record.ReasonDigest,
		record.CorrelationID); err != nil {
		return mapAuditExportWorkloadIdentityRevocationWriteError(err)
	}
	resourceID := identity.Derived(
		"awr", record.IsolationDomainID+"\n"+record.RevocationSHA256,
	)
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, 'audit-export-workload-identity-revocation.record',
			'audit-export-workload-identity-revocation', $4, 'accepted', $5,
			jsonb_strip_nulls(jsonb_build_object(
				'reasonDigest', $6::text,
				'workloadIdentityRevocationSha256', $7::text,
				'workloadIdentityRevocationScope', $8::text,
				'workloadIdentityAuthorityId', $9::text,
				'workloadIdentityTrustProfileSha256', $10::text,
				'workloadIdentitySigningKeyId', NULLIF($11::text, ''),
				'workloadIdentityRevocationAuthorityId', $12::text,
				'workloadIdentityRevocationEffectiveAt', $13::text
			)),
			clock_timestamp()
		)
	`, identity.New("aud"), record.IsolationDomainID, record.ActorID, resourceID,
		record.CorrelationID, digestBytes(record.ReasonDigest), record.RevocationSHA256,
		record.Scope, record.WorkloadIdentityAuthorityID, record.WorkloadIdentityTrustProfileSHA256,
		record.WorkloadIdentitySigningKeyID, record.RevocationAuthorityID,
		formatAuditExportRecipientTrustTime(record.EffectiveAt)); err != nil {
		return fmt.Errorf("audit export workload identity revocation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export workload identity revocation: %w", err)
	}
	return nil
}

func readAuditExportWorkloadIdentityRevocation(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	revocationSHA256 string,
) (AuditExportWorkloadIdentityRevocationRecord, bool, error) {
	var record AuditExportWorkloadIdentityRevocationRecord
	var signingKeyID *string
	err := querier.QueryRow(ctx, `
		SELECT record_contract, revocation_contract, revocation_sha256, isolation_domain_id,
		       scope, workload_identity_authority_id, workload_identity_trust_profile_sha256,
		       workload_identity_signing_key_id, external_reason_sha256, revocation_authority_id,
		       revocation_trust_profile_sha256, revocation_signing_key_id,
		       issued_at, effective_at, actor_id, reason_digest, correlation_id
		FROM audit_export_workload_identity_revocations
		WHERE revocation_sha256 = $1
	`, revocationSHA256).Scan(
		&record.Contract, &record.RevocationContract, &record.RevocationSHA256,
		&record.IsolationDomainID, &record.Scope, &record.WorkloadIdentityAuthorityID,
		&record.WorkloadIdentityTrustProfileSHA256, &signingKeyID, &record.ExternalReasonSHA256,
		&record.RevocationAuthorityID, &record.RevocationTrustProfileSHA256,
		&record.RevocationSigningKeyID, &record.IssuedAt, &record.EffectiveAt,
		&record.ActorID, &record.ReasonDigest, &record.CorrelationID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditExportWorkloadIdentityRevocationRecord{}, false, nil
	}
	if err != nil {
		return AuditExportWorkloadIdentityRevocationRecord{}, false,
			fmt.Errorf("read audit export workload identity revocation: %w", err)
	}
	if signingKeyID != nil {
		record.WorkloadIdentitySigningKeyID = *signingKeyID
	}
	if !record.Valid() {
		return AuditExportWorkloadIdentityRevocationRecord{}, false,
			ErrAuditExportWorkloadIdentityRevocationConflict
	}
	return record, true, nil
}

func lockAuditExportWorkloadIdentityRevocations(
	ctx context.Context,
	tx pgx.Tx,
	isolationDomainID string,
) error {
	lockKey := "audit-export-workload-identity-revocation\n" + isolationDomainID
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return fmt.Errorf("lock audit export workload identity revocations: %w", err)
	}
	return nil
}

func auditExportWorkloadIdentityRevoked(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	workloadIdentityAuthorityID string,
	workloadIdentityTrustProfileSHA256 string,
	workloadIdentitySigningKeyID string,
	at time.Time,
) (bool, error) {
	var revoked bool
	if err := querier.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM audit_export_workload_identity_revocations
			WHERE isolation_domain_id = $1
			  AND workload_identity_authority_id = $2
			  AND workload_identity_trust_profile_sha256 = $3
			  AND effective_at <= $5
			  AND (scope = 'profile' OR (scope = 'key' AND workload_identity_signing_key_id = $4))
		)
	`, isolationDomainID, workloadIdentityAuthorityID, workloadIdentityTrustProfileSHA256,
		workloadIdentitySigningKeyID, at).Scan(&revoked); err != nil {
		return false, fmt.Errorf("read audit export workload identity revocation: %w", err)
	}
	return revoked, nil
}

func sameAuditExportWorkloadIdentityRevocation(
	left AuditExportWorkloadIdentityRevocationRecord,
	right AuditExportWorkloadIdentityRevocationRecord,
) bool {
	return left.Contract == right.Contract && left.RevocationContract == right.RevocationContract &&
		left.RevocationSHA256 == right.RevocationSHA256 &&
		left.IsolationDomainID == right.IsolationDomainID && left.Scope == right.Scope &&
		left.WorkloadIdentityAuthorityID == right.WorkloadIdentityAuthorityID &&
		left.WorkloadIdentityTrustProfileSHA256 == right.WorkloadIdentityTrustProfileSHA256 &&
		left.WorkloadIdentitySigningKeyID == right.WorkloadIdentitySigningKeyID &&
		left.ExternalReasonSHA256 == right.ExternalReasonSHA256 &&
		left.RevocationAuthorityID == right.RevocationAuthorityID &&
		left.RevocationTrustProfileSHA256 == right.RevocationTrustProfileSHA256 &&
		left.RevocationSigningKeyID == right.RevocationSigningKeyID &&
		left.IssuedAt.Equal(right.IssuedAt) && left.EffectiveAt.Equal(right.EffectiveAt) &&
		left.ActorID == right.ActorID && bytes.Equal(left.ReasonDigest, right.ReasonDigest) &&
		left.CorrelationID == right.CorrelationID
}

func validAuditExportWorkloadIdentityRevocationScope(scope, signingKeyID string) bool {
	return (scope == "profile" && signingKeyID == "") ||
		(scope == "key" && auditExportDeliveryKeyID.MatchString(signingKeyID))
}

func mapAuditExportWorkloadIdentityRevocationWriteError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) &&
		(databaseError.Code == "23505" || databaseError.Code == "P0001") {
		return ErrAuditExportWorkloadIdentityRevocationConflict
	}
	return fmt.Errorf("record audit export workload identity revocation: %w", err)
}
