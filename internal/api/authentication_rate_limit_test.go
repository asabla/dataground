package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
)

const rateLimitOrigin = "https://api.example.invalid"

func TestRateLimitedDPoPHandlerRejectsBeforeBindingOrAuthentication(t *testing.T) {
	t.Parallel()

	authenticator := &countingRateLimitAuthenticator{}
	limiter := &recordingAuthenticationRateLimiter{
		decision: api.AuthenticationRateLimitDecision{
			RetryAfter: 1500 * time.Millisecond,
		},
	}
	handler := newRateLimitedDPoPHandler(t, authenticator, limiter)
	reader := &countingRateLimitReader{}
	request := httptest.NewRequest(http.MethodPost, serviceCollectionPath(testDomain), reader)
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("DPoP", "unverified.proof.value")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusTooManyRequests)
	}
	if response.Header().Get("Retry-After") != "2" {
		t.Fatalf("Retry-After = %q, want %q", response.Header().Get("Retry-After"), "2")
	}
	if request.Header.Get("Authorization") != "" || request.Header.Get("DPoP") != "" {
		t.Fatal("rejected credentials remained in request headers")
	}
	if authenticator.calls != 0 {
		t.Fatalf("authentication calls = %d, want 0", authenticator.calls)
	}
	if reader.reads != 0 {
		t.Fatalf("request body reads = %d, want 0", reader.reads)
	}
	if len(limiter.requests) != 1 {
		t.Fatalf("rate limit requests = %d, want 1", len(limiter.requests))
	}
	got := limiter.requests[0]
	if got.IsolationDomainID != testDomain || got.CredentialDigest != sha256.Sum256([]byte(testToken)) {
		t.Fatalf("rate limit request = %#v", got)
	}
	var problem api.ErrorEnvelope
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if problem.Error.Code != "AUTHENTICATION_RATE_LIMITED" || !problem.Error.Retryable {
		t.Fatalf("problem = %#v", problem.Error)
	}
}

func TestRateLimitedDPoPHandlerFailsClosedWhenLimiterIsUnavailable(t *testing.T) {
	t.Parallel()

	for name, limiter := range map[string]*recordingAuthenticationRateLimiter{
		"dependency failure": {err: errors.New("rate limit backend failed")},
		"invalid decision":   {decision: api.AuthenticationRateLimitDecision{}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			authenticator := &countingRateLimitAuthenticator{}
			handler := newRateLimitedDPoPHandler(t, authenticator, limiter)
			request := httptest.NewRequest(http.MethodPost, serviceCollectionPath(testDomain), nil)
			request.Header.Set("Authorization", "Bearer "+testToken)
			request.Header.Set("DPoP", "unverified.proof.value")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
			}
			if response.Header().Get("Retry-After") != "" {
				t.Fatalf("unexpected Retry-After = %q", response.Header().Get("Retry-After"))
			}
			if request.Header.Get("Authorization") != "" || request.Header.Get("DPoP") != "" {
				t.Fatal("unavailable admission left credentials in request headers")
			}
			if authenticator.calls != 0 {
				t.Fatalf("authentication calls = %d, want 0", authenticator.calls)
			}
			var problem api.ErrorEnvelope
			if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if problem.Error.Code != "AUTHENTICATION_RATE_LIMIT_UNAVAILABLE" || !problem.Error.Retryable {
				t.Fatalf("problem = %#v", problem.Error)
			}
		})
	}
}

func TestRateLimitedDPoPHandlerLeavesHealthOutsideAuthenticationAdmission(t *testing.T) {
	t.Parallel()

	limiter := &recordingAuthenticationRateLimiter{
		decision: api.AuthenticationRateLimitDecision{RetryAfter: time.Minute},
	}
	handler := newRateLimitedDPoPHandler(t, &countingRateLimitAuthenticator{}, limiter)
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if len(limiter.requests) != 0 {
		t.Fatalf("health probe entered rate limiter %d times", len(limiter.requests))
	}
}

func TestRateLimitedDPoPHandlerRejectsTypedNilLimiter(t *testing.T) {
	t.Parallel()

	binder, err := api.NewDPoPRequestBinder(rateLimitOrigin)
	if err != nil {
		t.Fatalf("create DPoP binder: %v", err)
	}
	var limiter *recordingAuthenticationRateLimiter
	if _, err := api.NewRateLimitedDPoPHandler(
		&countingRateLimitAuthenticator{}, allowAuthorizer{}, binder, limiter,
	); err == nil {
		t.Fatal("typed-nil authentication rate limiter was accepted")
	}
}

func newRateLimitedDPoPHandler(
	t *testing.T,
	authenticator authn.Authenticator,
	limiter api.AuthenticationRateLimiter,
) http.Handler {
	t.Helper()
	binder, err := api.NewDPoPRequestBinder(rateLimitOrigin)
	if err != nil {
		t.Fatalf("create DPoP binder: %v", err)
	}
	handler, err := api.NewRateLimitedDPoPHandler(authenticator, allowAuthorizer{}, binder, limiter)
	if err != nil {
		t.Fatalf("create rate-limited DPoP handler: %v", err)
	}
	return handler
}

type recordingAuthenticationRateLimiter struct {
	decision api.AuthenticationRateLimitDecision
	err      error
	requests []api.AuthenticationRateLimitRequest
}

func (limiter *recordingAuthenticationRateLimiter) AllowAuthentication(
	ctx context.Context,
	request api.AuthenticationRateLimitRequest,
) (api.AuthenticationRateLimitDecision, error) {
	if err := ctx.Err(); err != nil {
		return api.AuthenticationRateLimitDecision{}, err
	}
	limiter.requests = append(limiter.requests, request)
	return limiter.decision, limiter.err
}

type countingRateLimitAuthenticator struct {
	calls int
}

func (authenticator *countingRateLimitAuthenticator) Authenticate(
	context.Context,
	[]byte,
) (authn.Principal, error) {
	authenticator.calls++
	return authn.Principal{}, authn.ErrInvalidCredential
}

type countingRateLimitReader struct {
	reads int
}

func (reader *countingRateLimitReader) Read([]byte) (int, error) {
	reader.reads++
	return 0, errors.New("request body must not be read")
}

var _ api.AuthenticationRateLimiter = (*recordingAuthenticationRateLimiter)(nil)
var _ authn.Authenticator = (*countingRateLimitAuthenticator)(nil)
