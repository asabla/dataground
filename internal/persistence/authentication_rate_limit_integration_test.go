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
		if _, err := pool.Exec(ctx, `TRUNCATE authentication_rate_limit_buckets, authentication_rate_limit_policy_activations`); err != nil {
			t.Fatalf("reset rate limit buckets: %v", err)
		}
	}
	activate := func(t *testing.T, generation uint64, policy persistence.AuthenticationRateLimitPolicy) {
		t.Helper()
		reasonDigest := sha256.Sum256([]byte("reviewed test policy"))
		err := repository.ActivateAuthenticationRateLimitPolicy(
			ctx,
			persistence.AuthenticationRateLimitPolicyActivation{
				Contract:      "dataground.authentication-rate-limit-policy/v1",
				Generation:    generation,
				Policy:        policy,
				ActivatedBy:   "test-operator",
				CorrelationID: "cor_0123456789abcdefghij",
				ReasonDigest:  append([]byte(nil), reasonDigest[:]...),
			},
		)
		if err != nil {
			t.Fatalf("activate authentication rate limit policy: %v", err)
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
		activate(t, 1, policy)
		for attempt := 0; attempt < 2; attempt++ {
			result, err := repository.AllowAuthentication(ctx, domain, credential, 1, policy)
			if err != nil || !result.Allowed {
				t.Fatalf("credential admission %d = %#v, %v", attempt, result, err)
			}
		}
		result, err := repository.AllowAuthentication(ctx, domain, credential, 1, policy)
		if err != nil || result.Allowed || result.RetryAfter <= 0 || result.RetryAfter > time.Hour {
			t.Fatalf("credential denial = %#v, %v", result, err)
		}
		conflicting := policy
		conflicting.GlobalBurst++
		if _, err := repository.AllowAuthentication(
			ctx, domain, credential, 1, conflicting,
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
		activate(t, 1, policy)
		for _, digest := range [][sha256.Size]byte{credential, otherCredential} {
			result, err := repository.AllowAuthentication(ctx, domain, digest, 1, policy)
			if err != nil || !result.Allowed {
				t.Fatalf("domain admission = %#v, %v", result, err)
			}
		}
		thirdCredential := sha256.Sum256([]byte("credential-three"))
		result, err := repository.AllowAuthentication(ctx, domain, thirdCredential, 1, policy)
		if err != nil || result.Allowed || result.RetryAfter <= 0 {
			t.Fatalf("domain denial = %#v, %v", result, err)
		}
	})

	t.Run("global concurrency", func(t *testing.T) {
		reset(t)
		policy := persistence.AuthenticationRateLimitPolicy{
			Window: time.Hour, GlobalBurst: 10, IsolationDomainBurst: 10, CredentialBurst: 10,
		}
		activate(t, 1, policy)
		var wait sync.WaitGroup
		results := make(chan persistence.AuthenticationRateLimitResult, 20)
		errors := make(chan error, 20)
		for index := 0; index < 20; index++ {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				candidateDomain := identity.New("iso")
				candidateCredential := sha256.Sum256([]byte{byte(index + 1)})
				result, err := repository.AllowAuthentication(ctx, candidateDomain, candidateCredential, 1, policy)
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
		activate(t, 1, policy)
		result, err := repository.AllowAuthentication(ctx, domain, credential, 1, policy)
		if err != nil || !result.Allowed {
			t.Fatalf("seed admission = %#v, %v", result, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO authentication_rate_limit_buckets (
				scope, subject_digest, policy_generation, policy_digest, theoretical_arrival_at, updated_at
			)
			SELECT
				'credential',
				decode(lpad(to_hex(value), 64, '0'), 'hex'),
				1,
				decode(repeat('01', 32), 'hex'),
				clock_timestamp() - interval '2 hours',
				clock_timestamp() - interval '2 hours'
			FROM generate_series(1, 140) AS series(value)
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
		result, err = repository.AllowAuthentication(ctx, newDomain, newCredential, 1, policy)
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

	t.Run("generation cutover", func(t *testing.T) {
		reset(t)
		first := persistence.AuthenticationRateLimitPolicy{
			Window: time.Hour, GlobalBurst: 2, IsolationDomainBurst: 2, CredentialBurst: 2,
		}
		activate(t, 1, first)
		activate(t, 1, first)
		gapReason := sha256.Sum256([]byte("invalid generation gap"))
		if err := repository.ActivateAuthenticationRateLimitPolicy(
			ctx,
			persistence.AuthenticationRateLimitPolicyActivation{
				Contract:      "dataground.authentication-rate-limit-policy/v1",
				Generation:    3,
				Policy:        first,
				ActivatedBy:   "test-operator",
				CorrelationID: "cor_0123456789abcdefghil",
				ReasonDigest:  append([]byte(nil), gapReason[:]...),
			},
		); !errors.Is(err, persistence.ErrAuthenticationRateLimitConflict) {
			t.Fatalf("generation gap error = %v", err)
		}
		if result, err := repository.AllowAuthentication(ctx, domain, credential, 1, first); err != nil || !result.Allowed {
			t.Fatalf("first-generation admission = %#v, %v", result, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO authentication_rate_limit_buckets (
				scope,
				subject_digest,
				policy_generation,
				policy_digest,
				theoretical_arrival_at,
				updated_at
			)
			SELECT
				'credential',
				decode(lpad(to_hex(value + 1000), 64, '0'), 'hex'),
				1,
				(
					SELECT policy_digest
					FROM authentication_rate_limit_buckets
					WHERE policy_generation = 1
					LIMIT 1
				),
				clock_timestamp() - interval '2 hours',
				clock_timestamp() - interval '2 hours'
			FROM generate_series(1, 140) AS series(value)
		`); err != nil {
			t.Fatalf("seed retired-generation buckets: %v", err)
		}
		second := persistence.AuthenticationRateLimitPolicy{
			Window: time.Minute, GlobalBurst: 20, IsolationDomainBurst: 10, CredentialBurst: 5,
		}
		secondReason := sha256.Sum256([]byte("reviewed replacement policy"))
		if err := repository.ActivateAuthenticationRateLimitPolicy(
			ctx,
			persistence.AuthenticationRateLimitPolicyActivation{
				Contract:      "dataground.authentication-rate-limit-policy/v1",
				Generation:    2,
				Policy:        second,
				ActivatedBy:   "test-operator",
				CorrelationID: "cor_0123456789abcdefghik",
				ReasonDigest:  append([]byte(nil), secondReason[:]...),
			},
		); err != nil {
			t.Fatalf("activate replacement policy: %v", err)
		}
		if _, err := repository.AllowAuthentication(
			ctx, domain, credential, 1, first,
		); !errors.Is(err, persistence.ErrAuthenticationRateLimitConflict) {
			t.Fatalf("stale policy error = %v", err)
		}
		correlationReuseReason := sha256.Sum256([]byte("conflicting correlation reuse"))
		if err := repository.ActivateAuthenticationRateLimitPolicy(
			ctx,
			persistence.AuthenticationRateLimitPolicyActivation{
				Contract:      "dataground.authentication-rate-limit-policy/v1",
				Generation:    3,
				Policy:        second,
				ActivatedBy:   "test-operator",
				CorrelationID: "cor_0123456789abcdefghik",
				ReasonDigest:  append([]byte(nil), correlationReuseReason[:]...),
			},
		); !errors.Is(err, persistence.ErrAuthenticationRateLimitConflict) {
			t.Fatalf("correlation reuse error = %v", err)
		}
		if result, err := repository.AllowAuthentication(
			ctx, domain, credential, 2, second,
		); err != nil || !result.Allowed {
			t.Fatalf("replacement policy admission = %#v, %v", result, err)
		}
		rows, err := pool.Query(ctx, `
			SELECT policy_generation, count(*)
			FROM authentication_rate_limit_buckets
			GROUP BY policy_generation
			ORDER BY policy_generation
		`)
		if err != nil {
			t.Fatalf("inspect bucket generations: %v", err)
		}
		defer rows.Close()
		counts := make(map[int64]int)
		for rows.Next() {
			var generation int64
			var count int
			if err := rows.Scan(&generation, &count); err != nil {
				t.Fatal(err)
			}
			counts[generation] = count
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if counts[1] != 15 || counts[2] != 3 || len(counts) != 2 {
			t.Fatalf("bucket generation counts = %v, want map[1:15 2:3]", counts)
		}
	})

	t.Run("capacity evidence", func(t *testing.T) {
		reset(t)
		var databaseName string
		if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
			t.Fatalf("read capacity database name: %v", err)
		}
		config := persistence.AuthenticationRateLimitCapacityConfig{
			Contract:          "dataground.authentication-rate-limit-capacity/v2",
			RunID:             "cap_0123456789abcdefghij",
			SourceRevision:    "0123456789abcdef0123456789abcdef01234567",
			DeploymentProfile: "developer",
			DatabaseName:      databaseName,
			Policy: persistence.AuthenticationRateLimitPolicy{
				Window: time.Hour, GlobalBurst: 8, IsolationDomainBurst: 6, CredentialBurst: 4,
			},
			AttemptsPerPhase:  12,
			Workers:           4,
			MaximumP99Latency: time.Minute,
			MinimumThroughput: 1,
		}
		evidence, err := repository.MeasureAuthenticationRateLimitCapacity(ctx, config)
		if err != nil {
			t.Fatalf("measure authentication rate limit capacity: %v", err)
		}
		if evidence.Contract != "dataground.authentication-rate-limit-capacity-evidence/v2" ||
			evidence.RunID != config.RunID || evidence.SourceRevision != config.SourceRevision ||
			evidence.DatabaseName != config.DatabaseName ||
			!evidence.Accepted || evidence.GoVersion == "" || evidence.PostgreSQLServerVersion <= 0 ||
			evidence.PostgreSQLMaxConnections <= 0 ||
			len(evidence.Phases) != 3 || evidence.RecordedAt == "" {
			t.Fatalf("capacity evidence = %#v", evidence)
		}
		expectedAllowed := []uint32{4, 6, 8}
		for index, phase := range evidence.Phases {
			if phase.Allowed != expectedAllowed[index] || phase.Denied != 12-expectedAllowed[index] ||
				!phase.P99LatencyAccepted || !phase.MinimumThroughputAccepted {
				t.Fatalf("capacity phase %d = %#v", index, phase)
			}
		}
		if !evidence.AcceptedFor(
			config.SourceRevision,
			config.DeploymentProfile,
			evidence.GoVersion,
			config.Policy,
		) {
			t.Fatal("capacity evidence did not validate for its exact profile")
		}
		if err := repository.AuthenticationRateLimitCapacityReady(ctx, evidence); err != nil {
			t.Fatalf("capacity serving profile readiness: %v", err)
		}
		if err := repository.AuthenticationRateLimitPolicyAndCapacityReady(
			ctx,
			3,
			config.Policy,
			evidence,
		); err != nil {
			t.Fatalf("capacity-bound policy readiness: %v", err)
		}
		mismatched := evidence
		mismatched.PostgreSQLMaxConnections++
		if err := repository.AuthenticationRateLimitCapacityReady(ctx, mismatched); !errors.Is(err, persistence.ErrAuthenticationRateLimitCapacityInvalid) {
			t.Fatalf("mismatched capacity serving profile error = %v", err)
		}
		if _, err := repository.MeasureAuthenticationRateLimitCapacity(ctx, config); !errors.Is(err, persistence.ErrAuthenticationRateLimitCapacityInvalid) {
			t.Fatalf("capacity database reuse error = %v", err)
		}
	})

}
