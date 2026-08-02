package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	authenticationRateLimitPolicyContract      = "dataground.authentication-rate-limit-policy/v1"
	authenticationRateLimitCoordinationLockKey = int64(0x444741555448524c)
)

var (
	ErrAuthenticationRateLimitPolicyActivationInvalid = errors.New("authentication rate limit policy activation is invalid")
	ErrAuthenticationRateLimitPolicyInactive          = errors.New("authentication rate limit policy is not active")
	authenticationRateLimitActorPattern               = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,127}$`)
	authenticationRateLimitCorrelationPattern         = regexp.MustCompile(`^cor_[0-9a-z]{20,32}$`)
)

// AuthenticationRateLimitPolicyActivation is one immutable operator-attributed
// generation. Activating the next generation atomically replaces limiter state;
// older processes then fail closed because their configured generation is stale.
type AuthenticationRateLimitPolicyActivation struct {
	Contract      string
	Generation    uint64
	Policy        AuthenticationRateLimitPolicy
	ActivatedBy   string
	CorrelationID string
	ReasonDigest  []byte
}

func (activation AuthenticationRateLimitPolicyActivation) Valid() bool {
	return activation.Contract == authenticationRateLimitPolicyContract &&
		activation.Generation > 0 && activation.Generation <= math.MaxInt64 &&
		activation.Policy.Valid() &&
		authenticationRateLimitActorPattern.MatchString(activation.ActivatedBy) &&
		authenticationRateLimitCorrelationPattern.MatchString(activation.CorrelationID) &&
		len(activation.ReasonDigest) == sha256.Size
}

// ActivateAuthenticationRateLimitPolicy installs exactly the next generation.
// Exact replay of the active generation is read-only. Every other reuse,
// rollback, or gap fails closed.
func (repository *Repository) ActivateAuthenticationRateLimitPolicy(
	ctx context.Context,
	activation AuthenticationRateLimitPolicyActivation,
) error {
	if repository == nil || repository.pool == nil || ctx == nil || !activation.Valid() {
		return ErrAuthenticationRateLimitPolicyActivationInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	activation = cloneAuthenticationRateLimitPolicyActivation(activation)
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin authentication rate limit policy activation: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`,
		authenticationRateLimitCoordinationLockKey); err != nil {
		return fmt.Errorf("lock authentication rate limit policy activation: %w", err)
	}

	var currentGeneration uint64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(generation), 0)
		FROM authentication_rate_limit_policy_activations
	`).Scan(&currentGeneration); err != nil {
		return fmt.Errorf("read active authentication rate limit policy generation: %w", err)
	}
	var correlatedGeneration uint64
	correlationErr := tx.QueryRow(ctx, `
		SELECT generation
		FROM authentication_rate_limit_policy_activations
		WHERE activation_correlation_id = $1
	`, activation.CorrelationID).Scan(&correlatedGeneration)
	if correlationErr == nil && correlatedGeneration != activation.Generation {
		return ErrAuthenticationRateLimitConflict
	}
	if correlationErr != nil && !errors.Is(correlationErr, pgx.ErrNoRows) {
		return fmt.Errorf("read authentication rate limit policy correlation: %w", correlationErr)
	}
	if activation.Generation <= currentGeneration {
		existing, err := getAuthenticationRateLimitPolicyActivation(ctx, tx, activation.Generation)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAuthenticationRateLimitConflict
			}
			return err
		}
		if activation.Generation != currentGeneration ||
			!sameAuthenticationRateLimitPolicyActivation(existing, activation) {
			return ErrAuthenticationRateLimitConflict
		}
		return tx.Commit(ctx)
	}
	if activation.Generation != currentGeneration+1 {
		return ErrAuthenticationRateLimitConflict
	}

	digest := activation.Policy.digest()
	if _, err := tx.Exec(ctx, `
		INSERT INTO authentication_rate_limit_policy_activations (
			generation,
			contract,
			policy_digest,
			window_nanoseconds,
			global_burst,
			isolation_domain_burst,
			credential_burst,
			activated_by,
			activation_correlation_id,
			reason_digest
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		activation.Generation,
		activation.Contract,
		digest[:],
		activation.Policy.Window.Nanoseconds(),
		activation.Policy.GlobalBurst,
		activation.Policy.IsolationDomainBurst,
		activation.Policy.CredentialBurst,
		activation.ActivatedBy,
		activation.CorrelationID,
		activation.ReasonDigest,
	); err != nil {
		return fmt.Errorf("record authentication rate limit policy activation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit authentication rate limit policy activation: %w", err)
	}
	return nil
}

// AuthenticationRateLimitPolicyReady proves that the exact configured
// generation and values are the latest operator-activated policy.
func (repository *Repository) AuthenticationRateLimitPolicyReady(
	ctx context.Context,
	generation uint64,
	policy AuthenticationRateLimitPolicy,
) error {
	if repository == nil || repository.pool == nil || ctx == nil ||
		generation == 0 || generation > math.MaxInt64 || !policy.Valid() {
		return ErrAuthenticationRateLimitPolicyActivationInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return requireActiveAuthenticationRateLimitPolicy(ctx, repository.pool, generation, policy)
}

type authenticationRateLimitPolicyQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func requireActiveAuthenticationRateLimitPolicy(
	ctx context.Context,
	querier authenticationRateLimitPolicyQuerier,
	generation uint64,
	policy AuthenticationRateLimitPolicy,
) error {
	var (
		activeGeneration  uint64
		policyDigest      []byte
		windowNanoseconds int64
		globalBurst       uint32
		domainBurst       uint32
		credentialBurst   uint32
	)
	err := querier.QueryRow(ctx, `
		SELECT
			generation,
			policy_digest,
			window_nanoseconds,
			global_burst,
			isolation_domain_burst,
			credential_burst
		FROM authentication_rate_limit_policy_activations
		ORDER BY generation DESC
		LIMIT 1
	`).Scan(
		&activeGeneration,
		&policyDigest,
		&windowNanoseconds,
		&globalBurst,
		&domainBurst,
		&credentialBurst,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAuthenticationRateLimitPolicyInactive
	}
	if err != nil {
		return fmt.Errorf("read active authentication rate limit policy: %w", err)
	}
	expectedDigest := policy.digest()
	if activeGeneration != generation ||
		len(policyDigest) != sha256.Size ||
		subtle.ConstantTimeCompare(policyDigest, expectedDigest[:]) != 1 ||
		windowNanoseconds != policy.Window.Nanoseconds() ||
		globalBurst != policy.GlobalBurst ||
		domainBurst != policy.IsolationDomainBurst ||
		credentialBurst != policy.CredentialBurst {
		return ErrAuthenticationRateLimitConflict
	}
	return nil
}

func getAuthenticationRateLimitPolicyActivation(
	ctx context.Context,
	querier authenticationRateLimitPolicyQuerier,
	generation uint64,
) (AuthenticationRateLimitPolicyActivation, error) {
	var (
		activation        AuthenticationRateLimitPolicyActivation
		policyDigest      []byte
		windowNanoseconds int64
	)
	err := querier.QueryRow(ctx, `
		SELECT
			contract,
			generation,
			policy_digest,
			window_nanoseconds,
			global_burst,
			isolation_domain_burst,
			credential_burst,
			activated_by,
			activation_correlation_id,
			reason_digest
		FROM authentication_rate_limit_policy_activations
		WHERE generation = $1
	`, generation).Scan(
		&activation.Contract,
		&activation.Generation,
		&policyDigest,
		&windowNanoseconds,
		&activation.Policy.GlobalBurst,
		&activation.Policy.IsolationDomainBurst,
		&activation.Policy.CredentialBurst,
		&activation.ActivatedBy,
		&activation.CorrelationID,
		&activation.ReasonDigest,
	)
	if err != nil {
		return AuthenticationRateLimitPolicyActivation{}, err
	}
	activation.Policy.Window = time.Duration(windowNanoseconds)
	expectedDigest := activation.Policy.digest()
	if !activation.Valid() || len(policyDigest) != sha256.Size ||
		subtle.ConstantTimeCompare(policyDigest, expectedDigest[:]) != 1 {
		return AuthenticationRateLimitPolicyActivation{}, ErrAuthenticationRateLimitPolicyActivationInvalid
	}
	return cloneAuthenticationRateLimitPolicyActivation(activation), nil
}

func cloneAuthenticationRateLimitPolicyActivation(
	activation AuthenticationRateLimitPolicyActivation,
) AuthenticationRateLimitPolicyActivation {
	activation.ReasonDigest = append([]byte(nil), activation.ReasonDigest...)
	return activation
}

func sameAuthenticationRateLimitPolicyActivation(
	left AuthenticationRateLimitPolicyActivation,
	right AuthenticationRateLimitPolicyActivation,
) bool {
	return left.Contract == right.Contract &&
		left.Generation == right.Generation &&
		left.Policy == right.Policy &&
		left.ActivatedBy == right.ActivatedBy &&
		left.CorrelationID == right.CorrelationID &&
		bytes.Equal(left.ReasonDigest, right.ReasonDigest)
}
