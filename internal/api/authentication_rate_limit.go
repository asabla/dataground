package api

import (
	"context"
	"crypto/sha256"
	"time"
)

const maximumAuthenticationRetryAfter = 24 * time.Hour

// AuthenticationRateLimitRequest is the minimized input to a deployment-owned
// authentication admission policy. CredentialDigest is transient correlation
// material and must not be logged or included in authentication audit records.
type AuthenticationRateLimitRequest struct {
	IsolationDomainID string
	CredentialDigest  [sha256.Size]byte
}

func (request AuthenticationRateLimitRequest) Valid() bool {
	return isolationDomainPattern.MatchString(request.IsolationDomainID)
}

// AuthenticationRateLimitDecision either admits the request or supplies a
// bounded delay for a stable HTTP Retry-After response.
type AuthenticationRateLimitDecision struct {
	Allowed    bool
	RetryAfter time.Duration
}

func (decision AuthenticationRateLimitDecision) Valid() bool {
	if decision.Allowed {
		return decision.RetryAfter == 0
	}
	return decision.RetryAfter > 0 && decision.RetryAfter <= maximumAuthenticationRetryAfter
}

type AuthenticationRateLimiter interface {
	AllowAuthentication(context.Context, AuthenticationRateLimitRequest) (AuthenticationRateLimitDecision, error)
}

func authenticationRateLimitRequest(
	isolationDomainID string,
	bearerToken []byte,
) AuthenticationRateLimitRequest {
	return AuthenticationRateLimitRequest{
		IsolationDomainID: isolationDomainID,
		CredentialDigest:  sha256.Sum256(bearerToken),
	}
}

func authenticationRetryAfterSeconds(delay time.Duration) int64 {
	return int64((delay + time.Second - 1) / time.Second)
}
