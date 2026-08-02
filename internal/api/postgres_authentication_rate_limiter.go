package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	"github.com/asabla/dataground/internal/persistence"
)

// PostgreSQLAuthenticationRateLimiter coordinates one explicit layered
// deployment policy across every API process that shares the repository.
type PostgreSQLAuthenticationRateLimiter struct {
	repository *persistence.Repository
	generation uint64
	policy     persistence.AuthenticationRateLimitPolicy
}

func NewPostgreSQLAuthenticationRateLimiter(
	repository *persistence.Repository,
	generation uint64,
	policy persistence.AuthenticationRateLimitPolicy,
) (*PostgreSQLAuthenticationRateLimiter, error) {
	if repository == nil || !repository.Configured() {
		return nil, errors.New("authentication rate limit repository is required")
	}
	if generation == 0 || generation > math.MaxInt64 || !policy.Valid() {
		return nil, errors.New("authentication rate limit policy is invalid")
	}
	return &PostgreSQLAuthenticationRateLimiter{
		repository: repository,
		generation: generation,
		policy:     policy,
	}, nil
}

func (limiter *PostgreSQLAuthenticationRateLimiter) AllowAuthentication(
	ctx context.Context,
	request AuthenticationRateLimitRequest,
) (AuthenticationRateLimitDecision, error) {
	if limiter == nil || limiter.repository == nil || ctx == nil || !request.Valid() || !limiter.policy.Valid() {
		return AuthenticationRateLimitDecision{}, persistence.ErrAuthenticationRateLimitInvalid
	}
	result, err := limiter.repository.AllowAuthentication(
		ctx,
		request.IsolationDomainID(),
		request.CredentialDigest(),
		limiter.generation,
		limiter.policy,
	)
	if err != nil {
		return AuthenticationRateLimitDecision{}, err
	}
	if result.Allowed {
		return AuthenticationRateLimitDecision{Allowed: true}, nil
	}
	return AuthenticationRateLimitDecision{RetryAfter: result.RetryAfter}, nil
}

func (limiter *PostgreSQLAuthenticationRateLimiter) Ready(ctx context.Context) error {
	if limiter == nil || limiter.repository == nil || ctx == nil ||
		limiter.generation == 0 || !limiter.policy.Valid() {
		return persistence.ErrAuthenticationRateLimitInvalid
	}
	return limiter.repository.AuthenticationRateLimitPolicyReady(ctx, limiter.generation, limiter.policy)
}

func (*PostgreSQLAuthenticationRateLimiter) MarshalJSON() ([]byte, error) {
	return nil, errors.New("PostgreSQL authentication rate limiters cannot be serialized")
}

var _ AuthenticationRateLimiter = (*PostgreSQLAuthenticationRateLimiter)(nil)
var _ json.Marshaler = (*PostgreSQLAuthenticationRateLimiter)(nil)
