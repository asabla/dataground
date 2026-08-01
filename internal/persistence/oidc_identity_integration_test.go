package persistence_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestOIDCIdentityRegistryIsScopedAuditedReplaySafeAndRevocable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL := testDatabaseURL(t)
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 0); err != nil {
		database.Close()
		t.Fatalf("reset schema: %v", err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		database.Close()
		t.Fatalf("migrate schema: %v", err)
	}
	database.Close()
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	repository := persistence.NewRepository(pool)
	externalIdentity := authn.OIDCIdentity{
		Issuer:  "https://identity.example.invalid/realms/dataground",
		Subject: "subject-0001",
	}
	principalID := identity.New("usr")
	actorID := identity.New("usr")
	domains := []string{identity.New("iso"), identity.New("iso")}
	registrations := make([]persistence.OIDCIdentityRegistration, 0, len(domains))
	for index := len(domains) - 1; index >= 0; index-- {
		reasonDigest := sha256.Sum256([]byte("register domain " + domains[index]))
		record := persistence.OIDCIdentityRegistration{
			IsolationDomainID:         domains[index],
			Identity:                  externalIdentity,
			PrincipalID:               principalID,
			PrincipalKind:             authn.PrincipalHuman,
			RegisteredBy:              actorID,
			RegistrationCorrelationID: identity.New("cor"),
			ReasonDigest:              reasonDigest[:],
		}
		if err := repository.RegisterOIDCIdentity(ctx, record); err != nil {
			t.Fatalf("register domain identity: %v", err)
		}
		if err := repository.RegisterOIDCIdentity(ctx, record); err != nil {
			t.Fatalf("replay domain identity registration: %v", err)
		}
		registrations = append(registrations, record)
	}

	binding, err := repository.Resolve(ctx, externalIdentity)
	if err != nil {
		t.Fatalf("resolve registered identity: %v", err)
	}
	wantDomains := append([]string(nil), domains...)
	sort.Strings(wantDomains)
	if binding.PrincipalID != principalID ||
		binding.PrincipalKind != authn.PrincipalHuman ||
		strings.Join(binding.IsolationDomains, ",") != strings.Join(wantDomains, ",") {
		t.Fatalf("resolved binding = %#v", binding)
	}

	conflictingReplay := registrations[0]
	conflictingReplay.RegisteredBy = identity.New("usr")
	if err := repository.RegisterOIDCIdentity(
		ctx,
		conflictingReplay,
	); !errors.Is(err, persistence.ErrOIDCIdentityBindingConflict) {
		t.Fatalf("changed registration replay error = %v", err)
	}
	conflictingPrincipal := registrations[0]
	conflictingPrincipal.IsolationDomainID = identity.New("iso")
	conflictingPrincipal.PrincipalID = identity.New("usr")
	conflictingPrincipal.RegistrationCorrelationID = identity.New("cor")
	if err := repository.RegisterOIDCIdentity(
		ctx,
		conflictingPrincipal,
	); !errors.Is(err, persistence.ErrOIDCIdentityBindingConflict) {
		t.Fatalf("cross-domain principal conflict error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO oidc_identity_bindings (
			isolation_domain_id, issuer, subject, principal_id, principal_kind,
			registered_by, registration_correlation_id, reason_digest
		) VALUES ($1, $2, $3, $4, 'human', $5, $6, $7)
	`,
		conflictingPrincipal.IsolationDomainID,
		externalIdentity.Issuer,
		externalIdentity.Subject,
		conflictingPrincipal.PrincipalID,
		actorID,
		identity.New("cor"),
		conflictingPrincipal.ReasonDigest,
	); err == nil {
		t.Fatal("database accepted a cross-domain principal conflict")
	}

	revocationReason := sha256.Sum256([]byte("remove one domain membership"))
	revocation := persistence.OIDCIdentityRevocation{
		IsolationDomainID:       domains[0],
		Identity:                externalIdentity,
		PrincipalID:             principalID,
		RevokedBy:               actorID,
		RevocationCorrelationID: identity.New("cor"),
		ReasonDigest:            revocationReason[:],
	}
	if err := repository.RevokeOIDCIdentity(ctx, revocation); err != nil {
		t.Fatalf("revoke domain identity: %v", err)
	}
	if err := repository.RevokeOIDCIdentity(ctx, revocation); err != nil {
		t.Fatalf("replay domain identity revocation: %v", err)
	}
	changedRevocation := revocation
	changedRevocation.RevocationCorrelationID = identity.New("cor")
	if err := repository.RevokeOIDCIdentity(
		ctx,
		changedRevocation,
	); !errors.Is(err, persistence.ErrOIDCIdentityRevocationConflict) {
		t.Fatalf("changed revocation replay error = %v", err)
	}
	wrongPrincipal := revocation
	wrongPrincipal.IsolationDomainID = domains[1]
	wrongPrincipal.PrincipalID = identity.New("usr")
	wrongPrincipal.RevocationCorrelationID = identity.New("cor")
	if err := repository.RevokeOIDCIdentity(
		ctx,
		wrongPrincipal,
	); !errors.Is(err, persistence.ErrOIDCIdentityRevocationConflict) {
		t.Fatalf("wrong-principal revocation error = %v", err)
	}

	binding, err = repository.Resolve(ctx, externalIdentity)
	if err != nil {
		t.Fatalf("resolve partially revoked identity: %v", err)
	}
	if len(binding.IsolationDomains) != 1 || binding.IsolationDomains[0] != domains[1] {
		t.Fatalf("partially revoked domains = %#v", binding.IsolationDomains)
	}
	finalRevocation := revocation
	finalRevocation.IsolationDomainID = domains[1]
	finalRevocation.RevocationCorrelationID = identity.New("cor")
	if err := repository.RevokeOIDCIdentity(ctx, finalRevocation); err != nil {
		t.Fatalf("revoke final domain identity: %v", err)
	}
	if _, err := repository.Resolve(ctx, externalIdentity); !errors.Is(err, authn.ErrIdentityNotFound) {
		t.Fatalf("fully revoked identity error = %v", err)
	}

	var auditCount int
	var auditMetadata string
	if err := pool.QueryRow(ctx, `
		SELECT count(*), string_agg(safe_metadata::text, '')
		FROM audit_records
		WHERE resource_type = 'oidc-identity-binding'
		  AND actor_id = $1
	`, actorID).Scan(&auditCount, &auditMetadata); err != nil {
		t.Fatalf("read identity audit: %v", err)
	}
	if auditCount != 4 ||
		strings.Contains(auditMetadata, externalIdentity.Issuer) ||
		strings.Contains(auditMetadata, externalIdentity.Subject) {
		t.Fatalf("identity audit = count %d metadata %q", auditCount, auditMetadata)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE oidc_identity_bindings
		SET principal_id = $1
		WHERE isolation_domain_id = $2 AND issuer = $3 AND subject = $4
	`, identity.New("usr"), domains[0], externalIdentity.Issuer, externalIdentity.Subject); err == nil {
		t.Fatal("identity binding update was accepted")
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM oidc_identity_revocations
		WHERE isolation_domain_id = $1 AND issuer = $2 AND subject = $3
	`, domains[0], externalIdentity.Issuer, externalIdentity.Subject); err == nil {
		t.Fatal("identity revocation deletion was accepted")
	}
}

func TestOIDCIdentityRegistryRejectsInvalidAndMissingRecords(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL := testDatabaseURL(t)
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 0); err != nil {
		database.Close()
		t.Fatalf("reset schema: %v", err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		database.Close()
		t.Fatalf("migrate schema: %v", err)
	}
	database.Close()
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	repository := persistence.NewRepository(pool)
	externalIdentity := authn.OIDCIdentity{
		Issuer:  "https://identity.example.invalid/realms/dataground",
		Subject: "unknown-subject",
	}
	if _, err := repository.Resolve(ctx, externalIdentity); !errors.Is(err, authn.ErrIdentityNotFound) {
		t.Fatalf("unknown identity error = %v", err)
	}
	reasonDigest := sha256.Sum256([]byte("missing identity"))
	err = repository.RevokeOIDCIdentity(ctx, persistence.OIDCIdentityRevocation{
		IsolationDomainID:       identity.New("iso"),
		Identity:                externalIdentity,
		PrincipalID:             identity.New("usr"),
		RevokedBy:               identity.New("usr"),
		RevocationCorrelationID: identity.New("cor"),
		ReasonDigest:            reasonDigest[:],
	})
	if !errors.Is(err, persistence.ErrOIDCIdentityBindingMissing) {
		t.Fatalf("missing revocation error = %v", err)
	}
	if _, err := repository.Resolve(
		ctx,
		authn.OIDCIdentity{Issuer: "http://identity.example.invalid", Subject: "subject"},
	); !errors.Is(err, persistence.ErrOIDCIdentityBindingInvalid) {
		t.Fatalf("invalid identity error = %v", err)
	}
}
