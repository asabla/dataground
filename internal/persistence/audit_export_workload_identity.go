package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	AuditExportWorkloadIdentityAuthorizationContract = "dataground.audit-export-workload-identity-authorization/v1"
	auditExportWorkloadIdentityGrantContract         = "dataground.audit-export-workload-identity-grant/ed25519/v1"
	auditExportWorkloadIdentityAudience              = "dataground.audit-export-transport"
)

var (
	ErrAuditExportWorkloadIdentityInvalid      = errors.New("audit export workload identity change is invalid")
	ErrAuditExportWorkloadIdentityConflict     = errors.New("audit export workload identity conflicts with durable state")
	ErrAuditExportWorkloadIdentityUnauthorized = errors.New("audit export workload identity is not authorized")
)

type AuditExportWorkloadIdentityChange struct {
	Contract                 string
	Operation                string
	IsolationDomainID        string
	WorkloadID               string
	Generation               int64
	GrantContract            string
	GrantSHA256              string
	Audience                 string
	ClientCertificateSHA256  string
	AuthorityID              string
	IssuerTrustProfileSHA256 string
	IssuerSigningKeyID       string
	IssuedAt                 time.Time
	NotBefore                time.Time
	ExpiresAt                time.Time
	ActorID                  string
	ReasonDigest             []byte
	CorrelationID            string
}

type AuditExportWorkloadIdentityAuthorization struct {
	WorkloadID              string
	GrantSHA256             string
	ClientCertificateSHA256 string
	Generation              int64
}

func (change AuditExportWorkloadIdentityChange) Valid() bool {
	if change.Contract != AuditExportWorkloadIdentityAuthorizationContract ||
		(change.Operation != "activate" && change.Operation != "revoke") ||
		!operatorAuditDomainPattern.MatchString(change.IsolationDomainID) ||
		!auditExportDeliveryRecipient.MatchString(change.WorkloadID) ||
		change.Generation < 1 || change.Generation > math.MaxInt64 ||
		!auditExportDeliveryDigest.MatchString(change.GrantSHA256) ||
		!auditExportDeliveryDigest.MatchString(change.ClientCertificateSHA256) ||
		!validOperatorAuditText(change.ActorID, 256) || len(change.ReasonDigest) != sha256.Size ||
		!operatorAuditExportCorrelation.MatchString(change.CorrelationID) {
		return false
	}
	if change.Operation == "revoke" {
		return change.GrantContract == "" && change.Audience == "" && change.AuthorityID == "" &&
			change.IssuerTrustProfileSHA256 == "" && change.IssuerSigningKeyID == "" &&
			change.IssuedAt.IsZero() && change.NotBefore.IsZero() && change.ExpiresAt.IsZero()
	}
	return change.GrantContract == auditExportWorkloadIdentityGrantContract &&
		change.Audience == auditExportWorkloadIdentityAudience &&
		auditExportDeliveryRecipient.MatchString(change.AuthorityID) &&
		auditExportDeliveryDigest.MatchString(change.IssuerTrustProfileSHA256) &&
		auditExportDeliveryKeyID.MatchString(change.IssuerSigningKeyID) &&
		canonicalAuditExportRecipientTrustTime(change.IssuedAt) &&
		canonicalAuditExportRecipientTrustTime(change.NotBefore) &&
		canonicalAuditExportRecipientTrustTime(change.ExpiresAt) &&
		!change.NotBefore.Before(change.IssuedAt) && change.ExpiresAt.After(change.NotBefore)
}

func (authorization AuditExportWorkloadIdentityAuthorization) Valid() bool {
	return auditExportDeliveryRecipient.MatchString(authorization.WorkloadID) &&
		auditExportDeliveryDigest.MatchString(authorization.GrantSHA256) &&
		auditExportDeliveryDigest.MatchString(authorization.ClientCertificateSHA256) &&
		authorization.Generation > 0
}

func (repository *Repository) ChangeAuditExportWorkloadIdentity(
	ctx context.Context,
	change AuditExportWorkloadIdentityChange,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !change.Valid() {
		return ErrAuditExportWorkloadIdentityInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	change.ReasonDigest = append([]byte(nil), change.ReasonDigest...)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin audit export workload identity change: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockAuditExportWorkloadIdentity(ctx, tx, change.IsolationDomainID, change.WorkloadID); err != nil {
		return err
	}
	existing, exists, err := readAuditExportWorkloadIdentityGeneration(
		ctx, tx, change.IsolationDomainID, change.WorkloadID, change.Generation,
	)
	if err != nil {
		return err
	}
	if exists {
		if !sameAuditExportWorkloadIdentityChange(existing, change) {
			return ErrAuditExportWorkloadIdentityConflict
		}
		return tx.Commit(ctx)
	}
	var correlatedDomain, correlatedWorkload string
	var correlatedGeneration int64
	err = tx.QueryRow(ctx, `
		SELECT isolation_domain_id, workload_id, generation
		FROM audit_export_workload_identity_events
		WHERE correlation_id = $1
	`, change.CorrelationID).Scan(&correlatedDomain, &correlatedWorkload, &correlatedGeneration)
	if err == nil {
		return ErrAuditExportWorkloadIdentityConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("inspect audit export workload identity correlation: %w", err)
	}
	latest, latestExists, err := readLatestAuditExportWorkloadIdentity(
		ctx, tx, change.IsolationDomainID, change.WorkloadID,
	)
	if err != nil {
		return err
	}
	expectedGeneration := int64(1)
	if latestExists {
		if latest.Generation == math.MaxInt64 {
			return ErrAuditExportWorkloadIdentityConflict
		}
		expectedGeneration = latest.Generation + 1
	}
	if change.Generation != expectedGeneration ||
		(!latestExists && change.Operation != "activate") ||
		(change.Operation == "revoke" &&
			(latest.Operation != "activate" || latest.GrantSHA256 != change.GrantSHA256 ||
				latest.ClientCertificateSHA256 != change.ClientCertificateSHA256)) ||
		(change.Operation == "activate" && latestExists && latest.Operation == "activate" &&
			latest.GrantSHA256 == change.GrantSHA256) {
		return ErrAuditExportWorkloadIdentityConflict
	}
	if change.Operation == "activate" {
		var databaseNow time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
			return fmt.Errorf("read audit export workload identity clock: %w", err)
		}
		if change.IssuedAt.After(databaseNow.Add(5*time.Minute)) ||
			change.NotBefore.After(databaseNow) || !change.ExpiresAt.After(databaseNow) {
			return ErrAuditExportWorkloadIdentityUnauthorized
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_export_workload_identity_events (
			isolation_domain_id, workload_id, generation, authorization_contract, operation,
			grant_contract, grant_sha256, audience, client_certificate_sha256,
			authority_id, issuer_trust_profile_sha256, issuer_signing_key_id,
			issued_at, not_before, expires_at, actor_id, reason_digest, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
	`, change.IsolationDomainID, change.WorkloadID, change.Generation, change.Contract,
		change.Operation, nullAuditExportRecipientTrustText(change.GrantContract), change.GrantSHA256,
		nullAuditExportRecipientTrustText(change.Audience), change.ClientCertificateSHA256,
		nullAuditExportRecipientTrustText(change.AuthorityID),
		nullAuditExportRecipientTrustText(change.IssuerTrustProfileSHA256),
		nullAuditExportRecipientTrustText(change.IssuerSigningKeyID),
		nullAuditExportRecipientTrustTime(change.IssuedAt),
		nullAuditExportRecipientTrustTime(change.NotBefore),
		nullAuditExportRecipientTrustTime(change.ExpiresAt),
		change.ActorID, change.ReasonDigest, change.CorrelationID); err != nil {
		return mapAuditExportWorkloadIdentityWriteError(err)
	}
	action := "audit-export-workload-identity." + change.Operation
	resourceID := identity.Derived("awi", change.IsolationDomainID+"\n"+change.WorkloadID)
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, $4, 'audit-export-workload-identity', $5, 'accepted', $6,
			jsonb_strip_nulls(jsonb_build_object(
				'generation', $7::bigint,
				'reasonDigest', $8::text,
				'workloadId', $9::text,
				'workloadIdentityGrantSha256', $10::text,
				'clientCertificateSha256', $11::text,
				'workloadIdentityAuthorityId', NULLIF($12::text, ''),
				'workloadIdentityExpiresAt', NULLIF($13::text, '')
			)),
			clock_timestamp()
		)
	`, identity.New("aud"), change.IsolationDomainID, change.ActorID, action, resourceID,
		change.CorrelationID, change.Generation, digestBytes(change.ReasonDigest), change.WorkloadID,
		change.GrantSHA256, change.ClientCertificateSHA256, change.AuthorityID,
		formatAuditExportRecipientTrustTime(change.ExpiresAt)); err != nil {
		return fmt.Errorf("audit export workload identity change: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit audit export workload identity change: %w", err)
	}
	return nil
}

func authorizeAuditExportWorkloadIdentity(
	ctx context.Context,
	tx pgx.Tx,
	isolationDomainID string,
	authorization AuditExportWorkloadIdentityAuthorization,
) error {
	if !authorization.Valid() {
		return ErrAuditExportWorkloadIdentityUnauthorized
	}
	if err := lockAuditExportWorkloadIdentity(ctx, tx, isolationDomainID, authorization.WorkloadID); err != nil {
		return err
	}
	latest, exists, err := readLatestAuditExportWorkloadIdentity(ctx, tx, isolationDomainID, authorization.WorkloadID)
	if err != nil {
		return err
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return fmt.Errorf("read audit export workload identity clock: %w", err)
	}
	if !exists || latest.Operation != "activate" || latest.Generation != authorization.Generation ||
		latest.GrantSHA256 != authorization.GrantSHA256 ||
		latest.ClientCertificateSHA256 != authorization.ClientCertificateSHA256 ||
		latest.Audience != auditExportWorkloadIdentityAudience ||
		latest.NotBefore.After(databaseNow) || !latest.ExpiresAt.After(databaseNow) {
		return ErrAuditExportWorkloadIdentityUnauthorized
	}
	return nil
}

func lockAuditExportWorkloadIdentity(ctx context.Context, tx pgx.Tx, isolationDomainID, workloadID string) error {
	lockKey := "audit-export-workload-identity\n" + isolationDomainID + "\n" + workloadID
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return fmt.Errorf("lock audit export workload identity: %w", err)
	}
	return nil
}

func readLatestAuditExportWorkloadIdentity(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	workloadID string,
) (AuditExportWorkloadIdentityChange, bool, error) {
	var generation int64
	err := querier.QueryRow(ctx, `
		SELECT generation
		FROM audit_export_workload_identity_events
		WHERE isolation_domain_id = $1 AND workload_id = $2
		ORDER BY generation DESC
		LIMIT 1
	`, isolationDomainID, workloadID).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditExportWorkloadIdentityChange{}, false, nil
	}
	if err != nil {
		return AuditExportWorkloadIdentityChange{}, false,
			fmt.Errorf("read latest audit export workload identity event: %w", err)
	}
	return readAuditExportWorkloadIdentityGeneration(ctx, querier, isolationDomainID, workloadID, generation)
}

func readAuditExportWorkloadIdentityGeneration(
	ctx context.Context,
	querier auditExportRecipientTrustQuerier,
	isolationDomainID string,
	workloadID string,
	generation int64,
) (AuditExportWorkloadIdentityChange, bool, error) {
	change := AuditExportWorkloadIdentityChange{
		Contract:          AuditExportWorkloadIdentityAuthorizationContract,
		IsolationDomainID: isolationDomainID, WorkloadID: workloadID, Generation: generation,
	}
	var issuedAt, notBefore, expiresAt sql.NullTime
	err := querier.QueryRow(ctx, `
		SELECT authorization_contract, operation, COALESCE(grant_contract, ''), grant_sha256,
		       COALESCE(audience, ''), client_certificate_sha256, COALESCE(authority_id, ''),
		       COALESCE(issuer_trust_profile_sha256, ''), COALESCE(issuer_signing_key_id, ''),
		       issued_at, not_before, expires_at, actor_id, reason_digest, correlation_id
		FROM audit_export_workload_identity_events
		WHERE isolation_domain_id = $1 AND workload_id = $2 AND generation = $3
	`, isolationDomainID, workloadID, generation).Scan(
		&change.Contract, &change.Operation, &change.GrantContract, &change.GrantSHA256,
		&change.Audience, &change.ClientCertificateSHA256, &change.AuthorityID,
		&change.IssuerTrustProfileSHA256, &change.IssuerSigningKeyID,
		&issuedAt, &notBefore, &expiresAt, &change.ActorID, &change.ReasonDigest, &change.CorrelationID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuditExportWorkloadIdentityChange{}, false, nil
	}
	if err != nil {
		return AuditExportWorkloadIdentityChange{}, false,
			fmt.Errorf("read audit export workload identity event: %w", err)
	}
	if issuedAt.Valid {
		change.IssuedAt = issuedAt.Time
	}
	if notBefore.Valid {
		change.NotBefore = notBefore.Time
	}
	if expiresAt.Valid {
		change.ExpiresAt = expiresAt.Time
	}
	if !change.Valid() {
		return AuditExportWorkloadIdentityChange{}, false, ErrAuditExportWorkloadIdentityConflict
	}
	return change, true, nil
}

func sameAuditExportWorkloadIdentityChange(left, right AuditExportWorkloadIdentityChange) bool {
	return left.Contract == right.Contract && left.Operation == right.Operation &&
		left.IsolationDomainID == right.IsolationDomainID && left.WorkloadID == right.WorkloadID &&
		left.Generation == right.Generation && left.GrantContract == right.GrantContract &&
		left.GrantSHA256 == right.GrantSHA256 && left.Audience == right.Audience &&
		left.ClientCertificateSHA256 == right.ClientCertificateSHA256 &&
		left.AuthorityID == right.AuthorityID &&
		left.IssuerTrustProfileSHA256 == right.IssuerTrustProfileSHA256 &&
		left.IssuerSigningKeyID == right.IssuerSigningKeyID && left.IssuedAt.Equal(right.IssuedAt) &&
		left.NotBefore.Equal(right.NotBefore) && left.ExpiresAt.Equal(right.ExpiresAt) &&
		left.ActorID == right.ActorID && bytes.Equal(left.ReasonDigest, right.ReasonDigest) &&
		left.CorrelationID == right.CorrelationID
}

func mapAuditExportWorkloadIdentityWriteError(err error) error {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) &&
		(databaseError.Code == "23505" || databaseError.Code == "P0001") {
		return ErrAuditExportWorkloadIdentityConflict
	}
	return fmt.Errorf("record audit export workload identity change: %w", err)
}
