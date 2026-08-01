package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
)

var (
	ErrOIDCIdentityBindingInvalid            = errors.New("OIDC identity binding is invalid")
	ErrOIDCIdentityBindingConflict           = errors.New("OIDC identity binding conflicts with an existing record")
	ErrOIDCIdentityBindingMissing            = errors.New("OIDC identity binding is missing")
	ErrOIDCIdentityRevocationInvalid         = errors.New("OIDC identity revocation is invalid")
	ErrOIDCIdentityRevocationConflict        = errors.New("OIDC identity revocation conflicts with an existing record")
	oidcRegistryPrincipalPattern             = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,127}$`)
	oidcRegistryIsolationDomainPattern       = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	oidcRegistryCorrelationIdentifierPattern = regexp.MustCompile(`^cor_[0-9a-z]{20,32}$`)
)

type OIDCIdentityRegistration struct {
	IsolationDomainID         string
	Identity                  authn.OIDCIdentity
	PrincipalID               string
	PrincipalKind             authn.PrincipalKind
	RegisteredBy              string
	RegistrationCorrelationID string
	ReasonDigest              []byte
}

func (record OIDCIdentityRegistration) Valid() bool {
	binding := authn.OIDCIdentityBinding{
		PrincipalID:      record.PrincipalID,
		PrincipalKind:    record.PrincipalKind,
		IsolationDomains: []string{record.IsolationDomainID},
	}
	return record.Identity.Valid() &&
		binding.ValidFor(record.Identity) &&
		oidcRegistryPrincipalPattern.MatchString(record.RegisteredBy) &&
		oidcRegistryCorrelationIdentifierPattern.MatchString(record.RegistrationCorrelationID) &&
		len(record.ReasonDigest) == sha256.Size
}

type OIDCIdentityRevocation struct {
	IsolationDomainID       string
	Identity                authn.OIDCIdentity
	PrincipalID             string
	RevokedBy               string
	RevocationCorrelationID string
	ReasonDigest            []byte
}

func (record OIDCIdentityRevocation) Valid() bool {
	return oidcRegistryIsolationDomainPattern.MatchString(record.IsolationDomainID) &&
		record.Identity.Valid() &&
		oidcRegistryPrincipalPattern.MatchString(record.PrincipalID) &&
		oidcRegistryPrincipalPattern.MatchString(record.RevokedBy) &&
		oidcRegistryCorrelationIdentifierPattern.MatchString(record.RevocationCorrelationID) &&
		len(record.ReasonDigest) == sha256.Size
}

func (repository *Repository) RegisterOIDCIdentity(
	ctx context.Context,
	record OIDCIdentityRegistration,
) error {
	if repository == nil || repository.pool == nil || !record.Valid() {
		return ErrOIDCIdentityBindingInvalid
	}
	record.ReasonDigest = append([]byte(nil), record.ReasonDigest...)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin OIDC identity registration: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockOIDCIdentity(ctx, tx, record.Identity); err != nil {
		return err
	}

	var principalID, principalKind string
	err = tx.QueryRow(ctx, `
		SELECT principal_id, principal_kind
		FROM oidc_identity_bindings
		WHERE issuer = $1 AND subject = $2
		ORDER BY isolation_domain_id
		LIMIT 1
	`, record.Identity.Issuer, record.Identity.Subject).Scan(&principalID, &principalKind)
	switch {
	case err == nil && (principalID != record.PrincipalID || principalKind != string(record.PrincipalKind)):
		return ErrOIDCIdentityBindingConflict
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("inspect OIDC identity registration: %w", err)
	}

	result, err := tx.Exec(ctx, `
		INSERT INTO oidc_identity_bindings (
			isolation_domain_id,
			issuer,
			subject,
			principal_id,
			principal_kind,
			registered_by,
			registration_correlation_id,
			reason_digest
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (isolation_domain_id, issuer, subject) DO NOTHING
	`,
		record.IsolationDomainID,
		record.Identity.Issuer,
		record.Identity.Subject,
		record.PrincipalID,
		string(record.PrincipalKind),
		record.RegisteredBy,
		record.RegistrationCorrelationID,
		record.ReasonDigest,
	)
	if err != nil {
		return fmt.Errorf("register OIDC identity: %w", err)
	}
	if result.RowsAffected() == 0 {
		existing, err := getOIDCIdentityRegistration(
			ctx,
			tx,
			record.IsolationDomainID,
			record.Identity,
		)
		if err != nil {
			return err
		}
		if !sameOIDCIdentityRegistration(existing, record) {
			return ErrOIDCIdentityBindingConflict
		}
		return tx.Commit(ctx)
	}

	if err := auditOIDCIdentityChange(
		ctx,
		tx,
		"oidc-identity-binding.register",
		record.IsolationDomainID,
		record.Identity,
		record.PrincipalID,
		record.PrincipalKind,
		record.RegisteredBy,
		record.RegistrationCorrelationID,
		record.ReasonDigest,
	); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit OIDC identity registration: %w", err)
	}
	return nil
}

func (repository *Repository) RevokeOIDCIdentity(
	ctx context.Context,
	record OIDCIdentityRevocation,
) error {
	if repository == nil || repository.pool == nil || !record.Valid() {
		return ErrOIDCIdentityRevocationInvalid
	}
	record.ReasonDigest = append([]byte(nil), record.ReasonDigest...)
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin OIDC identity revocation: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := lockOIDCIdentity(ctx, tx, record.Identity); err != nil {
		return err
	}

	var principalID, principalKind string
	err = tx.QueryRow(ctx, `
		SELECT principal_id, principal_kind
		FROM oidc_identity_bindings
		WHERE isolation_domain_id = $1 AND issuer = $2 AND subject = $3
		FOR UPDATE
	`, record.IsolationDomainID, record.Identity.Issuer, record.Identity.Subject).Scan(
		&principalID,
		&principalKind,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrOIDCIdentityBindingMissing
	}
	if err != nil {
		return fmt.Errorf("read OIDC identity binding for revocation: %w", err)
	}
	if principalID != record.PrincipalID {
		return ErrOIDCIdentityRevocationConflict
	}

	result, err := tx.Exec(ctx, `
		INSERT INTO oidc_identity_revocations (
			isolation_domain_id,
			issuer,
			subject,
			principal_id,
			revoked_by,
			revocation_correlation_id,
			reason_digest
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (isolation_domain_id, issuer, subject) DO NOTHING
	`,
		record.IsolationDomainID,
		record.Identity.Issuer,
		record.Identity.Subject,
		record.PrincipalID,
		record.RevokedBy,
		record.RevocationCorrelationID,
		record.ReasonDigest,
	)
	if err != nil {
		return fmt.Errorf("revoke OIDC identity: %w", err)
	}
	if result.RowsAffected() == 0 {
		existing, err := getOIDCIdentityRevocation(
			ctx,
			tx,
			record.IsolationDomainID,
			record.Identity,
		)
		if err != nil {
			return err
		}
		if !sameOIDCIdentityRevocation(existing, record) {
			return ErrOIDCIdentityRevocationConflict
		}
		return tx.Commit(ctx)
	}

	if err := auditOIDCIdentityChange(
		ctx,
		tx,
		"oidc-identity-binding.revoke",
		record.IsolationDomainID,
		record.Identity,
		record.PrincipalID,
		authn.PrincipalKind(principalKind),
		record.RevokedBy,
		record.RevocationCorrelationID,
		record.ReasonDigest,
	); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit OIDC identity revocation: %w", err)
	}
	return nil
}

func (repository *Repository) Resolve(
	ctx context.Context,
	externalIdentity authn.OIDCIdentity,
) (authn.OIDCIdentityBinding, error) {
	if repository == nil || repository.pool == nil || !externalIdentity.Valid() {
		return authn.OIDCIdentityBinding{}, ErrOIDCIdentityBindingInvalid
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT binding.isolation_domain_id, binding.principal_id, binding.principal_kind
		FROM oidc_identity_bindings AS binding
		LEFT JOIN oidc_identity_revocations AS revocation
		  ON revocation.isolation_domain_id = binding.isolation_domain_id
		 AND revocation.issuer = binding.issuer
		 AND revocation.subject = binding.subject
		WHERE binding.issuer = $1
		  AND binding.subject = $2
		  AND revocation.isolation_domain_id IS NULL
		ORDER BY binding.isolation_domain_id
	`, externalIdentity.Issuer, externalIdentity.Subject)
	if err != nil {
		return authn.OIDCIdentityBinding{}, fmt.Errorf("resolve OIDC identity: %w", err)
	}
	defer rows.Close()

	var binding authn.OIDCIdentityBinding
	for rows.Next() {
		var domainID, principalID, principalKind string
		if err := rows.Scan(&domainID, &principalID, &principalKind); err != nil {
			return authn.OIDCIdentityBinding{}, fmt.Errorf("scan OIDC identity binding: %w", err)
		}
		if binding.PrincipalID == "" {
			binding.PrincipalID = principalID
			binding.PrincipalKind = authn.PrincipalKind(principalKind)
		} else if binding.PrincipalID != principalID || binding.PrincipalKind != authn.PrincipalKind(principalKind) {
			return authn.OIDCIdentityBinding{}, ErrOIDCIdentityBindingInvalid
		}
		binding.IsolationDomains = append(binding.IsolationDomains, domainID)
	}
	if err := rows.Err(); err != nil {
		return authn.OIDCIdentityBinding{}, fmt.Errorf("read OIDC identity bindings: %w", err)
	}
	if len(binding.IsolationDomains) == 0 {
		return authn.OIDCIdentityBinding{}, authn.ErrIdentityNotFound
	}
	if !binding.ValidFor(externalIdentity) {
		return authn.OIDCIdentityBinding{}, ErrOIDCIdentityBindingInvalid
	}
	binding.IsolationDomains = append([]string(nil), binding.IsolationDomains...)
	return binding, nil
}

func lockOIDCIdentity(ctx context.Context, tx pgx.Tx, externalIdentity authn.OIDCIdentity) error {
	lockKey := externalIdentity.Issuer + "\n" + externalIdentity.Subject
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return fmt.Errorf("lock OIDC identity: %w", err)
	}
	return nil
}

func auditOIDCIdentityChange(
	ctx context.Context,
	tx pgx.Tx,
	action string,
	isolationDomainID string,
	externalIdentity authn.OIDCIdentity,
	principalID string,
	principalKind authn.PrincipalKind,
	actorID string,
	correlationID string,
	reasonDigest []byte,
) error {
	identityDigest := oidcIdentityDigest(externalIdentity)
	bindingDigest := oidcIdentityBindingDigest(
		isolationDomainID,
		externalIdentity,
		principalID,
		principalKind,
	)
	resourceID := identity.Derived(
		"oid",
		isolationDomainID+"\n"+externalIdentity.Issuer+"\n"+externalIdentity.Subject,
	)
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records (
			id,
			isolation_domain_id,
			actor_id,
			action,
			resource_type,
			resource_id,
			outcome,
			correlation_id,
			safe_metadata,
			occurred_at
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			'oidc-identity-binding',
			$5,
			'accepted',
			$6,
			jsonb_build_object(
				'principalId', $7::text,
				'principalKind', $8::text,
				'identityDigest', $9::text,
				'bindingDigest', $10::text,
				'reasonDigest', $11::text
			),
			clock_timestamp()
		)
	`,
		identity.New("aud"),
		isolationDomainID,
		actorID,
		action,
		resourceID,
		correlationID,
		principalID,
		string(principalKind),
		"sha256:"+hex.EncodeToString(identityDigest[:]),
		"sha256:"+hex.EncodeToString(bindingDigest[:]),
		"sha256:"+hex.EncodeToString(reasonDigest),
	); err != nil {
		return fmt.Errorf("audit OIDC identity change: %w", err)
	}
	return nil
}

type oidcIdentityQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getOIDCIdentityRegistration(
	ctx context.Context,
	querier oidcIdentityQuerier,
	isolationDomainID string,
	externalIdentity authn.OIDCIdentity,
) (OIDCIdentityRegistration, error) {
	record := OIDCIdentityRegistration{
		IsolationDomainID: isolationDomainID,
		Identity:          externalIdentity,
	}
	var principalKind string
	err := querier.QueryRow(ctx, `
		SELECT principal_id, principal_kind, registered_by,
		       registration_correlation_id, reason_digest
		FROM oidc_identity_bindings
		WHERE isolation_domain_id = $1 AND issuer = $2 AND subject = $3
	`, isolationDomainID, externalIdentity.Issuer, externalIdentity.Subject).Scan(
		&record.PrincipalID,
		&principalKind,
		&record.RegisteredBy,
		&record.RegistrationCorrelationID,
		&record.ReasonDigest,
	)
	if err != nil {
		return OIDCIdentityRegistration{}, fmt.Errorf("read OIDC identity registration: %w", err)
	}
	record.PrincipalKind = authn.PrincipalKind(principalKind)
	if !record.Valid() {
		return OIDCIdentityRegistration{}, ErrOIDCIdentityBindingInvalid
	}
	return record, nil
}

func getOIDCIdentityRevocation(
	ctx context.Context,
	querier oidcIdentityQuerier,
	isolationDomainID string,
	externalIdentity authn.OIDCIdentity,
) (OIDCIdentityRevocation, error) {
	record := OIDCIdentityRevocation{
		IsolationDomainID: isolationDomainID,
		Identity:          externalIdentity,
	}
	err := querier.QueryRow(ctx, `
		SELECT principal_id, revoked_by, revocation_correlation_id, reason_digest
		FROM oidc_identity_revocations
		WHERE isolation_domain_id = $1 AND issuer = $2 AND subject = $3
	`, isolationDomainID, externalIdentity.Issuer, externalIdentity.Subject).Scan(
		&record.PrincipalID,
		&record.RevokedBy,
		&record.RevocationCorrelationID,
		&record.ReasonDigest,
	)
	if err != nil {
		return OIDCIdentityRevocation{}, fmt.Errorf("read OIDC identity revocation: %w", err)
	}
	if !record.Valid() {
		return OIDCIdentityRevocation{}, ErrOIDCIdentityRevocationInvalid
	}
	return record, nil
}

func sameOIDCIdentityRegistration(left OIDCIdentityRegistration, right OIDCIdentityRegistration) bool {
	return left.IsolationDomainID == right.IsolationDomainID &&
		left.Identity == right.Identity &&
		left.PrincipalID == right.PrincipalID &&
		left.PrincipalKind == right.PrincipalKind &&
		left.RegisteredBy == right.RegisteredBy &&
		left.RegistrationCorrelationID == right.RegistrationCorrelationID &&
		bytes.Equal(left.ReasonDigest, right.ReasonDigest)
}

func sameOIDCIdentityRevocation(left OIDCIdentityRevocation, right OIDCIdentityRevocation) bool {
	return left.IsolationDomainID == right.IsolationDomainID &&
		left.Identity == right.Identity &&
		left.PrincipalID == right.PrincipalID &&
		left.RevokedBy == right.RevokedBy &&
		left.RevocationCorrelationID == right.RevocationCorrelationID &&
		bytes.Equal(left.ReasonDigest, right.ReasonDigest)
}

func oidcIdentityDigest(externalIdentity authn.OIDCIdentity) [sha256.Size]byte {
	return digestOIDCIdentityValues(externalIdentity.Issuer, externalIdentity.Subject)
}

func oidcIdentityBindingDigest(
	isolationDomainID string,
	externalIdentity authn.OIDCIdentity,
	principalID string,
	principalKind authn.PrincipalKind,
) [sha256.Size]byte {
	return digestOIDCIdentityValues(
		isolationDomainID,
		externalIdentity.Issuer,
		externalIdentity.Subject,
		principalID,
		string(principalKind),
	)
}

func digestOIDCIdentityValues(values ...string) [sha256.Size]byte {
	digest := sha256.New()
	var size [8]byte
	for _, value := range values {
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = digest.Write(size[:])
		_, _ = digest.Write([]byte(value))
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

var _ authn.OIDCIdentityResolver = (*Repository)(nil)
