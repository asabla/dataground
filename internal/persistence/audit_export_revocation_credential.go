package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const AuditExportRevocationCredentialAuthorizationContract = "dataground.audit-export-revocation-credential-authorization/v1"

var (
	ErrAuditExportRevocationCredentialInvalid      = errors.New("audit export revocation credential change is invalid")
	ErrAuditExportRevocationCredentialConflict     = errors.New("audit export revocation credential change conflicts with durable state")
	ErrAuditExportRevocationCredentialUnauthorized = errors.New("audit export revocation credential is not authorized")
)

type AuditExportRevocationCredentialChange struct {
	Contract             string
	Operation            string
	IsolationDomainID    string
	Purpose              string
	SourceID             string
	SourceRegistrySHA256 string
	Endpoint             string
	Generation           int64
	CredentialSHA256     string
	ActivatedAt          time.Time
	ExpiresAt            time.Time
	ActorID              string
	ReasonDigest         []byte
	CorrelationID        string
}

func (change AuditExportRevocationCredentialChange) Valid() bool {
	validScope := change.Contract == AuditExportRevocationCredentialAuthorizationContract &&
		(change.Operation == "activate" || change.Operation == "revoke") &&
		operatorAuditDomainPattern.MatchString(change.IsolationDomainID) &&
		(change.Purpose == AuditExportRevocationAuthorityPurposeRecipientProof ||
			change.Purpose == AuditExportRevocationAuthorityPurposeWorkloadIdentity) &&
		auditExportDeliveryRecipient.MatchString(change.SourceID) &&
		auditExportDeliveryDigest.MatchString(change.SourceRegistrySHA256) &&
		(change.Endpoint == "notice" || change.Endpoint == "trust") &&
		change.Generation > 0 && change.Generation <= math.MaxInt64 &&
		auditExportDeliveryDigest.MatchString(change.CredentialSHA256) &&
		validOperatorAuditText(change.ActorID, 256) && len(change.ReasonDigest) == sha256.Size &&
		operatorAuditExportCorrelation.MatchString(change.CorrelationID)
	if !validScope {
		return false
	}
	if change.Operation == "revoke" {
		return change.ActivatedAt.IsZero() && change.ExpiresAt.IsZero()
	}
	return canonicalAuditExportRecipientTrustTime(change.ActivatedAt) &&
		canonicalAuditExportRecipientTrustTime(change.ExpiresAt) &&
		change.ExpiresAt.After(change.ActivatedAt) &&
		change.ExpiresAt.Sub(change.ActivatedAt) <= 24*time.Hour
}

type AuditExportRevocationCredentialEvidence struct {
	Endpoint         string
	CredentialSHA256 string
	ActivatedAt      time.Time
	ExpiresAt        time.Time
}

func (evidence AuditExportRevocationCredentialEvidence) validFor(endpoint string) bool {
	return evidence.Endpoint == endpoint &&
		auditExportDeliveryDigest.MatchString(evidence.CredentialSHA256) &&
		canonicalAuditExportRecipientTrustTime(evidence.ActivatedAt) &&
		canonicalAuditExportRecipientTrustTime(evidence.ExpiresAt) &&
		evidence.ExpiresAt.After(evidence.ActivatedAt) &&
		evidence.ExpiresAt.Sub(evidence.ActivatedAt) <= 24*time.Hour
}

type AuditExportRevocationCredentialGenerations struct {
	Notice int64
	Trust  int64
}

func (repository *Repository) ChangeAuditExportRevocationCredential(
	ctx context.Context,
	change AuditExportRevocationCredentialChange,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !change.Valid() {
		return ErrAuditExportRevocationCredentialInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	change.ReasonDigest = append([]byte(nil), change.ReasonDigest...)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit export revocation credential change: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuditExportRevocationCredentialPurpose(
		ctx, tx, change.IsolationDomainID, change.Purpose,
	); err != nil {
		return err
	}
	existing, exists, err := readAuditExportRevocationCredentialGeneration(
		ctx, tx, change.IsolationDomainID, change.Purpose, change.SourceID,
		change.SourceRegistrySHA256, change.Endpoint, change.Generation,
	)
	if err != nil {
		return err
	}
	if exists {
		if !sameAuditExportRevocationCredentialChange(existing, change) {
			return ErrAuditExportRevocationCredentialConflict
		}
		return tx.Commit(ctx)
	}
	var correlation string
	err = tx.QueryRow(ctx, `
		SELECT correlation_id
		FROM audit_export_revocation_credential_events
		WHERE correlation_id = $1
	`, change.CorrelationID).Scan(&correlation)
	if err == nil {
		return ErrAuditExportRevocationCredentialConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect audit export revocation credential correlation: %w", err)
	}
	if change.Operation == "activate" {
		if _, err := requireAuditExportRevocationSource(
			ctx, tx, change.IsolationDomainID, change.Purpose, change.SourceID,
			change.SourceRegistrySHA256, 0,
		); err != nil {
			return err
		}
	}
	var activatedAt, expiresAt *time.Time
	if change.Operation == "activate" {
		activated, expires := change.ActivatedAt, change.ExpiresAt
		activatedAt, expiresAt = &activated, &expires
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_revocation_credential_events (
			authorization_contract, isolation_domain_id, purpose, source_id,
			source_registry_sha256, endpoint, generation, operation,
			credential_sha256, activated_at, expires_at, actor_id,
			reason_digest, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, change.Contract, change.IsolationDomainID, change.Purpose, change.SourceID,
		change.SourceRegistrySHA256, change.Endpoint, change.Generation, change.Operation,
		change.CredentialSHA256, activatedAt, expiresAt, change.ActorID,
		change.ReasonDigest, change.CorrelationID); err != nil {
		return mapAuditExportRevocationCredentialWriteError(err)
	}
	resourceID := identity.Derived("arc", fmt.Sprintf(
		"%s\n%s\n%s\n%s\n%s\n%d", change.IsolationDomainID, change.Purpose,
		change.SourceID, change.SourceRegistrySHA256, change.Endpoint, change.Generation,
	))
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, $4, 'audit-export-revocation-credential', $5,
			'accepted', $6, jsonb_strip_nulls(jsonb_build_object(
				'reasonDigest', $7::text,
				'revocationSourcePurpose', $8::text,
				'revocationSourceId', $9::text,
				'revocationSourceRegistrySha256', $10::text,
				'revocationSourceCredentialEndpoint', $11::text,
				'revocationSourceCredentialGeneration', $12::bigint,
				'revocationSourceCredentialSha256', $13::text,
				'revocationSourceCredentialActivatedAt', $14::text,
				'revocationSourceCredentialExpiresAt', $15::text
			)), clock_timestamp()
		)
	`, identity.New("aud"), change.IsolationDomainID, change.ActorID,
		"audit-export-revocation-credential."+change.Operation, resourceID,
		change.CorrelationID, digestBytes(change.ReasonDigest), change.Purpose,
		change.SourceID, change.SourceRegistrySHA256, change.Endpoint, change.Generation,
		change.CredentialSHA256, nullableAuditExportRevocationCredentialTime(activatedAt),
		nullableAuditExportRevocationCredentialTime(expiresAt)); err != nil {
		return fmt.Errorf("audit export revocation credential change: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export revocation credential change: %w", err)
	}
	return nil
}

func (repository *Repository) AuthorizeAuditExportRevocationCredentials(
	ctx context.Context,
	isolationDomainID string,
	purpose string,
	sourceID string,
	sourceRegistrySHA256 string,
	notice AuditExportRevocationCredentialEvidence,
	trust AuditExportRevocationCredentialEvidence,
) (AuditExportRevocationCredentialGenerations, error) {
	var generations AuditExportRevocationCredentialGenerations
	if repository == nil || repository.pool == nil || ctx == nil ||
		!operatorAuditDomainPattern.MatchString(isolationDomainID) ||
		(purpose != AuditExportRevocationAuthorityPurposeRecipientProof &&
			purpose != AuditExportRevocationAuthorityPurposeWorkloadIdentity) ||
		!auditExportDeliveryRecipient.MatchString(sourceID) ||
		!auditExportDeliveryDigest.MatchString(sourceRegistrySHA256) ||
		!notice.validFor("notice") || !trust.validFor("trust") {
		return generations, ErrAuditExportRevocationCredentialInvalid
	}
	if err := ctx.Err(); err != nil {
		return generations, err
	}
	noticeGeneration, err := requireAuditExportRevocationCredential(
		ctx, repository.pool, isolationDomainID, purpose, sourceID,
		sourceRegistrySHA256, notice, 0,
	)
	if err != nil {
		return generations, err
	}
	trustGeneration, err := requireAuditExportRevocationCredential(
		ctx, repository.pool, isolationDomainID, purpose, sourceID,
		sourceRegistrySHA256, trust, 0,
	)
	if err != nil {
		return generations, err
	}
	return AuditExportRevocationCredentialGenerations{
		Notice: noticeGeneration,
		Trust:  trustGeneration,
	}, nil
}

func requireAuditExportRevocationCredential(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	purpose string,
	sourceID string,
	sourceRegistrySHA256 string,
	evidence AuditExportRevocationCredentialEvidence,
	expectedGeneration int64,
) (int64, error) {
	var generation int64
	err := querier.QueryRow(ctx, `
		SELECT generation
		FROM audit_export_revocation_credential_events
		WHERE isolation_domain_id = $1 AND purpose = $2 AND source_id = $3
		  AND source_registry_sha256 = $4 AND endpoint = $5
		  AND credential_sha256 = $6 AND activated_at = $7 AND expires_at = $8
		  AND operation = 'activate'
		  AND activated_at <= clock_timestamp() AND expires_at > clock_timestamp()
		  AND generation = (
			SELECT max(latest.generation)
			FROM audit_export_revocation_credential_events AS latest
			WHERE latest.isolation_domain_id = $1 AND latest.purpose = $2
			  AND latest.source_id = $3 AND latest.source_registry_sha256 = $4
			  AND latest.endpoint = $5
		  )
	`, isolationDomainID, purpose, sourceID, sourceRegistrySHA256,
		evidence.Endpoint, evidence.CredentialSHA256,
		evidence.ActivatedAt, evidence.ExpiresAt).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) ||
		(err == nil && expectedGeneration > 0 && generation != expectedGeneration) {
		return 0, ErrAuditExportRevocationCredentialUnauthorized
	}
	if err != nil {
		return 0, fmt.Errorf("authorize audit export revocation credential: %w", err)
	}
	return generation, nil
}

func requireAuditExportRevocationCredentialBinding(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	purpose string,
	sourceID string,
	sourceRegistrySHA256 string,
	endpoint string,
	credentialSHA256 string,
	expectedGeneration int64,
) (int64, error) {
	var generation int64
	err := querier.QueryRow(ctx, `
		SELECT generation
		FROM audit_export_revocation_credential_events
		WHERE isolation_domain_id = $1 AND purpose = $2 AND source_id = $3
		  AND source_registry_sha256 = $4 AND endpoint = $5
		  AND credential_sha256 = $6 AND operation = 'activate'
		  AND activated_at <= clock_timestamp() AND expires_at > clock_timestamp()
		  AND generation = (
			SELECT max(latest.generation)
			FROM audit_export_revocation_credential_events AS latest
			WHERE latest.isolation_domain_id = $1 AND latest.purpose = $2
			  AND latest.source_id = $3 AND latest.source_registry_sha256 = $4
			  AND latest.endpoint = $5
		  )
	`, isolationDomainID, purpose, sourceID, sourceRegistrySHA256,
		endpoint, credentialSHA256).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) ||
		(err == nil && expectedGeneration > 0 && generation != expectedGeneration) {
		return 0, ErrAuditExportRevocationCredentialUnauthorized
	}
	if err != nil {
		return 0, fmt.Errorf("authorize audit export revocation credential binding: %w", err)
	}
	return generation, nil
}

func readAuditExportRevocationCredentialGeneration(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	purpose string,
	sourceID string,
	sourceRegistrySHA256 string,
	endpoint string,
	generation int64,
) (AuditExportRevocationCredentialChange, bool, error) {
	var change AuditExportRevocationCredentialChange
	var activatedAt, expiresAt *time.Time
	err := querier.QueryRow(ctx, `
		SELECT authorization_contract, operation, isolation_domain_id, purpose,
		       source_id, source_registry_sha256, endpoint, generation,
		       credential_sha256, activated_at, expires_at, actor_id,
		       reason_digest, correlation_id
		FROM audit_export_revocation_credential_events
		WHERE isolation_domain_id = $1 AND purpose = $2 AND source_id = $3
		  AND source_registry_sha256 = $4 AND endpoint = $5 AND generation = $6
	`, isolationDomainID, purpose, sourceID, sourceRegistrySHA256,
		endpoint, generation).Scan(
		&change.Contract, &change.Operation, &change.IsolationDomainID, &change.Purpose,
		&change.SourceID, &change.SourceRegistrySHA256, &change.Endpoint, &change.Generation,
		&change.CredentialSHA256, &activatedAt, &expiresAt, &change.ActorID,
		&change.ReasonDigest, &change.CorrelationID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditExportRevocationCredentialChange{}, false, nil
	}
	if err != nil {
		return AuditExportRevocationCredentialChange{}, false,
			fmt.Errorf("read audit export revocation credential generation: %w", err)
	}
	if activatedAt != nil {
		change.ActivatedAt = *activatedAt
	}
	if expiresAt != nil {
		change.ExpiresAt = *expiresAt
	}
	if !change.Valid() {
		return AuditExportRevocationCredentialChange{}, false,
			ErrAuditExportRevocationCredentialConflict
	}
	return change, true, nil
}

func sameAuditExportRevocationCredentialChange(
	left AuditExportRevocationCredentialChange,
	right AuditExportRevocationCredentialChange,
) bool {
	return left.Contract == right.Contract && left.Operation == right.Operation &&
		left.IsolationDomainID == right.IsolationDomainID && left.Purpose == right.Purpose &&
		left.SourceID == right.SourceID &&
		left.SourceRegistrySHA256 == right.SourceRegistrySHA256 &&
		left.Endpoint == right.Endpoint && left.Generation == right.Generation &&
		left.CredentialSHA256 == right.CredentialSHA256 &&
		left.ActivatedAt.Equal(right.ActivatedAt) && left.ExpiresAt.Equal(right.ExpiresAt) &&
		left.ActorID == right.ActorID && bytes.Equal(left.ReasonDigest, right.ReasonDigest) &&
		left.CorrelationID == right.CorrelationID
}

func lockAuditExportRevocationCredentialPurpose(
	ctx context.Context,
	tx pgx.Tx,
	isolationDomainID string,
	purpose string,
) error {
	namespace := "audit-export-recipient-proof-revocation"
	if purpose == AuditExportRevocationAuthorityPurposeWorkloadIdentity {
		namespace = "audit-export-workload-identity-revocation"
	}
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	`, namespace+"\n"+isolationDomainID); err != nil {
		return fmt.Errorf("lock audit export revocation credentials: %w", err)
	}
	return nil
}

func nullableAuditExportRevocationCredentialTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatAuditExportRecipientTrustTime(*value)
}

func mapAuditExportRevocationCredentialWriteError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) &&
		(databaseError.Code == "23505" || databaseError.Code == "P0001") {
		return ErrAuditExportRevocationCredentialConflict
	}
	return fmt.Errorf("change audit export revocation credential: %w", err)
}
