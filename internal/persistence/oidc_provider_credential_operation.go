package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
)

const OIDCProviderCredentialRequestContract = "dataground.oidc-provider-credential-request/v2"

var (
	ErrOIDCProviderCredentialOperationInvalid  = errors.New("OIDC provider credential operation is invalid")
	ErrOIDCProviderCredentialOperationConflict = errors.New("OIDC provider credential operation conflicts with durable state")
	oidcProviderCredentialDomainPattern        = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	oidcProviderCredentialProviderPattern      = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
	oidcProviderCredentialActorPattern         = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,127}$`)
	oidcProviderCredentialCorrelationPattern   = regexp.MustCompile(`^cor_[0-9a-z]{20,32}$`)
)

// OIDCProviderCredentialOperation is the safe, operator-attributed portion of
// one credential publication. It deliberately excludes credential bytes.
type OIDCProviderCredentialOperation struct {
	Contract               string
	IsolationDomainID      string
	Operation              string
	Generation             uint64
	ProviderID             string
	ProviderRegistrySHA256 string
	Endpoint               string
	PublicationPathDigest  []byte
	CredentialDigest       []byte
	ActivatedAt            time.Time
	ExpiresAt              time.Time
	RevokedAt              time.Time
	ActorID                string
	CorrelationID          string
	ReasonDigest           []byte
}

func (operation OIDCProviderCredentialOperation) Valid() bool {
	if operation.Contract != OIDCProviderCredentialRequestContract ||
		!oidcProviderCredentialDomainPattern.MatchString(operation.IsolationDomainID) ||
		(operation.Generation == 0 || operation.Generation > math.MaxInt64) ||
		!oidcProviderCredentialProviderPattern.MatchString(operation.ProviderID) ||
		len(operation.ProviderRegistrySHA256) != sha256.Size*2 ||
		!validLowerHex(operation.ProviderRegistrySHA256) ||
		(operation.Endpoint != "discovery" && operation.Endpoint != "jwks") ||
		len(operation.PublicationPathDigest) != sha256.Size ||
		len(operation.CredentialDigest) != sha256.Size ||
		!oidcProviderCredentialActorPattern.MatchString(operation.ActorID) ||
		!oidcProviderCredentialCorrelationPattern.MatchString(operation.CorrelationID) ||
		len(operation.ReasonDigest) != sha256.Size {
		return false
	}
	switch operation.Operation {
	case "activate":
		return !operation.ActivatedAt.IsZero() && !operation.ExpiresAt.IsZero() &&
			credentialTimestampExact(operation.ActivatedAt) && credentialTimestampExact(operation.ExpiresAt) &&
			operation.ExpiresAt.After(operation.ActivatedAt) &&
			operation.ExpiresAt.Sub(operation.ActivatedAt) <= 31*24*time.Hour && operation.RevokedAt.IsZero()
	case "revoke":
		return operation.ActivatedAt.IsZero() && operation.ExpiresAt.IsZero() &&
			!operation.RevokedAt.IsZero() && credentialTimestampExact(operation.RevokedAt)
	default:
		return false
	}
}

func (repository *Repository) PrepareOIDCProviderCredentialOperation(
	ctx context.Context,
	operation OIDCProviderCredentialOperation,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !operation.Valid() {
		return ErrOIDCProviderCredentialOperationInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	operation = cloneOIDCProviderCredentialOperation(operation)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin OIDC provider credential operation: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		operation.IsolationDomainID+"\n"+operation.ProviderID+"\n"+operation.Endpoint); err != nil {
		return fmt.Errorf("lock OIDC provider credential operation: %w", err)
	}
	if err := rejectOIDCProviderCredentialCorrelationReuse(ctx, tx, operation); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `
		INSERT INTO oidc_provider_credential_operations (
			isolation_domain_id, provider_id, endpoint, generation, contract, operation,
			provider_registry_sha256, publication_path_digest, credential_digest, activated_at, expires_at,
			revoked_at, actor_id, correlation_id, reason_digest
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (isolation_domain_id, provider_id, endpoint, generation) DO NOTHING
	`, operation.IsolationDomainID, operation.ProviderID, operation.Endpoint, operation.Generation,
		operation.Contract, operation.Operation, operation.ProviderRegistrySHA256,
		operation.PublicationPathDigest, operation.CredentialDigest, nullableCredentialTime(operation.ActivatedAt),
		nullableCredentialTime(operation.ExpiresAt), nullableCredentialTime(operation.RevokedAt),
		operation.ActorID, operation.CorrelationID, operation.ReasonDigest)
	if err != nil {
		return fmt.Errorf("prepare OIDC provider credential operation: %w", err)
	}
	if result.RowsAffected() == 0 {
		existing, _, err := readOIDCProviderCredentialOperation(ctx, tx, operation)
		if err != nil {
			return err
		}
		if !sameOIDCProviderCredentialOperation(existing, operation) {
			return ErrOIDCProviderCredentialOperationConflict
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit OIDC provider credential operation preparation: %w", err)
	}
	return nil
}

func (repository *Repository) CompleteOIDCProviderCredentialOperation(
	ctx context.Context,
	operation OIDCProviderCredentialOperation,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !operation.Valid() {
		return ErrOIDCProviderCredentialOperationInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	operation = cloneOIDCProviderCredentialOperation(operation)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin OIDC provider credential completion: %w", err)
	}
	defer tx.Rollback(ctx)
	existing, status, err := readOIDCProviderCredentialOperation(ctx, tx, operation)
	if err != nil {
		return err
	}
	if !sameOIDCProviderCredentialOperation(existing, operation) {
		return ErrOIDCProviderCredentialOperationConflict
	}
	if status == "succeeded" {
		return tx.Commit(ctx)
	}
	if status != "prepared" {
		return ErrOIDCProviderCredentialOperationConflict
	}
	if _, err := tx.Exec(ctx, `
		UPDATE oidc_provider_credential_operations
		SET status = 'succeeded', completed_at = clock_timestamp()
		WHERE isolation_domain_id = $1 AND provider_id = $2 AND endpoint = $3 AND generation = $4
	`, operation.IsolationDomainID, operation.ProviderID, operation.Endpoint, operation.Generation); err != nil {
		return fmt.Errorf("complete OIDC provider credential operation: %w", err)
	}
	resourceID := identity.Derived("opc", operation.IsolationDomainID+"\n"+operation.ProviderID+"\n"+operation.Endpoint)
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			$1, $2, $3, $4, 'oidc-provider-credential', $5,
			'succeeded', $6,
			jsonb_build_object(
				'generation', $7::bigint,
				'providerId', $8::text,
				'endpoint', $9::text,
				'providerRegistrySha256', $10::text,
				'publicationPathDigest', $11::text,
				'reasonDigest', $12::text
			),
			clock_timestamp()
		)
	`, identity.New("aud"), operation.IsolationDomainID, operation.ActorID,
		"oidc-provider-credential."+operation.Operation, resourceID, operation.CorrelationID,
		operation.Generation, operation.ProviderID, operation.Endpoint,
		"sha256:"+operation.ProviderRegistrySHA256,
		"sha256:"+hex.EncodeToString(operation.PublicationPathDigest),
		"sha256:"+hex.EncodeToString(operation.ReasonDigest)); err != nil {
		return fmt.Errorf("audit OIDC provider credential operation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit OIDC provider credential completion: %w", err)
	}
	return nil
}

type oidcProviderCredentialOperationQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readOIDCProviderCredentialOperation(
	ctx context.Context,
	querier oidcProviderCredentialOperationQuerier,
	key OIDCProviderCredentialOperation,
) (OIDCProviderCredentialOperation, string, error) {
	var operation OIDCProviderCredentialOperation
	var status string
	var activatedAt, expiresAt, revokedAt *time.Time
	err := querier.QueryRow(ctx, `
		SELECT contract, isolation_domain_id, operation, generation, provider_id,
		       provider_registry_sha256, endpoint, publication_path_digest, credential_digest,
		       activated_at, expires_at, revoked_at, actor_id, correlation_id,
		       reason_digest, status
		FROM oidc_provider_credential_operations
		WHERE isolation_domain_id = $1 AND provider_id = $2 AND endpoint = $3 AND generation = $4
		FOR UPDATE
	`, key.IsolationDomainID, key.ProviderID, key.Endpoint, key.Generation).Scan(
		&operation.Contract, &operation.IsolationDomainID, &operation.Operation, &operation.Generation,
		&operation.ProviderID, &operation.ProviderRegistrySHA256, &operation.Endpoint,
		&operation.PublicationPathDigest, &operation.CredentialDigest, &activatedAt, &expiresAt, &revokedAt,
		&operation.ActorID, &operation.CorrelationID, &operation.ReasonDigest, &status,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OIDCProviderCredentialOperation{}, "", ErrOIDCProviderCredentialOperationConflict
	}
	if err != nil {
		return OIDCProviderCredentialOperation{}, "", fmt.Errorf("read OIDC provider credential operation: %w", err)
	}
	if activatedAt != nil {
		operation.ActivatedAt = activatedAt.UTC()
	}
	if expiresAt != nil {
		operation.ExpiresAt = expiresAt.UTC()
	}
	if revokedAt != nil {
		operation.RevokedAt = revokedAt.UTC()
	}
	if !operation.Valid() {
		return OIDCProviderCredentialOperation{}, "", ErrOIDCProviderCredentialOperationConflict
	}
	return operation, status, nil
}

func rejectOIDCProviderCredentialCorrelationReuse(
	ctx context.Context,
	querier oidcProviderCredentialOperationQuerier,
	operation OIDCProviderCredentialOperation,
) error {
	var domainID, providerID, endpoint string
	var generation uint64
	err := querier.QueryRow(ctx, `
		SELECT isolation_domain_id, provider_id, endpoint, generation
		FROM oidc_provider_credential_operations
		WHERE correlation_id = $1
	`, operation.CorrelationID).Scan(&domainID, &providerID, &endpoint, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read OIDC provider credential correlation: %w", err)
	}
	if domainID != operation.IsolationDomainID || providerID != operation.ProviderID ||
		endpoint != operation.Endpoint || generation != operation.Generation {
		return ErrOIDCProviderCredentialOperationConflict
	}
	return nil
}

func sameOIDCProviderCredentialOperation(left, right OIDCProviderCredentialOperation) bool {
	return left.Contract == right.Contract && left.IsolationDomainID == right.IsolationDomainID &&
		left.Operation == right.Operation && left.Generation == right.Generation &&
		left.ProviderID == right.ProviderID && left.ProviderRegistrySHA256 == right.ProviderRegistrySHA256 &&
		left.Endpoint == right.Endpoint && bytes.Equal(left.PublicationPathDigest, right.PublicationPathDigest) &&
		subtle.ConstantTimeCompare(left.CredentialDigest, right.CredentialDigest) == 1 &&
		left.ActivatedAt.Equal(right.ActivatedAt) && left.ExpiresAt.Equal(right.ExpiresAt) &&
		left.RevokedAt.Equal(right.RevokedAt) && left.ActorID == right.ActorID &&
		left.CorrelationID == right.CorrelationID && bytes.Equal(left.ReasonDigest, right.ReasonDigest)
}

func cloneOIDCProviderCredentialOperation(operation OIDCProviderCredentialOperation) OIDCProviderCredentialOperation {
	operation.ActivatedAt = operation.ActivatedAt.UTC()
	operation.ExpiresAt = operation.ExpiresAt.UTC()
	operation.RevokedAt = operation.RevokedAt.UTC()
	operation.PublicationPathDigest = append([]byte(nil), operation.PublicationPathDigest...)
	operation.CredentialDigest = append([]byte(nil), operation.CredentialDigest...)
	operation.ReasonDigest = append([]byte(nil), operation.ReasonDigest...)
	return operation
}

func nullableCredentialTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func credentialTimestampExact(value time.Time) bool {
	return value.Equal(value.Truncate(time.Microsecond))
}

func validLowerHex(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
