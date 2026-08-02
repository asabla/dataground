package api

import (
	"context"
	"crypto/sha256"
	"time"
)

const maximumAuthenticationRetryAfter = 24 * time.Hour

// AuthenticationRateLimitRequest is the minimized immutable input to a
// deployment-owned authentication admission policy. The credential digest is
// transient correlation material and must not be logged or included in
// authentication audit records.
type AuthenticationRateLimitRequest struct {
	isolationDomainID string
	credentialDigest  [sha256.Size]byte
}

func (request AuthenticationRateLimitRequest) IsolationDomainID() string {
	return request.isolationDomainID
}

func (request AuthenticationRateLimitRequest) CredentialDigest() [sha256.Size]byte {
	return request.credentialDigest
}

func (request AuthenticationRateLimitRequest) Valid() bool {
	return isolationDomainPattern.MatchString(request.isolationDomainID) &&
		request.credentialDigest != [sha256.Size]byte{}
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
	accessToken []byte,
) AuthenticationRateLimitRequest {
	return AuthenticationRateLimitRequest{
		isolationDomainID: isolationDomainID,
		credentialDigest:  sha256.Sum256(accessToken),
	}
}

func authenticationRetryAfterSeconds(delay time.Duration) int64 {
	return int64((delay + time.Second - 1) / time.Second)
}
