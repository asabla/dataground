package persistence_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func TestProviderCredentialGrantAuthorizationLifecycle(t *testing.T) {
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
	defer func() {
		if _, cleanupErr := pool.Exec(context.Background(), `
			TRUNCATE provider_credential_authorization_decisions,
			         provider_credential_grant_events
		`); cleanupErr != nil {
			t.Errorf("clean provider credential evidence: %v", cleanupErr)
		}
	}()

	domainID := "iso_" + strings.Repeat("a", 20)
	revisionID := "rev_" + strings.Repeat("b", 20)
	operationID := "op_" + strings.Repeat("c", 20)
	actorID := "operator-one"
	reason := sha256.Sum256([]byte("authorize reviewed OpenShell mediation"))
	activatedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	activate := persistence.ProviderCredentialGrantChange{
		Contract:          persistence.ProviderCredentialGrantContract,
		IsolationDomainID: domainID, RevisionID: revisionID,
		ProviderProfile: "codex", Purpose: persistence.ProviderCredentialPurposeAgentInference,
		Generation: 1, Operation: "activate", ActivatedAt: activatedAt, ExpiresAt: expiresAt,
		ActorID: actorID, ReasonDigest: append([]byte(nil), reason[:]...),
		CorrelationID: "cor_" + strings.Repeat("d", 20),
	}
	if err := repository.ChangeProviderCredentialGrant(ctx, activate); err != nil {
		t.Fatalf("activate grant: %v", err)
	}
	if err := repository.ChangeProviderCredentialGrant(ctx, activate); err != nil {
		t.Fatalf("replay activation: %v", err)
	}
	use := persistence.ProviderCredentialUse{
		Contract:          persistence.ProviderCredentialAuthorizationContract,
		IsolationDomainID: domainID, RevisionID: revisionID, OperationID: operationID,
		ProviderProfile: "codex", Purpose: persistence.ProviderCredentialPurposeAgentInference,
		Phase: persistence.ProviderCredentialPhaseAdmission, ActorID: actorID,
		CorrelationID: "cor_" + strings.Repeat("e", 20),
	}
	if err := repository.AuthorizeProviderCredentialUse(ctx, use); err != nil {
		t.Fatalf("authorize admission: %v", err)
	}
	use.Phase = persistence.ProviderCredentialPhaseEffect
	if err := repository.AuthorizeProviderCredentialUse(ctx, use); err != nil {
		t.Fatalf("authorize effect: %v", err)
	}

	substituted := use
	substituted.ProviderProfile = "other"
	if err := repository.AuthorizeProviderCredentialUse(ctx, substituted); !errors.Is(err, persistence.ErrProviderCredentialUnauthorized) {
		t.Fatalf("substituted profile error = %v", err)
	}
	crossDomain := use
	crossDomain.IsolationDomainID = "iso_" + strings.Repeat("z", 20)
	if err := repository.AuthorizeProviderCredentialUse(ctx, crossDomain); !errors.Is(err, persistence.ErrProviderCredentialUnauthorized) {
		t.Fatalf("cross-domain error = %v", err)
	}

	revokeReason := sha256.Sum256([]byte("revoke local use"))
	revoke := activate
	revoke.Generation = 2
	revoke.Operation = "revoke"
	revoke.ActivatedAt = time.Time{}
	revoke.ExpiresAt = time.Time{}
	revoke.ReasonDigest = append([]byte(nil), revokeReason[:]...)
	revoke.CorrelationID = "cor_" + strings.Repeat("f", 20)
	if err := repository.ChangeProviderCredentialGrant(ctx, revoke); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	if err := repository.AuthorizeProviderCredentialUse(ctx, use); !errors.Is(err, persistence.ErrProviderCredentialUnauthorized) {
		t.Fatalf("revoked grant error = %v", err)
	}

	expiringRevision := "rev_" + strings.Repeat("g", 20)
	expiring := activate
	expiring.RevisionID = expiringRevision
	expiring.ExpiresAt = time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
	expiring.CorrelationID = "cor_" + strings.Repeat("h", 20)
	if err := repository.ChangeProviderCredentialGrant(ctx, expiring); err != nil {
		t.Fatalf("activate expiring grant: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	expiredUse := use
	expiredUse.RevisionID = expiringRevision
	if err := repository.AuthorizeProviderCredentialUse(ctx, expiredUse); !errors.Is(err, persistence.ErrProviderCredentialUnauthorized) {
		t.Fatalf("expired grant error = %v", err)
	}

	var allowed, denied int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE outcome = 'allowed'),
			count(*) FILTER (WHERE outcome = 'denied')
		FROM provider_credential_authorization_decisions
		WHERE isolation_domain_id = $1 OR isolation_domain_id = $2
	`, domainID, crossDomain.IsolationDomainID).Scan(&allowed, &denied); err != nil {
		t.Fatal(err)
	}
	if allowed != 2 || denied != 4 {
		t.Fatalf("decision outcomes allowed=%d denied=%d", allowed, denied)
	}
	var grantEvents, grantAudits int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM provider_credential_grant_events),
			(SELECT count(*) FROM audit_records WHERE resource_type = 'provider-credential-grant')
	`).Scan(&grantEvents, &grantAudits); err != nil {
		t.Fatal(err)
	}
	if grantEvents != 3 || grantAudits != 3 {
		t.Fatalf("grant evidence events=%d audits=%d", grantEvents, grantAudits)
	}
}

func TestProviderCredentialContractsContainNoSecretMaterial(t *testing.T) {
	for _, contract := range []any{
		persistence.ProviderCredentialGrantChange{},
		persistence.ProviderCredentialUse{},
	} {
		contractType := reflect.TypeOf(contract)
		for index := 0; index < contractType.NumField(); index++ {
			name := strings.ToLower(contractType.Field(index).Name)
			if strings.Contains(name, "secret") || strings.Contains(name, "token") ||
				strings.Contains(name, "password") || strings.Contains(name, "credentialvalue") {
				t.Fatalf("%s exposes secret field %q", contractType.Name(), name)
			}
		}
	}
}
