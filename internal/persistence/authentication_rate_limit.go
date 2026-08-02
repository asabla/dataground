package persistence

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	minimumAuthenticationRateLimitWindow = time.Second
	maximumAuthenticationRateLimitWindow = 24 * time.Hour
	maximumAuthenticationRateLimitBurst  = 1_000_000
	authenticationRateLimitCleanupBatch  = 128
)

var (
	ErrAuthenticationRateLimitInvalid    = errors.New("authentication rate limit request is invalid")
	ErrAuthenticationRateLimitConflict   = errors.New("authentication rate limit policy conflicts with active coordination")
	authenticationRateLimitDomainPattern = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	authenticationRateLimitGlobalDigest  = sha256.Sum256([]byte("dataground:authentication-rate-limit:global:v1"))
)

type AuthenticationRateLimitPolicy struct {
	Window               time.Duration
	GlobalBurst          uint32
	IsolationDomainBurst uint32
	CredentialBurst      uint32
}

func (policy AuthenticationRateLimitPolicy) Valid() bool {
	return policy.Window >= minimumAuthenticationRateLimitWindow &&
		policy.Window <= maximumAuthenticationRateLimitWindow &&
		policy.GlobalBurst >= 1 && policy.GlobalBurst <= maximumAuthenticationRateLimitBurst &&
		policy.IsolationDomainBurst >= 1 && policy.IsolationDomainBurst <= policy.GlobalBurst &&
		policy.CredentialBurst >= 1 && policy.CredentialBurst <= policy.IsolationDomainBurst
}

type AuthenticationRateLimitResult struct {
	Allowed    bool
	RetryAfter time.Duration
}

type authenticationRateLimitAdvance struct {
	result      AuthenticationRateLimitResult
	nextArrival time.Time
}

func (repository *Repository) AllowAuthentication(
	ctx context.Context,
	isolationDomainID string,
	credentialDigest [sha256.Size]byte,
	generation uint64,
	policy AuthenticationRateLimitPolicy,
) (AuthenticationRateLimitResult, error) {
	if repository == nil || repository.pool == nil || ctx == nil ||
		!authenticationRateLimitDomainPattern.MatchString(isolationDomainID) ||
		credentialDigest == [sha256.Size]byte{} ||
		generation == 0 || generation > math.MaxInt64 || !policy.Valid() {
		return AuthenticationRateLimitResult{}, ErrAuthenticationRateLimitInvalid
	}
	if err := ctx.Err(); err != nil {
		return AuthenticationRateLimitResult{}, err
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AuthenticationRateLimitResult{}, fmt.Errorf("begin authentication admission: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`,
		authenticationRateLimitCoordinationLockKey); err != nil {
		return AuthenticationRateLimitResult{}, fmt.Errorf("lock authentication admission policy: %w", err)
	}
	if err := requireActiveAuthenticationRateLimitPolicy(ctx, tx, generation, policy, nil); err != nil {
		return AuthenticationRateLimitResult{}, err
	}
	policyDigest := policy.digest()

	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return AuthenticationRateLimitResult{}, fmt.Errorf("read authentication admission clock: %w", err)
	}
	globalArrival, err := lockAuthenticationRateLimitBucket(
		ctx, tx, "global", authenticationRateLimitGlobalDigest, generation, policyDigest, now,
	)
	if err != nil {
		return AuthenticationRateLimitResult{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return AuthenticationRateLimitResult{}, fmt.Errorf("read locked authentication admission clock: %w", err)
	}
	global := advanceAuthenticationRateLimitBucket(now, globalArrival, policy.Window, policy.GlobalBurst)
	if err := storeAuthenticationRateLimitBucket(
		ctx, tx, "global", authenticationRateLimitGlobalDigest, generation, global, now,
	); err != nil {
		return AuthenticationRateLimitResult{}, err
	}
	if !global.result.Allowed {
		return commitAuthenticationRateLimitResult(ctx, tx, global.result)
	}
	if err := reclaimAuthenticationRateLimitBuckets(ctx, tx, generation, now.Add(-policy.Window)); err != nil {
		return AuthenticationRateLimitResult{}, err
	}

	domainDigest := authenticationRateLimitDigest("domain", []byte(isolationDomainID))
	domain, err := consumeAuthenticationRateLimitBucket(
		ctx, tx, "domain", domainDigest, generation, policyDigest, now, policy.Window, policy.IsolationDomainBurst,
	)
	if err != nil {
		return AuthenticationRateLimitResult{}, err
	}
	if !domain.Allowed {
		return commitAuthenticationRateLimitResult(ctx, tx, domain)
	}
	credentialKey := make([]byte, 0, len(isolationDomainID)+1+sha256.Size)
	credentialKey = append(credentialKey, isolationDomainID...)
	credentialKey = append(credentialKey, 0)
	credentialKey = append(credentialKey, credentialDigest[:]...)
	credentialKeyDigest := authenticationRateLimitDigest("credential", credentialKey)
	clear(credentialKey)
	credential, err := consumeAuthenticationRateLimitBucket(
		ctx, tx, "credential", credentialKeyDigest, generation, policyDigest, now, policy.Window, policy.CredentialBurst,
	)
	if err != nil {
		return AuthenticationRateLimitResult{}, err
	}
	return commitAuthenticationRateLimitResult(ctx, tx, credential)
}

func consumeAuthenticationRateLimitBucket(
	ctx context.Context,
	tx pgx.Tx,
	scope string,
	digest [sha256.Size]byte,
	generation uint64,
	policyDigest [sha256.Size]byte,
	now time.Time,
	window time.Duration,
	burst uint32,
) (AuthenticationRateLimitResult, error) {
	arrival, err := lockAuthenticationRateLimitBucket(ctx, tx, scope, digest, generation, policyDigest, now)
	if err != nil {
		return AuthenticationRateLimitResult{}, err
	}
	advance := advanceAuthenticationRateLimitBucket(now, arrival, window, burst)
	if err := storeAuthenticationRateLimitBucket(ctx, tx, scope, digest, generation, advance, now); err != nil {
		return AuthenticationRateLimitResult{}, err
	}
	return advance.result, nil
}

func lockAuthenticationRateLimitBucket(
	ctx context.Context,
	tx pgx.Tx,
	scope string,
	digest [sha256.Size]byte,
	generation uint64,
	policyDigest [sha256.Size]byte,
	now time.Time,
) (time.Time, error) {
	_, err := tx.Exec(ctx, `
		INSERT INTO authentication_rate_limit_buckets (
			scope, subject_digest, policy_generation, policy_digest, theoretical_arrival_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (policy_generation, scope, subject_digest) DO NOTHING
	`, scope, digest[:], generation, policyDigest[:], now)
	if err != nil {
		return time.Time{}, fmt.Errorf("initialize authentication rate limit bucket: %w", err)
	}
	var arrival time.Time
	var storedGeneration uint64
	var storedPolicyDigest []byte
	if err := tx.QueryRow(ctx, `
		SELECT policy_generation, policy_digest, theoretical_arrival_at
		FROM authentication_rate_limit_buckets
		WHERE policy_generation = $3
		  AND scope = $1
		  AND subject_digest = $2
		FOR UPDATE
	`, scope, digest[:], generation).Scan(&storedGeneration, &storedPolicyDigest, &arrival); err != nil {
		return time.Time{}, fmt.Errorf("lock authentication rate limit bucket: %w", err)
	}
	if storedGeneration != generation || len(storedPolicyDigest) != sha256.Size ||
		subtle.ConstantTimeCompare(storedPolicyDigest, policyDigest[:]) != 1 {
		return time.Time{}, ErrAuthenticationRateLimitConflict
	}
	return arrival.UTC(), nil
}

func storeAuthenticationRateLimitBucket(
	ctx context.Context,
	tx pgx.Tx,
	scope string,
	digest [sha256.Size]byte,
	generation uint64,
	advance authenticationRateLimitAdvance,
	now time.Time,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE authentication_rate_limit_buckets
		SET theoretical_arrival_at = $4, updated_at = $5
		WHERE policy_generation = $3
		  AND scope = $1
		  AND subject_digest = $2
	`, scope, digest[:], generation, advance.nextArrival, now)
	if err != nil {
		return fmt.Errorf("update authentication rate limit bucket: %w", err)
	}
	return nil
}

func advanceAuthenticationRateLimitBucket(
	now time.Time,
	theoreticalArrival time.Time,
	window time.Duration,
	burst uint32,
) authenticationRateLimitAdvance {
	interval := window / time.Duration(burst)
	allowedAt := theoreticalArrival.Add(-interval * time.Duration(burst-1))
	if now.Before(allowedAt) {
		return authenticationRateLimitAdvance{
			result:      AuthenticationRateLimitResult{RetryAfter: allowedAt.Sub(now)},
			nextArrival: theoreticalArrival,
		}
	}
	if theoreticalArrival.Before(now) {
		theoreticalArrival = now
	}
	return authenticationRateLimitAdvance{
		result:      AuthenticationRateLimitResult{Allowed: true},
		nextArrival: theoreticalArrival.Add(interval),
	}
}

func reclaimAuthenticationRateLimitBuckets(
	ctx context.Context,
	tx pgx.Tx,
	activeGeneration uint64,
	staleBefore time.Time,
) error {
	_, err := tx.Exec(ctx, `
		WITH stale AS (
			SELECT policy_generation, scope, subject_digest
			FROM authentication_rate_limit_buckets
			WHERE policy_generation <> $1
			   OR (
					policy_generation = $1
					AND scope <> 'global'
					AND updated_at <= $2
			   )
			ORDER BY policy_generation, updated_at, scope, subject_digest
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM authentication_rate_limit_buckets AS bucket
		USING stale
		WHERE bucket.policy_generation = stale.policy_generation
		  AND bucket.scope = stale.scope
		  AND bucket.subject_digest = stale.subject_digest
	`, activeGeneration, staleBefore, authenticationRateLimitCleanupBatch)
	if err != nil {
		return fmt.Errorf("reclaim authentication rate limit buckets: %w", err)
	}
	return nil
}

func commitAuthenticationRateLimitResult(
	ctx context.Context,
	tx pgx.Tx,
	result AuthenticationRateLimitResult,
) (AuthenticationRateLimitResult, error) {
	if err := tx.Commit(ctx); err != nil {
		return AuthenticationRateLimitResult{}, fmt.Errorf("commit authentication admission: %w", err)
	}
	return result, nil
}

func authenticationRateLimitDigest(scope string, value []byte) [sha256.Size]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte("dataground:authentication-rate-limit:" + scope + ":v1\x00"))
	_, _ = hash.Write(value)
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func (policy AuthenticationRateLimitPolicy) digest() [sha256.Size]byte {
	var encoded [8 + 4 + 4 + 4]byte
	binary.BigEndian.PutUint64(encoded[0:8], uint64(policy.Window))
	binary.BigEndian.PutUint32(encoded[8:12], policy.GlobalBurst)
	binary.BigEndian.PutUint32(encoded[12:16], policy.IsolationDomainBurst)
	binary.BigEndian.PutUint32(encoded[16:20], policy.CredentialBurst)
	return authenticationRateLimitDigest("policy", encoded[:])
}
