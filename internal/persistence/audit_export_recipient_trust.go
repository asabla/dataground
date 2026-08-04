package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	AuditExportRecipientTrustAuthorizationContract      = "dataground.audit-export-recipient-trust-authorization/v2"
	AuditExportRecipientEncryptionAuthorizationContract = "dataground.audit-export-recipient-trust-authorization/v3"
	auditExportRecipientTrustProfileContract            = "dataground.audit-export-recipient-trust/ed25519/v1"
	auditExportRecipientEncryptionTrustProfileContract  = "dataground.audit-export-recipient-trust/ed25519-x25519/v2"
	auditExportRecipientIdentityProofContract           = "dataground.audit-export-recipient-identity-proof/ed25519/v1"
	legacyAuditExportRecipientTrustContract             = "dataground.audit-export-recipient-trust-authorization/v1"
)

var (
	ErrAuditExportRecipientTrustInvalid      = errors.New("audit export recipient trust change is invalid")
	ErrAuditExportRecipientTrustConflict     = errors.New("audit export recipient trust change conflicts with durable state")
	ErrAuditExportRecipientTrustUnauthorized = errors.New("audit export recipient trust is not authorized")
)

type AuditExportRecipientTrustChange struct {
	Contract                    string
	Operation                   string
	IsolationDomainID           string
	RecipientID                 string
	Generation                  int64
	TrustContract               string
	TrustProfileSHA256          string
	KeyIDs                      []string
	EncryptionKeyIDs            []string
	IdentityProofContract       string
	IdentityProofSHA256         string
	IdentityProofEvidenceSHA256 string
	ProofingAuthorityID         string
	ProofingTrustProfileSHA256  string
	ProofingSigningKeyID        string
	IdentityProofVerifiedAt     time.Time
	IdentityProofExpiresAt      time.Time
	ActorID                     string
	ReasonDigest                []byte
	CorrelationID               string
}

func (change AuditExportRecipientTrustChange) Valid() bool {
	return (change.Contract == AuditExportRecipientTrustAuthorizationContract ||
		change.Contract == AuditExportRecipientEncryptionAuthorizationContract ||
		change.Contract == legacyAuditExportRecipientTrustContract) &&
		(change.Operation == "activate" || change.Operation == "revoke") &&
		operatorAuditDomainPattern.MatchString(change.IsolationDomainID) &&
		auditExportDeliveryRecipient.MatchString(change.RecipientID) &&
		change.Generation > 0 && change.Generation <= math.MaxInt64 &&
		validAuditExportRecipientTrustContract(change) &&
		auditExportDeliveryDigest.MatchString(change.TrustProfileSHA256) &&
		validAuditExportRecipientTrustKeys(change.Operation, change.KeyIDs) &&
		validAuditExportRecipientEncryptionKeys(change) &&
		validAuditExportRecipientIdentityProof(change) &&
		validOperatorAuditText(change.ActorID, 256) &&
		len(change.ReasonDigest) == sha256.Size &&
		operatorAuditExportCorrelation.MatchString(change.CorrelationID)
}

func (repository *Repository) ChangeAuditExportRecipientTrust(
	ctx context.Context,
	change AuditExportRecipientTrustChange,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !change.Valid() ||
		(change.Contract != AuditExportRecipientTrustAuthorizationContract &&
			change.Contract != AuditExportRecipientEncryptionAuthorizationContract) {
		return ErrAuditExportRecipientTrustInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	change.ReasonDigest = append([]byte(nil), change.ReasonDigest...)
	change.KeyIDs = append([]string(nil), change.KeyIDs...)
	change.EncryptionKeyIDs = append([]string(nil), change.EncryptionKeyIDs...)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit export recipient trust change: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuditExportRecipientProofRevocations(ctx, tx, change.IsolationDomainID); err != nil {
		return err
	}
	if err := lockAuditExportRecipientTrust(ctx, tx, change.IsolationDomainID, change.RecipientID); err != nil {
		return err
	}

	existing, exists, err := readAuditExportRecipientTrustGeneration(
		ctx, tx, change.IsolationDomainID, change.RecipientID, change.Generation,
	)
	if err != nil {
		return err
	}
	if exists {
		if !sameAuditExportRecipientTrustChange(existing, change) {
			return ErrAuditExportRecipientTrustConflict
		}
		return tx.Commit(ctx)
	}
	var correlatedDomain, correlatedRecipient string
	var correlatedGeneration int64
	err = tx.QueryRow(ctx, `
		SELECT isolation_domain_id, recipient_id, generation
		FROM audit_export_recipient_trust_events
		WHERE correlation_id = $1
	`, change.CorrelationID).Scan(&correlatedDomain, &correlatedRecipient, &correlatedGeneration)
	if err == nil {
		return ErrAuditExportRecipientTrustConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect audit export recipient trust correlation: %w", err)
	}

	latest, latestExists, err := readLatestAuditExportRecipientTrust(
		ctx, tx, change.IsolationDomainID, change.RecipientID,
	)
	if err != nil {
		return err
	}
	expectedGeneration := int64(1)
	if latestExists {
		if latest.Generation == math.MaxInt64 {
			return ErrAuditExportRecipientTrustConflict
		}
		expectedGeneration = latest.Generation + 1
	}
	if change.Generation != expectedGeneration ||
		(!latestExists && change.Operation != "activate") ||
		(change.Operation == "revoke" &&
			(latest.Operation != "activate" || latest.TrustProfileSHA256 != change.TrustProfileSHA256)) ||
		(change.Operation == "activate" && latestExists && latest.Operation == "activate" &&
			latest.TrustProfileSHA256 == change.TrustProfileSHA256 &&
			latest.Contract != legacyAuditExportRecipientTrustContract) {
		return ErrAuditExportRecipientTrustConflict
	}
	if change.Operation == "activate" {
		var databaseNow time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
			return fmt.Errorf("read audit export recipient proof revocation clock: %w", err)
		}
		revoked, err := auditExportRecipientProofRevoked(
			ctx, tx, change.IsolationDomainID, change.ProofingAuthorityID,
			change.ProofingTrustProfileSHA256, change.ProofingSigningKeyID, databaseNow,
		)
		if err != nil {
			return err
		}
		if revoked {
			return ErrAuditExportRecipientTrustUnauthorized
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_recipient_trust_events (
			isolation_domain_id, recipient_id, generation, authorization_contract, operation,
			trust_contract, trust_profile_sha256, identity_proof_contract,
			identity_proof_sha256, identity_proof_evidence_sha256, proofing_authority_id,
			proofing_trust_profile_sha256, proofing_signing_key_id,
			identity_proof_verified_at, identity_proof_expires_at,
			actor_id, reason_digest, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
	`, change.IsolationDomainID, change.RecipientID, change.Generation, change.Contract,
		change.Operation, change.TrustContract, change.TrustProfileSHA256,
		nullAuditExportRecipientTrustText(change.IdentityProofContract),
		nullAuditExportRecipientTrustText(change.IdentityProofSHA256),
		nullAuditExportRecipientTrustText(change.IdentityProofEvidenceSHA256),
		nullAuditExportRecipientTrustText(change.ProofingAuthorityID),
		nullAuditExportRecipientTrustText(change.ProofingTrustProfileSHA256),
		nullAuditExportRecipientTrustText(change.ProofingSigningKeyID),
		nullAuditExportRecipientTrustTime(change.IdentityProofVerifiedAt),
		nullAuditExportRecipientTrustTime(change.IdentityProofExpiresAt),
		change.ActorID, change.ReasonDigest, change.CorrelationID); err != nil {
		return mapAuditExportRecipientTrustWriteError(err)
	}
	for _, keyID := range change.KeyIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_export_recipient_trust_keys (
				isolation_domain_id, recipient_id, generation, key_id
			) VALUES ($1, $2, $3, $4)
		`, change.IsolationDomainID, change.RecipientID, change.Generation, keyID); err != nil {
			return mapAuditExportRecipientTrustWriteError(err)
		}
	}
	for _, keyID := range change.EncryptionKeyIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit_export_recipient_encryption_keys (
				isolation_domain_id, recipient_id, generation, key_id
			) VALUES ($1, $2, $3, $4)
		`, change.IsolationDomainID, change.RecipientID, change.Generation, keyID); err != nil {
			return mapAuditExportRecipientTrustWriteError(err)
		}
	}
	action := "audit-export-recipient-trust." + change.Operation
	resourceID := identity.Derived(
		"art",
		change.IsolationDomainID+"\n"+change.RecipientID,
	)
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, $4, 'audit-export-recipient-trust', $5,
			'accepted', $6,
			jsonb_strip_nulls(jsonb_build_object(
				'generation', $7::bigint,
				'reasonDigest', $8::text,
				'recipientId', $9::text,
				'recipientTrustKeyCount', $10::bigint,
				'recipientEncryptionKeyCount', $11::bigint,
				'recipientTrustProfileSha256', $12::text,
				'recipientIdentityProofSha256', NULLIF($13::text, ''),
				'recipientProofingAuthorityId', NULLIF($14::text, ''),
				'recipientIdentityProofExpiresAt', NULLIF($15::text, '')
			)),
			clock_timestamp()
		)
	`, identity.New("aud"), change.IsolationDomainID, change.ActorID, action,
		resourceID, change.CorrelationID, change.Generation,
		digestBytes(change.ReasonDigest), change.RecipientID, len(change.KeyIDs), len(change.EncryptionKeyIDs),
		change.TrustProfileSHA256, change.IdentityProofSHA256, change.ProofingAuthorityID,
		formatAuditExportRecipientTrustTime(change.IdentityProofExpiresAt)); err != nil {
		return fmt.Errorf("audit export recipient trust change: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export recipient trust change: %w", err)
	}
	return nil
}

func authorizeAuditExportRecipientTrust(
	ctx context.Context,
	tx pgx.Tx,
	delivery AuditExportDelivery,
	acknowledgement AuditExportDeliveryAcknowledgement,
) (int64, error) {
	if err := lockAuditExportRecipientProofRevocations(ctx, tx, delivery.IsolationDomainID); err != nil {
		return 0, err
	}
	if err := lockAuditExportRecipientTrust(ctx, tx, delivery.IsolationDomainID, delivery.RecipientID); err != nil {
		return 0, err
	}
	latest, exists, err := readLatestAuditExportRecipientTrust(
		ctx, tx, delivery.IsolationDomainID, delivery.RecipientID,
	)
	if err != nil {
		return 0, err
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return 0, fmt.Errorf("read audit export recipient trust clock: %w", err)
	}
	revoked := false
	if exists && latest.Operation == "activate" &&
		(latest.Contract == AuditExportRecipientTrustAuthorizationContract ||
			latest.Contract == AuditExportRecipientEncryptionAuthorizationContract) {
		revoked, err = auditExportRecipientProofRevoked(
			ctx, tx, delivery.IsolationDomainID, latest.ProofingAuthorityID,
			latest.ProofingTrustProfileSHA256, latest.ProofingSigningKeyID, databaseNow,
		)
		if err != nil {
			return 0, err
		}
	}
	expectedAuthorizationContract := AuditExportRecipientTrustAuthorizationContract
	expectedTrustContract := auditExportRecipientTrustProfileContract
	if delivery.Contract == AuditExportEncryptedDeliveryContract {
		expectedAuthorizationContract = AuditExportRecipientEncryptionAuthorizationContract
		expectedTrustContract = auditExportRecipientEncryptionTrustProfileContract
	}
	if !exists || latest.Operation != "activate" ||
		latest.Contract != expectedAuthorizationContract ||
		latest.TrustProfileSHA256 != acknowledgement.RecipientTrustProfileSHA256 ||
		latest.TrustContract != expectedTrustContract ||
		!latest.IdentityProofExpiresAt.After(databaseNow) ||
		revoked ||
		!containsAuditExportRecipientTrustKey(latest.KeyIDs, acknowledgement.RecipientSigningKeyID) ||
		(delivery.Contract == AuditExportEncryptedDeliveryContract &&
			(latest.Generation != delivery.RecipientTrustGeneration ||
				latest.Generation != acknowledgement.RecipientTrustGeneration ||
				latest.TrustProfileSHA256 != delivery.RecipientTrustProfileSHA256 ||
				!containsAuditExportRecipientTrustKey(
					latest.EncryptionKeyIDs, delivery.RecipientEncryptionKeyID,
				))) {
		return 0, ErrAuditExportRecipientTrustUnauthorized
	}
	return latest.Generation, nil
}

func authorizeAuditExportRecipientEncryption(
	ctx context.Context,
	tx pgx.Tx,
	delivery AuditExportDelivery,
) (int64, error) {
	if err := lockAuditExportRecipientProofRevocations(ctx, tx, delivery.IsolationDomainID); err != nil {
		return 0, err
	}
	if err := lockAuditExportRecipientTrust(ctx, tx, delivery.IsolationDomainID, delivery.RecipientID); err != nil {
		return 0, err
	}
	latest, exists, err := readLatestAuditExportRecipientTrust(
		ctx, tx, delivery.IsolationDomainID, delivery.RecipientID,
	)
	if err != nil {
		return 0, err
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return 0, fmt.Errorf("read audit export recipient encryption clock: %w", err)
	}
	revoked := false
	if exists && latest.Operation == "activate" &&
		latest.Contract == AuditExportRecipientEncryptionAuthorizationContract {
		revoked, err = auditExportRecipientProofRevoked(
			ctx, tx, delivery.IsolationDomainID, latest.ProofingAuthorityID,
			latest.ProofingTrustProfileSHA256, latest.ProofingSigningKeyID, databaseNow,
		)
		if err != nil {
			return 0, err
		}
	}
	if !exists || latest.Operation != "activate" ||
		latest.Contract != AuditExportRecipientEncryptionAuthorizationContract ||
		latest.TrustContract != auditExportRecipientEncryptionTrustProfileContract ||
		latest.TrustProfileSHA256 != delivery.RecipientTrustProfileSHA256 ||
		!latest.IdentityProofExpiresAt.After(databaseNow) || revoked ||
		!containsAuditExportRecipientTrustKey(latest.EncryptionKeyIDs, delivery.RecipientEncryptionKeyID) {
		return 0, ErrAuditExportRecipientTrustUnauthorized
	}
	return latest.Generation, nil
}

func lockAuditExportRecipientTrust(
	ctx context.Context,
	tx pgx.Tx,
	isolationDomainID string,
	recipientID string,
) error {
	lockKey := "audit-export-recipient-trust\n" + isolationDomainID + "\n" + recipientID
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return fmt.Errorf("lock audit export recipient trust: %w", err)
	}
	return nil
}

func readLatestAuditExportRecipientTrust(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	recipientID string,
) (AuditExportRecipientTrustChange, bool, error) {
	var generation int64
	err := querier.QueryRow(ctx, `
		SELECT generation
		FROM audit_export_recipient_trust_events
		WHERE isolation_domain_id = $1 AND recipient_id = $2
		ORDER BY generation DESC
		LIMIT 1
	`, isolationDomainID, recipientID).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditExportRecipientTrustChange{}, false, nil
	}
	if err != nil {
		return AuditExportRecipientTrustChange{}, false,
			fmt.Errorf("read latest audit export recipient trust event: %w", err)
	}
	change, exists, err := readAuditExportRecipientTrustGeneration(
		ctx, querier, isolationDomainID, recipientID, generation,
	)
	return change, exists, err
}

type auditExportRecipientTrustQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func readAuditExportRecipientTrustGeneration(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	recipientID string,
	generation int64,
) (AuditExportRecipientTrustChange, bool, error) {
	change := AuditExportRecipientTrustChange{
		Contract:          AuditExportRecipientTrustAuthorizationContract,
		IsolationDomainID: isolationDomainID,
		RecipientID:       recipientID,
		Generation:        generation,
	}
	var proofVerifiedAt, proofExpiresAt sql.NullTime
	err := querier.QueryRow(ctx, `
		SELECT authorization_contract, operation, trust_contract, trust_profile_sha256,
		       COALESCE(identity_proof_contract, ''), COALESCE(identity_proof_sha256, ''),
		       COALESCE(identity_proof_evidence_sha256, ''), COALESCE(proofing_authority_id, ''),
		       COALESCE(proofing_trust_profile_sha256, ''), COALESCE(proofing_signing_key_id, ''),
		       identity_proof_verified_at, identity_proof_expires_at,
		       actor_id, reason_digest, correlation_id
		FROM audit_export_recipient_trust_events
		WHERE isolation_domain_id = $1 AND recipient_id = $2 AND generation = $3
	`, isolationDomainID, recipientID, generation).Scan(
		&change.Contract, &change.Operation, &change.TrustContract, &change.TrustProfileSHA256,
		&change.IdentityProofContract, &change.IdentityProofSHA256,
		&change.IdentityProofEvidenceSHA256, &change.ProofingAuthorityID,
		&change.ProofingTrustProfileSHA256, &change.ProofingSigningKeyID,
		&proofVerifiedAt, &proofExpiresAt,
		&change.ActorID, &change.ReasonDigest, &change.CorrelationID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditExportRecipientTrustChange{}, false, nil
	}
	if err != nil {
		return AuditExportRecipientTrustChange{}, false,
			fmt.Errorf("read audit export recipient trust event: %w", err)
	}
	if proofVerifiedAt.Valid {
		change.IdentityProofVerifiedAt = proofVerifiedAt.Time
	}
	if proofExpiresAt.Valid {
		change.IdentityProofExpiresAt = proofExpiresAt.Time
	}
	rows, err := querier.Query(ctx, `
		SELECT key_id
		FROM audit_export_recipient_trust_keys
		WHERE isolation_domain_id = $1 AND recipient_id = $2 AND generation = $3
		ORDER BY key_id
	`, isolationDomainID, recipientID, generation)
	if err != nil {
		return AuditExportRecipientTrustChange{}, false,
			fmt.Errorf("read audit export recipient trust keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var keyID string
		if err := rows.Scan(&keyID); err != nil {
			return AuditExportRecipientTrustChange{}, false,
				fmt.Errorf("scan audit export recipient trust key: %w", err)
		}
		change.KeyIDs = append(change.KeyIDs, keyID)
	}
	if err := rows.Err(); err != nil {
		return AuditExportRecipientTrustChange{}, false,
			fmt.Errorf("iterate audit export recipient trust keys: %w", err)
	}
	encryptionRows, err := querier.Query(ctx, `
		SELECT key_id
		FROM audit_export_recipient_encryption_keys
		WHERE isolation_domain_id = $1 AND recipient_id = $2 AND generation = $3
		ORDER BY key_id
	`, isolationDomainID, recipientID, generation)
	if err != nil {
		return AuditExportRecipientTrustChange{}, false,
			fmt.Errorf("read audit export recipient encryption keys: %w", err)
	}
	defer encryptionRows.Close()
	for encryptionRows.Next() {
		var keyID string
		if err := encryptionRows.Scan(&keyID); err != nil {
			return AuditExportRecipientTrustChange{}, false,
				fmt.Errorf("scan audit export recipient encryption key: %w", err)
		}
		change.EncryptionKeyIDs = append(change.EncryptionKeyIDs, keyID)
	}
	if err := encryptionRows.Err(); err != nil {
		return AuditExportRecipientTrustChange{}, false,
			fmt.Errorf("iterate audit export recipient encryption keys: %w", err)
	}
	if !change.Valid() {
		return AuditExportRecipientTrustChange{}, false, ErrAuditExportRecipientTrustConflict
	}
	return change, true, nil
}

func sameAuditExportRecipientTrustChange(left, right AuditExportRecipientTrustChange) bool {
	return left.Contract == right.Contract && left.Operation == right.Operation &&
		left.IsolationDomainID == right.IsolationDomainID && left.RecipientID == right.RecipientID &&
		left.Generation == right.Generation && left.TrustContract == right.TrustContract &&
		left.TrustProfileSHA256 == right.TrustProfileSHA256 && left.ActorID == right.ActorID &&
		left.IdentityProofContract == right.IdentityProofContract &&
		left.IdentityProofSHA256 == right.IdentityProofSHA256 &&
		left.IdentityProofEvidenceSHA256 == right.IdentityProofEvidenceSHA256 &&
		left.ProofingAuthorityID == right.ProofingAuthorityID &&
		left.ProofingTrustProfileSHA256 == right.ProofingTrustProfileSHA256 &&
		left.ProofingSigningKeyID == right.ProofingSigningKeyID &&
		left.IdentityProofVerifiedAt.Equal(right.IdentityProofVerifiedAt) &&
		left.IdentityProofExpiresAt.Equal(right.IdentityProofExpiresAt) &&
		left.CorrelationID == right.CorrelationID && bytes.Equal(left.ReasonDigest, right.ReasonDigest) &&
		sameAuditExportRecipientTrustKeys(left.KeyIDs, right.KeyIDs) &&
		sameAuditExportRecipientTrustKeys(left.EncryptionKeyIDs, right.EncryptionKeyIDs)
}

func validAuditExportRecipientTrustContract(change AuditExportRecipientTrustChange) bool {
	switch change.Contract {
	case AuditExportRecipientEncryptionAuthorizationContract:
		return change.TrustContract == auditExportRecipientEncryptionTrustProfileContract
	case AuditExportRecipientTrustAuthorizationContract, legacyAuditExportRecipientTrustContract:
		return change.TrustContract == auditExportRecipientTrustProfileContract
	default:
		return false
	}
}

func validAuditExportRecipientIdentityProof(change AuditExportRecipientTrustChange) bool {
	if change.Operation == "revoke" || change.Contract == legacyAuditExportRecipientTrustContract {
		return change.IdentityProofContract == "" && change.IdentityProofSHA256 == "" &&
			change.IdentityProofEvidenceSHA256 == "" && change.ProofingAuthorityID == "" &&
			change.ProofingTrustProfileSHA256 == "" && change.ProofingSigningKeyID == "" &&
			change.IdentityProofVerifiedAt.IsZero() && change.IdentityProofExpiresAt.IsZero()
	}
	return change.Operation == "activate" &&
		change.IdentityProofContract == auditExportRecipientIdentityProofContract &&
		auditExportDeliveryDigest.MatchString(change.IdentityProofSHA256) &&
		auditExportDeliveryDigest.MatchString(change.IdentityProofEvidenceSHA256) &&
		auditExportDeliveryRecipient.MatchString(change.ProofingAuthorityID) &&
		auditExportDeliveryDigest.MatchString(change.ProofingTrustProfileSHA256) &&
		auditExportDeliveryKeyID.MatchString(change.ProofingSigningKeyID) &&
		canonicalAuditExportRecipientTrustTime(change.IdentityProofVerifiedAt) &&
		canonicalAuditExportRecipientTrustTime(change.IdentityProofExpiresAt) &&
		change.IdentityProofExpiresAt.After(change.IdentityProofVerifiedAt)
}

func canonicalAuditExportRecipientTrustTime(value time.Time) bool {
	_, offset := value.Zone()
	return !value.IsZero() && offset == 0 && value.Nanosecond()%1000 == 0 && value.Equal(value.UTC())
}

func nullAuditExportRecipientTrustText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullAuditExportRecipientTrustTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func formatAuditExportRecipientTrustTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func validAuditExportRecipientTrustKeys(operation string, keyIDs []string) bool {
	if operation == "revoke" {
		return len(keyIDs) == 0
	}
	if operation != "activate" || len(keyIDs) == 0 || len(keyIDs) > 8 ||
		!sort.StringsAreSorted(keyIDs) {
		return false
	}
	previous := ""
	for _, keyID := range keyIDs {
		if !auditExportDeliveryKeyID.MatchString(keyID) || keyID == previous {
			return false
		}
		previous = keyID
	}
	return true
}

func validAuditExportRecipientEncryptionKeys(change AuditExportRecipientTrustChange) bool {
	if change.Operation == "revoke" || change.Contract != AuditExportRecipientEncryptionAuthorizationContract {
		return len(change.EncryptionKeyIDs) == 0
	}
	return validAuditExportRecipientTrustKeys(change.Operation, change.EncryptionKeyIDs)
}

func containsAuditExportRecipientTrustKey(keyIDs []string, keyID string) bool {
	index := sort.SearchStrings(keyIDs, keyID)
	return index < len(keyIDs) && keyIDs[index] == keyID
}

func sameAuditExportRecipientTrustKeys(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func mapAuditExportRecipientTrustWriteError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) &&
		(databaseError.Code == "23505" || databaseError.Code == "P0001") {
		return ErrAuditExportRecipientTrustConflict
	}
	return fmt.Errorf("record audit export recipient trust change: %w", err)
}
