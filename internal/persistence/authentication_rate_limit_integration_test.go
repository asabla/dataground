package persistence_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestAuthenticationRateLimitsCoordinateLayeredAdmission(t *testing.T) {
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

	reset := func(t *testing.T) {
		t.Helper()
		if _, err := pool.Exec(ctx, `TRUNCATE authentication_rate_limit_buckets`); err != nil {
			t.Fatalf("reset rate limit buckets: %v", err)
		}
	}
	credential := sha256.Sum256([]byte("credential-one"))
	otherCredential := sha256.Sum256([]byte("credential-two"))
	domain := identity.New("iso")

	t.Run("credential", func(t *testing.T) {
		reset(t)
		policy := persistence.AuthenticationRateLimitPolicy{
			Window: time.Hour, GlobalBurst: 10, IsolationDomainBurst: 10, CredentialBurst: 2,
		}
		for attempt := 0; attempt < 2; attempt++ {
			result, err := repository.AllowAuthentication(ctx, domain, credential, policy)
			if err != nil || !result.Allowed {
				t.Fatalf("credential admission %d = %#v, %v", attempt, result, err)
			}
		}
		result, err := repository.AllowAuthentication(ctx, domain, credential, policy)
		if err != nil || result.Allowed || result.RetryAfter <= 0 || result.RetryAfter > time.Hour {
			t.Fatalf("credential denial = %#v, %v", result, err)
		}
		conflicting := policy
		conflicting.GlobalBurst++
		if _, err := repository.AllowAuthentication(
			ctx, domain, credential, conflicting,
		); !errors.Is(err, persistence.ErrAuthenticationRateLimitConflict) {
			t.Fatalf("conflicting policy error = %v", err)
		}
		var bucketCount, rawCredentialCount int
		if err := pool.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE subject_digest = $1)
			FROM authentication_rate_limit_buckets
		`, credential[:]).Scan(&bucketCount, &rawCredentialCount); err != nil {
			t.Fatalf("inspect minimized bucket subjects: %v", err)
		}
		if bucketCount != 3 || rawCredentialCount != 0 {
			t.Fatalf("stored admission buckets = %d, raw credentials = %d", bucketCount, rawCredentialCount)
		}
	})

	t.Run("isolation domain", func(t *testing.T) {
		reset(t)
		policy := persistence.AuthenticationRateLimitPolicy{
			Window: time.Hour, GlobalBurst: 10, IsolationDomainBurst: 2, CredentialBurst: 2,
		}
		for _, digest := range [][sha256.Size]byte{credential, otherCredential} {
			result, err := repository.AllowAuthentication(ctx, domain, digest, policy)
			if err != nil || !result.Allowed {
				t.Fatalf("domain admission = %#v, %v", result, err)
			}
		}
		thirdCredential := sha256.Sum256([]byte("credential-three"))
		result, err := repository.AllowAuthentication(ctx, domain, thirdCredential, policy)
		if err != nil || result.Allowed || result.RetryAfter <= 0 {
			t.Fatalf("domain denial = %#v, %v", result, err)
		}
	})

	t.Run("global concurrency", func(t *testing.T) {
		reset(t)
		policy := persistence.AuthenticationRateLimitPolicy{
			Window: time.Hour, GlobalBurst: 10, IsolationDomainBurst: 10, CredentialBurst: 10,
		}
		var wait sync.WaitGroup
		results := make(chan persistence.AuthenticationRateLimitResult, 20)
		errors := make(chan error, 20)
		for index := 0; index < 20; index++ {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				candidateDomain := identity.New("iso")
				candidateCredential := sha256.Sum256([]byte{byte(index + 1)})
				result, err := repository.AllowAuthentication(ctx, candidateDomain, candidateCredential, policy)
				results <- result
				errors <- err
			}(index)
		}
		wait.Wait()
		close(results)
		close(errors)
		for err := range errors {
			if err != nil {
				t.Fatalf("concurrent admission: %v", err)
			}
		}
		allowed := 0
		for result := range results {
			if result.Allowed {
				allowed++
			} else if result.RetryAfter <= 0 {
				t.Fatalf("denial omitted retry delay: %#v", result)
			}
		}
		if allowed != 10 {
			t.Fatalf("concurrent allowed admissions = %d, want 10", allowed)
		}
	})

	t.Run("bounded reclamation", func(t *testing.T) {
		reset(t)
		policy := persistence.AuthenticationRateLimitPolicy{
			Window: time.Hour, GlobalBurst: 1000, IsolationDomainBurst: 100, CredentialBurst: 10,
		}
		result, err := repository.AllowAuthentication(ctx, domain, credential, policy)
		if err != nil || !result.Allowed {
			t.Fatalf("seed admission = %#v, %v", result, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO authentication_rate_limit_buckets (
				scope, subject_digest, policy_digest, theoretical_arrival_at, updated_at
			)
			SELECT
				'credential',
				decode(lpad(to_hex(value), 64, '0'), 'hex'),
				decode(repeat('01', 32), 'hex'),
				clock_timestamp() - interval '2 hours',
				clock_timestamp() - interval '2 hours'
			FROM generate_series(1, 140) AS value
		`); err != nil {
			t.Fatalf("seed stale buckets: %v", err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE authentication_rate_limit_buckets
			SET theoretical_arrival_at = clock_timestamp() - interval '2 hours',
			    updated_at = clock_timestamp() - interval '2 hours'
			WHERE scope <> 'global'
		`); err != nil {
			t.Fatalf("age admission buckets: %v", err)
		}
		newDomain := identity.New("iso")
		newCredential := sha256.Sum256([]byte("reclamation-credential"))
		result, err = repository.AllowAuthentication(ctx, newDomain, newCredential, policy)
		if err != nil || !result.Allowed {
			t.Fatalf("admission with reclamation = %#v, %v", result, err)
		}
		var stale int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM authentication_rate_limit_buckets
			WHERE scope <> 'global'
			  AND updated_at <= clock_timestamp() - interval '1 hour'
		`).Scan(&stale); err != nil {
			t.Fatalf("count stale buckets: %v", err)
		}
		if stale != 14 {
			t.Fatalf("stale buckets after one cleanup = %d, want 14", stale)
		}
	})

}
