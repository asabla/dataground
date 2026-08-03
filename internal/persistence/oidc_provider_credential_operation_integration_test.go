package persistence_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestOIDCProviderCredentialOperationIsPreparedCompletedAuditedAndReplaySafe(t *testing.T) {
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

	now := time.Now().UTC().Truncate(time.Second)
	pathDigest := sha256.Sum256([]byte("/run/dataground/credentials/provider.json"))
	reasonDigest := sha256.Sum256([]byte("activate reviewed provider credential"))
	credentialDigest := sha256.Sum256([]byte("provider-secret"))
	operation := persistence.OIDCProviderCredentialOperation{
		Contract:          persistence.OIDCProviderCredentialRequestContract,
		IsolationDomainID: identity.New("iso"), Operation: "activate", Generation: 7,
		ProviderID: "primary", ProviderRegistrySHA256: "1111111111111111111111111111111111111111111111111111111111111111",
		Endpoint: "jwks", PublicationPathDigest: pathDigest[:],
		CredentialDigest: credentialDigest[:],
		ActivatedAt:      now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		ActorID: identity.New("usr"), CorrelationID: identity.New("cor"), ReasonDigest: reasonDigest[:],
	}
	repository := persistence.NewRepository(pool)
	if err := repository.PrepareOIDCProviderCredentialOperation(ctx, operation); err != nil {
		t.Fatalf("prepare operation: %v", err)
	}
	if err := repository.PrepareOIDCProviderCredentialOperation(ctx, operation); err != nil {
		t.Fatalf("replay preparation: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM oidc_provider_credential_operations
		WHERE isolation_domain_id = $1 AND provider_id = $2 AND endpoint = $3 AND generation = $4
	`, operation.IsolationDomainID, operation.ProviderID, operation.Endpoint, operation.Generation).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "prepared" {
		t.Fatalf("prepared status = %q", status)
	}
	if err := repository.CompleteOIDCProviderCredentialOperation(ctx, operation); err != nil {
		t.Fatalf("complete operation: %v", err)
	}
	if err := repository.CompleteOIDCProviderCredentialOperation(ctx, operation); err != nil {
		t.Fatalf("replay completion: %v", err)
	}
	var operations, audits int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM oidc_provider_credential_operations WHERE correlation_id = $1),
			(SELECT count(*) FROM audit_records WHERE correlation_id = $1 AND action = 'oidc-provider-credential.activate')
	`, operation.CorrelationID).Scan(&operations, &audits); err != nil {
		t.Fatal(err)
	}
	if operations != 1 || audits != 1 {
		t.Fatalf("operation rows = %d, audit rows = %d", operations, audits)
	}

	conflict := operation
	conflict.ActorID = identity.New("usr")
	if err := repository.PrepareOIDCProviderCredentialOperation(
		ctx, conflict,
	); !errors.Is(err, persistence.ErrOIDCProviderCredentialOperationConflict) {
		t.Fatalf("changed replay error = %v", err)
	}
	credentialConflict := operation
	changedCredentialDigest := sha256.Sum256([]byte("different-provider-secret"))
	credentialConflict.CredentialDigest = changedCredentialDigest[:]
	if err := repository.PrepareOIDCProviderCredentialOperation(
		ctx, credentialConflict,
	); !errors.Is(err, persistence.ErrOIDCProviderCredentialOperationConflict) {
		t.Fatalf("changed credential replay error = %v", err)
	}
	correlationReuse := operation
	correlationReuse.Generation++
	if err := repository.PrepareOIDCProviderCredentialOperation(
		ctx, correlationReuse,
	); !errors.Is(err, persistence.ErrOIDCProviderCredentialOperationConflict) {
		t.Fatalf("correlation reuse error = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_records SET outcome = 'failed' WHERE correlation_id = $1`,
		operation.CorrelationID); err == nil {
		t.Fatal("database allowed audit record mutation")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM oidc_provider_credential_operations WHERE correlation_id = $1`,
		operation.CorrelationID); err == nil {
		t.Fatal("database allowed operation deletion")
	}
}
