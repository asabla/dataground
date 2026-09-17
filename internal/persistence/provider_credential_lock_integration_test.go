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

func TestProviderCredentialAuthorizationSerializesWithDatabaseGrantChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL := testDatabaseURL(t)
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	defer func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE provider_credential_authorization_decisions, provider_credential_grant_events`); err != nil {
			t.Errorf("clean provider credential fixtures: %v", err)
		}
	}()

	for _, mode := range []string{"commit-admission", "commit-effect", "rollback", "expiry", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			scope, revision := identity.New("iso"), identity.New("rev")
			reason := sha256.Sum256([]byte("reviewed concurrency fixture"))
			expires := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
			if mode == "expiry" {
				expires = time.Now().UTC().Add(2 * time.Second).Truncate(time.Microsecond)
			}
			grant := persistence.ProviderCredentialGrantChange{
				Contract:          persistence.ProviderCredentialGrantContract,
				IsolationDomainID: scope, RevisionID: revision, ProviderProfile: "codex",
				Purpose:    persistence.ProviderCredentialPurposeAgentInference,
				Generation: 1, Operation: "activate", ActivatedAt: time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond), ExpiresAt: expires,
				ActorID: "operator", ReasonDigest: reason[:], CorrelationID: identity.New("cor"),
			}
			if err := repository.ChangeProviderCredentialGrant(ctx, grant); err != nil {
				t.Fatal(err)
			}
			use := persistence.ProviderCredentialUse{
				Contract:          persistence.ProviderCredentialAuthorizationContract,
				IsolationDomainID: scope, RevisionID: revision, OperationID: identity.New("op"),
				ProviderProfile: "codex", Purpose: persistence.ProviderCredentialPurposeAgentInference,
				Phase: persistence.ProviderCredentialPhaseEffect, ActorID: "operator", CorrelationID: identity.New("cor"),
			}
			if mode == "commit-admission" {
				use.Phase = persistence.ProviderCredentialPhaseAdmission
			}
			transaction, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer transaction.Rollback(context.Background())
			// An administrative SQL writer still uses the sequence trigger. Keep
			// its revocation uncommitted so readers must wait on that exact lock.
			if _, err := transaction.Exec(ctx, `
				INSERT INTO provider_credential_grant_events (
				 contract, isolation_domain_id, revision_id, provider_profile, purpose,
				 generation, operation, actor_id, reason_digest, correlation_id
				) VALUES ($1, $2, $3, 'codex', 'agent-inference', 2, 'revoke', 'operator', $4, $5)
			`, grant.Contract, scope, revision, reason[:], identity.New("cor")); err != nil {
				t.Fatal(err)
			}
			authorizationCtx, stopAuthorization := context.WithCancel(ctx)
			defer stopAuthorization()
			result := make(chan error, 1)
			go func() { result <- repository.AuthorizeProviderCredentialUse(authorizationCtx, use) }()
			key := "provider-credential-grant\n" + scope + "\n" + revision + "\ncodex\nagent-inference"
			waitCtx, stopWait := context.WithTimeout(ctx, 5*time.Second)
			defer stopWait()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if err := pool.QueryRow(waitCtx, `
					SELECT EXISTS (
					 SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted
					 AND classid::bigint = ((hashtextextended($1, 0) >> 32) & 4294967295)
					 AND objid::bigint = (hashtextextended($1, 0) & 4294967295) AND objsubid = 1
					)
				`, key).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-result:
					t.Fatalf("authorization escaped the database grant lock: %v", err)
				case <-waitCtx.Done():
					t.Fatal("authorization did not wait on the database grant lock")
				case <-ticker.C:
				}
			}
			// The same identifiers in a different isolation domain must not wait.
			foreign := use
			foreign.IsolationDomainID = identity.New("iso")
			foreignCtx, stopForeign := context.WithTimeout(ctx, time.Second)
			err = repository.AuthorizeProviderCredentialUse(foreignCtx, foreign)
			stopForeign()
			if !errors.Is(err, persistence.ErrProviderCredentialUnauthorized) {
				t.Fatalf("unrelated isolation domain was blocked: %v", err)
			}
			if mode == "cancel" {
				stopAuthorization()
				select {
				case err := <-result:
					if err == nil || errors.Is(err, persistence.ErrProviderCredentialUnauthorized) {
						t.Fatalf("cancelled lock wait became a completed decision: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("cancelled authorization did not stop")
				}
			}
			if mode == "expiry" {
				// Expiry must be checked after acquisition, with PostgreSQL time.
				if _, err := transaction.Exec(ctx, `SELECT pg_sleep(GREATEST(0, EXTRACT(EPOCH FROM ($1::timestamptz - clock_timestamp()))) + 0.05)`, expires); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "commit-admission" || mode == "commit-effect" {
				err = transaction.Commit(ctx)
			} else {
				err = transaction.Rollback(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode != "cancel" {
				select {
				case err := <-result:
					if mode == "rollback" && err != nil {
						t.Fatalf("rolled back revocation denied the current grant: %v", err)
					}
					if mode != "rollback" && !errors.Is(err, persistence.ErrProviderCredentialUnauthorized) {
						t.Fatalf("revoked or expired grant was authorized: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("authorization did not resume")
				}
			}
			var decisions, matching int
			outcome, generation := "denied", 2
			if mode == "rollback" {
				outcome, generation = "allowed", 1
			} else if mode == "expiry" {
				generation = 1
			}
			if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE outcome=$3 AND grant_generation=$4)
				FROM provider_credential_authorization_decisions WHERE isolation_domain_id=$1 AND correlation_id=$2`, scope, use.CorrelationID, outcome, generation).Scan(&decisions, &matching); err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" {
				if decisions != 0 {
					t.Fatal("cancelled wait committed a decision")
				}
			} else if decisions != 1 || matching != 1 {
				t.Fatalf("wrong decision evidence: total=%d matching=%d", decisions, matching)
			}
		})
	}
}
