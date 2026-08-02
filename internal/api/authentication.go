package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/identity"
)

const maximumAuthorizationHeaderBytes = 8 << 10

var bearerTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)

type authenticatedPrincipalKey struct{}
type authenticatedCorrelationKey struct{}

func newProtectedRoute(
	authenticator authn.Authenticator,
	authorizer authz.Authorizer,
	dpopBinder *DPoPRequestBinder,
	rateLimiter AuthenticationRateLimiter,
) (func(authz.Action, authz.ResourceType, string, http.HandlerFunc) http.Handler, error) {
	if authenticator == nil || isNilInterface(authenticator) {
		return nil, errors.New("API authenticator is required")
	}
	if authorizer == nil || isNilInterface(authorizer) {
		return nil, errors.New("API authorizer is required")
	}
	if rateLimiter != nil && isNilInterface(rateLimiter) {
		return nil, errors.New("authentication rate limiter is invalid")
	}
	return func(
		action authz.Action,
		resourceType authz.ResourceType,
		resourcePathValue string,
		next http.HandlerFunc,
	) http.Handler {
		authorizationScheme := "Bearer"
		if dpopBinder != nil {
			authorizationScheme = "DPoP"
		}
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			correlationID := identity.New("cor")
			domainID := request.PathValue("isolationDomainId")
			if !isolationDomainPattern.MatchString(domainID) {
				writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: safeErrorWithCorrelation(
					correlationID,
					"INVALID_ISOLATION_DOMAIN",
					"Isolation domain identifier is invalid.",
					false,
				)})
				return
			}
			values := request.Header.Values("Authorization")
			request.Header.Del("Authorization")
			accessToken, credentialValid := parseAuthorizationToken(values, authorizationScheme)
			defer clear(accessToken)
			authenticationContext, err := authn.WithAuthenticationAttemptScope(
				request.Context(),
				authn.AuthenticationAttemptScope{
					IsolationDomainID: domainID,
					CorrelationID:     correlationID,
				},
			)
			if err != nil {
				writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
					correlationID,
					"AUTHENTICATION_UNAVAILABLE",
					"Authentication is temporarily unavailable.",
					true,
				)})
				return
			}

			if rateLimiter != nil {
				if authenticationContext.Err() != nil {
					writeAuthenticationRateLimitUnavailable(
						response, request, accessToken, correlationID,
					)
					return
				}
				limitRequest := authenticationRateLimitRequest(domainID, accessToken)
				if !limitRequest.Valid() {
					writeAuthenticationRateLimitUnavailable(
						response, request, accessToken, correlationID,
					)
					return
				}
				decision, limitErr := rateLimiter.AllowAuthentication(authenticationContext, limitRequest)
				if limitErr != nil || authenticationContext.Err() != nil || !decision.Valid() {
					writeAuthenticationRateLimitUnavailable(
						response, request, accessToken, correlationID,
					)
					return
				}
				if !decision.Allowed {
					writeAuthenticationRateLimited(
						response, request, accessToken, correlationID, decision.RetryAfter,
					)
					return
				}
			}

			authenticationRejected := !credentialValid
			if !credentialValid {
				clear(accessToken)
				accessToken = nil
				request.Header.Del("DPoP")
				rejectedContext, rejectionErr := authn.WithRejectedAuthenticationAttempt(authenticationContext)
				if rejectionErr != nil {
					writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
						correlationID,
						"AUTHENTICATION_UNAVAILABLE",
						"Authentication is temporarily unavailable.",
						true,
					)})
					return
				}
				authenticationContext = rejectedContext
			} else if dpopBinder != nil {
				boundContext, bindErr := dpopBinder.bind(request.WithContext(authenticationContext), domainID)
				if bindErr != nil {
					clear(accessToken)
					accessToken = nil
					authenticationRejected = true
					rejectedContext, rejectionErr := authn.WithRejectedAuthenticationAttempt(authenticationContext)
					if rejectionErr != nil {
						writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
							correlationID,
							"AUTHENTICATION_UNAVAILABLE",
							"Authentication is temporarily unavailable.",
							true,
						)})
						return
					}
					authenticationContext = rejectedContext
				} else {
					authenticationContext = boundContext
				}
			} else {
				request.Header.Del("DPoP")
			}

			principal, err := authenticator.Authenticate(authenticationContext, accessToken)
			if authenticationRejected {
				principal = authn.Principal{}
				err = authn.ErrInvalidCredential
			}
			if err != nil {
				if challenge, ok := authn.DPoPNonceChallenge(err); ok && dpopBinder != nil {
					writeDPoPNonceChallenge(response, correlationID, challenge)
					return
				}
				if errors.Is(err, authn.ErrInvalidCredential) {
					writeAuthenticationRequired(response, correlationID, dpopBinder != nil)
					return
				}
				writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
					correlationID,
					"AUTHENTICATION_UNAVAILABLE",
					"Authentication is temporarily unavailable.",
					true,
				)})
				return
			}
			if !principal.Valid() {
				writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
					correlationID,
					"AUTHENTICATION_UNAVAILABLE",
					"Authentication is temporarily unavailable.",
					true,
				)})
				return
			}

			if !principal.AllowsIsolationDomain(domainID) {
				writeJSON(response, http.StatusForbidden, ErrorEnvelope{Error: safeErrorWithCorrelation(
					correlationID,
					"ISOLATION_DOMAIN_FORBIDDEN",
					"The authenticated principal cannot access this isolation domain.",
					false,
				)})
				return
			}

			resourceID := domainID
			if resourcePathValue != "" {
				resourceID = request.PathValue(resourcePathValue)
			}
			if err := authorizer.Authorize(request.Context(), authz.Request{
				Principal: principal, Action: action, ResourceType: resourceType,
				ResourceID: resourceID, IsolationDomainID: domainID, CorrelationID: correlationID,
			}); err != nil {
				if errors.Is(err, authz.ErrDenied) {
					writeJSON(response, http.StatusForbidden, ErrorEnvelope{Error: safeErrorWithCorrelation(
						correlationID,
						"ACTION_FORBIDDEN",
						"The authenticated principal cannot perform this action.",
						false,
					)})
					return
				}
				writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
					correlationID,
					"AUTHORIZATION_UNAVAILABLE",
					"Authorization is temporarily unavailable.",
					true,
				)})
				return
			}

			ctx := context.WithValue(request.Context(), authenticatedPrincipalKey{}, principal)
			ctx = context.WithValue(ctx, authenticatedCorrelationKey{}, correlationID)
			next.ServeHTTP(response, request.WithContext(ctx))
		})
	}, nil
}

func writeAuthenticationRateLimitUnavailable(
	response http.ResponseWriter,
	request *http.Request,
	accessToken []byte,
	correlationID string,
) {
	clear(accessToken)
	request.Header.Del("DPoP")
	writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeErrorWithCorrelation(
		correlationID,
		"AUTHENTICATION_RATE_LIMIT_UNAVAILABLE",
		"Authentication admission is temporarily unavailable.",
		true,
	)})
}

func writeAuthenticationRateLimited(
	response http.ResponseWriter,
	request *http.Request,
	accessToken []byte,
	correlationID string,
	retryAfter time.Duration,
) {
	clear(accessToken)
	request.Header.Del("DPoP")
	response.Header().Set(
		"Retry-After",
		strconv.FormatInt(authenticationRetryAfterSeconds(retryAfter), 10),
	)
	writeJSON(response, http.StatusTooManyRequests, ErrorEnvelope{Error: safeErrorWithCorrelation(
		correlationID,
		"AUTHENTICATION_RATE_LIMITED",
		"Too many authentication requests.",
		true,
	)})
}

func parseAuthorizationToken(values []string, expectedScheme string) ([]byte, bool) {
	if len(values) != 1 || len(values[0]) > maximumAuthorizationHeaderBytes {
		return nil, false
	}
	scheme, token, found := strings.Cut(values[0], " ")
	if !found || !strings.EqualFold(scheme, expectedScheme) || token == "" {
		return nil, false
	}
	if strings.Contains(token, " ") || !bearerTokenPattern.MatchString(token) {
		return nil, false
	}
	return []byte(token), true
}

func authenticatedPrincipal(request *http.Request) (authn.Principal, bool) {
	principal, ok := request.Context().Value(authenticatedPrincipalKey{}).(authn.Principal)
	return principal, ok && principal.Valid()
}

func authenticatedActorID(request *http.Request) string {
	principal, ok := authenticatedPrincipal(request)
	if !ok {
		return ""
	}
	return principal.ID()
}

func authenticatedCorrelationID(request *http.Request) string {
	correlationID, ok := request.Context().Value(authenticatedCorrelationKey{}).(string)
	if !ok || correlationID == "" {
		return ""
	}
	return correlationID
}

func authenticatedRequestDigest(actorID string, body []byte) [sha256.Size]byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte(actorID))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(body)
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func writeAuthenticationRequired(response http.ResponseWriter, correlationID string, dpop bool) {
	challenge := `Bearer realm="dataground-api"`
	message := "A valid bearer access token is required."
	if dpop {
		challenge = `DPoP realm="dataground-api", algs="ES256 EdDSA"`
		message = "A valid DPoP-bound access token and proof are required."
	}
	response.Header().Set("WWW-Authenticate", challenge)
	writeJSON(response, http.StatusUnauthorized, ErrorEnvelope{Error: safeErrorWithCorrelation(
		correlationID,
		"UNAUTHENTICATED",
		message,
		false,
	)})
}

func writeDPoPNonceChallenge(response http.ResponseWriter, correlationID, challenge string) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("DPoP-Nonce", challenge)
	response.Header().Set(
		"WWW-Authenticate",
		`DPoP realm="dataground-api", error="use_dpop_nonce", error_description="Resource server requires nonce in DPoP proof", algs="ES256 EdDSA"`,
	)
	writeJSON(response, http.StatusUnauthorized, ErrorEnvelope{Error: safeErrorWithCorrelation(
		correlationID,
		"DPOP_NONCE_REQUIRED",
		"A fresh DPoP proof with the supplied nonce is required.",
		true,
	)})
}

func isNilInterface(value any) bool {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
