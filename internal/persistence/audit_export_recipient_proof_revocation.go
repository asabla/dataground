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
	AuditExportRecipientProofRevocationRecordContract = "dataground.audit-export-recipient-proof-revocation-record/v1"
	auditExportRecipientProofRevocationContract       = "dataground.audit-export-recipient-proof-revocation/ed25519/v1"
)

var (
	ErrAuditExportRecipientProofRevocationInvalid  = errors.New("audit export recipient proof revocation is invalid")
	ErrAuditExportRecipientProofRevocationConflict = errors.New("audit export recipient proof revocation conflicts with durable state")
)

type AuditExportRecipientProofRevocationRecord struct {
	Contract                     string
	RevocationContract           string
	RevocationSHA256             string
	IsolationDomainID            string
	Scope                        string
	ProofingAuthorityID          string
	ProofingTrustProfileSHA256   string
	ProofingSigningKeyID         string
	ExternalReasonSHA256         string
	RevocationAuthorityID        string
	RevocationTrustProfileSHA256 string
	RevocationSigningKeyID       string
	IssuedAt                     time.Time
	EffectiveAt                  time.Time
	ActorID                      string
	ReasonDigest                 []byte
	CorrelationID                string
	Acquisition                  *AuditExportRevocationAcquisition
}

func (record AuditExportRecipientProofRevocationRecord) Valid() bool {
	return record.Contract == AuditExportRecipientProofRevocationRecordContract &&
		record.RevocationContract == auditExportRecipientProofRevocationContract &&
		auditExportDeliveryDigest.MatchString(record.RevocationSHA256) &&
		operatorAuditDomainPattern.MatchString(record.IsolationDomainID) &&
		validAuditExportRecipientProofRevocationScope(record.Scope, record.ProofingSigningKeyID) &&
		auditExportDeliveryRecipient.MatchString(record.ProofingAuthorityID) &&
		auditExportDeliveryDigest.MatchString(record.ProofingTrustProfileSHA256) &&
		auditExportDeliveryDigest.MatchString(record.ExternalReasonSHA256) &&
		auditExportDeliveryRecipient.MatchString(record.RevocationAuthorityID) &&
		record.RevocationAuthorityID != record.ProofingAuthorityID &&
		auditExportDeliveryDigest.MatchString(record.RevocationTrustProfileSHA256) &&
		auditExportDeliveryKeyID.MatchString(record.RevocationSigningKeyID) &&
		canonicalAuditExportRecipientTrustTime(record.IssuedAt) &&
		canonicalAuditExportRecipientTrustTime(record.EffectiveAt) &&
		validOperatorAuditText(record.ActorID, 256) && len(record.ReasonDigest) == sha256.Size &&
		operatorAuditExportCorrelation.MatchString(record.CorrelationID) &&
		(record.Acquisition == nil || record.Acquisition.validFor(AuditExportRevocationAuthorityPurposeRecipientProof))
}

func (repository *Repository) RecordAuditExportRecipientProofRevocation(
	ctx context.Context,
	record AuditExportRecipientProofRevocationRecord,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !record.Valid() {
		return ErrAuditExportRecipientProofRevocationInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	record.ReasonDigest = append([]byte(nil), record.ReasonDigest...)
	record.Acquisition = cloneAuditExportRevocationAcquisition(record.Acquisition)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit export recipient proof revocation: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuditExportRecipientProofRevocations(ctx, tx, record.IsolationDomainID); err != nil {
		return err
	}
	existing, exists, err := readAuditExportRecipientProofRevocation(
		ctx, tx, record.RevocationSHA256,
	)
	if err != nil {
		return err
	}
	if exists {
		if !sameAuditExportRecipientProofRevocation(existing, record) {
			return ErrAuditExportRecipientProofRevocationConflict
		}
		return tx.Commit(ctx)
	}
	if err := requireAuditExportRevocationAuthority(
		ctx, tx, record.IsolationDomainID,
		AuditExportRevocationAuthorityPurposeRecipientProof,
		record.RevocationAuthorityID, record.RevocationTrustProfileSHA256,
		record.RevocationSigningKeyID,
	); err != nil {
		return err
	}
	var correlatedSHA256 string
	err = tx.QueryRow(ctx, `
		SELECT revocation_sha256
		FROM audit_export_recipient_proof_revocations
		WHERE correlation_id = $1
	`, record.CorrelationID).Scan(&correlatedSHA256)
	if err == nil {
		return ErrAuditExportRecipientProofRevocationConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect audit export recipient proof revocation correlation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_recipient_proof_revocations (
			record_contract, revocation_contract, revocation_sha256, isolation_domain_id,
			scope, proofing_authority_id, proofing_trust_profile_sha256,
			proofing_signing_key_id, external_reason_sha256, revocation_authority_id,
			revocation_trust_profile_sha256, revocation_signing_key_id,
			issued_at, effective_at, actor_id, reason_digest, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`, record.Contract, record.RevocationContract, record.RevocationSHA256,
		record.IsolationDomainID, record.Scope, record.ProofingAuthorityID,
		record.ProofingTrustProfileSHA256, nullAuditExportRecipientTrustText(record.ProofingSigningKeyID),
		record.ExternalReasonSHA256, record.RevocationAuthorityID,
		record.RevocationTrustProfileSHA256, record.RevocationSigningKeyID,
		record.IssuedAt, record.EffectiveAt, record.ActorID, record.ReasonDigest,
		record.CorrelationID); err != nil {
		return mapAuditExportRecipientProofRevocationWriteError(err)
	}
	if err := insertAuditExportRevocationAcquisition(
		ctx, tx, record.Acquisition, record.RevocationSHA256, record.IsolationDomainID,
		record.RevocationTrustProfileSHA256, record.CorrelationID,
	); err != nil {
		return mapAuditExportRecipientProofRevocationWriteError(err)
	}
	sourceID, sourceRegistrySHA256 := auditExportRevocationAcquisitionMetadata(record.Acquisition)
	resourceID := identity.Derived(
		"arv", record.IsolationDomainID+"\n"+record.RevocationSHA256,
	)
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, 'audit-export-recipient-proof-revocation.record',
			'audit-export-recipient-proof-revocation', $4, 'accepted', $5,
			jsonb_strip_nulls(jsonb_build_object(
				'reasonDigest', $6::text,
				'recipientProofRevocationSha256', $7::text,
				'recipientProofRevocationScope', $8::text,
				'recipientProofingAuthorityId', $9::text,
				'recipientProofingTrustProfileSha256', $10::text,
				'recipientProofingSigningKeyId', NULLIF($11::text, ''),
				'recipientRevocationAuthorityId', $12::text,
				'recipientProofRevocationEffectiveAt', $13::text,
				'revocationSourceId', NULLIF($14::text, ''),
				'revocationSourceRegistrySha256', NULLIF($15::text, '')
			)),
			clock_timestamp()
		)
	`, identity.New("aud"), record.IsolationDomainID, record.ActorID, resourceID,
		record.CorrelationID, digestBytes(record.ReasonDigest), record.RevocationSHA256,
		record.Scope, record.ProofingAuthorityID, record.ProofingTrustProfileSHA256,
		record.ProofingSigningKeyID, record.RevocationAuthorityID,
		formatAuditExportRecipientTrustTime(record.EffectiveAt), sourceID,
		sourceRegistrySHA256); err != nil {
		return fmt.Errorf("audit export recipient proof revocation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export recipient proof revocation: %w", err)
	}
	return nil
}

func readAuditExportRecipientProofRevocation(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	revocationSHA256 string,
) (AuditExportRecipientProofRevocationRecord, bool, error) {
	var record AuditExportRecipientProofRevocationRecord
	var signingKeyID *string
	err := querier.QueryRow(ctx, `
		SELECT record_contract, revocation_contract, revocation_sha256, isolation_domain_id,
		       scope, proofing_authority_id, proofing_trust_profile_sha256,
		       proofing_signing_key_id, external_reason_sha256, revocation_authority_id,
		       revocation_trust_profile_sha256, revocation_signing_key_id,
		       issued_at, effective_at, actor_id, reason_digest, correlation_id
		FROM audit_export_recipient_proof_revocations
		WHERE revocation_sha256 = $1
	`, revocationSHA256).Scan(
		&record.Contract, &record.RevocationContract, &record.RevocationSHA256,
		&record.IsolationDomainID, &record.Scope, &record.ProofingAuthorityID,
		&record.ProofingTrustProfileSHA256, &signingKeyID, &record.ExternalReasonSHA256,
		&record.RevocationAuthorityID, &record.RevocationTrustProfileSHA256,
		&record.RevocationSigningKeyID, &record.IssuedAt, &record.EffectiveAt,
		&record.ActorID, &record.ReasonDigest, &record.CorrelationID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditExportRecipientProofRevocationRecord{}, false, nil
	}
	if err != nil {
		return AuditExportRecipientProofRevocationRecord{}, false,
			fmt.Errorf("read audit export recipient proof revocation: %w", err)
	}
	if signingKeyID != nil {
		record.ProofingSigningKeyID = *signingKeyID
	}
	record.Acquisition, err = readAuditExportRevocationAcquisition(
		ctx, querier, AuditExportRevocationAuthorityPurposeRecipientProof, revocationSHA256,
	)
	if err != nil {
		return AuditExportRecipientProofRevocationRecord{}, false, err
	}
	if !record.Valid() {
		return AuditExportRecipientProofRevocationRecord{}, false,
			ErrAuditExportRecipientProofRevocationConflict
	}
	return record, true, nil
}

func lockAuditExportRecipientProofRevocations(
	ctx context.Context,
	tx pgx.Tx,
	isolationDomainID string,
) error {
	lockKey := "audit-export-recipient-proof-revocation\n" + isolationDomainID
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return fmt.Errorf("lock audit export recipient proof revocations: %w", err)
	}
	return nil
}

func auditExportRecipientProofRevoked(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	proofingAuthorityID string,
	proofingTrustProfileSHA256 string,
	proofingSigningKeyID string,
	at time.Time,
) (bool, error) {
	var revoked bool
	if err := querier.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM audit_export_recipient_proof_revocations
			WHERE isolation_domain_id = $1
			  AND proofing_authority_id = $2
			  AND proofing_trust_profile_sha256 = $3
			  AND effective_at <= $5
			  AND (scope = 'profile' OR (scope = 'key' AND proofing_signing_key_id = $4))
		)
	`, isolationDomainID, proofingAuthorityID, proofingTrustProfileSHA256,
		proofingSigningKeyID, at).Scan(&revoked); err != nil {
		return false, fmt.Errorf("read audit export recipient proof revocation: %w", err)
	}
	return revoked, nil
}

func sameAuditExportRecipientProofRevocation(
	left AuditExportRecipientProofRevocationRecord,
	right AuditExportRecipientProofRevocationRecord,
) bool {
	return left.Contract == right.Contract && left.RevocationContract == right.RevocationContract &&
		left.RevocationSHA256 == right.RevocationSHA256 &&
		left.IsolationDomainID == right.IsolationDomainID && left.Scope == right.Scope &&
		left.ProofingAuthorityID == right.ProofingAuthorityID &&
		left.ProofingTrustProfileSHA256 == right.ProofingTrustProfileSHA256 &&
		left.ProofingSigningKeyID == right.ProofingSigningKeyID &&
		left.ExternalReasonSHA256 == right.ExternalReasonSHA256 &&
		left.RevocationAuthorityID == right.RevocationAuthorityID &&
		left.RevocationTrustProfileSHA256 == right.RevocationTrustProfileSHA256 &&
		left.RevocationSigningKeyID == right.RevocationSigningKeyID &&
		left.IssuedAt.Equal(right.IssuedAt) && left.EffectiveAt.Equal(right.EffectiveAt) &&
		left.ActorID == right.ActorID && bytes.Equal(left.ReasonDigest, right.ReasonDigest) &&
		left.CorrelationID == right.CorrelationID &&
		sameAuditExportRevocationAcquisition(left.Acquisition, right.Acquisition)
}

func validAuditExportRecipientProofRevocationScope(scope, signingKeyID string) bool {
	return (scope == "profile" && signingKeyID == "") ||
		(scope == "key" && auditExportDeliveryKeyID.MatchString(signingKeyID))
}

func mapAuditExportRecipientProofRevocationWriteError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) &&
		(databaseError.Code == "23505" || databaseError.Code == "P0001") {
		return ErrAuditExportRecipientProofRevocationConflict
	}
	return fmt.Errorf("record audit export recipient proof revocation: %w", err)
}
