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
	capacity   *persistence.AuthenticationRateLimitCapacityEvidence
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

// NewCapacityBoundPostgreSQLAuthenticationRateLimiter requires one internally
// consistent accepted capacity record for the policy. Callers bind its source,
// runtime, and deployment profile before construction. The PostgreSQL profile
// remains part of readiness after assembly.
func NewCapacityBoundPostgreSQLAuthenticationRateLimiter(
	repository *persistence.Repository,
	generation uint64,
	policy persistence.AuthenticationRateLimitPolicy,
	evidence persistence.AuthenticationRateLimitCapacityEvidence,
) (*PostgreSQLAuthenticationRateLimiter, error) {
	limiter, err := NewPostgreSQLAuthenticationRateLimiter(repository, generation, policy)
	if err != nil {
		return nil, err
	}
	if !evidence.AcceptedFor(
		evidence.SourceRevision,
		evidence.DeploymentProfile,
		evidence.GoVersion,
		policy,
	) {
		return nil, errors.New("authentication rate limit capacity evidence is invalid")
	}
	owned := evidence
	owned.Phases = append([]persistence.AuthenticationRateLimitCapacityPhase(nil), evidence.Phases...)
	limiter.capacity = &owned
	return limiter, nil
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
	if limiter.capacity == nil {
		return limiter.repository.AuthenticationRateLimitPolicyReady(ctx, limiter.generation, limiter.policy)
	}
	return limiter.repository.AuthenticationRateLimitPolicyAndCapacityReady(
		ctx,
		limiter.generation,
		limiter.policy,
		*limiter.capacity,
	)
}

func (*PostgreSQLAuthenticationRateLimiter) MarshalJSON() ([]byte, error) {
	return nil, errors.New("PostgreSQL authentication rate limiters cannot be serialized")
}

var _ AuthenticationRateLimiter = (*PostgreSQLAuthenticationRateLimiter)(nil)
var _ json.Marshaler = (*PostgreSQLAuthenticationRateLimiter)(nil)
