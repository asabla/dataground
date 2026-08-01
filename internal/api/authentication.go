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
			bearerToken, _ := parseBearerToken(values)
			defer clear(bearerToken)
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
						response, request, bearerToken, correlationID,
					)
					return
				}
				limitRequest := authenticationRateLimitRequest(domainID, bearerToken)
				if !limitRequest.Valid() {
					writeAuthenticationRateLimitUnavailable(
						response, request, bearerToken, correlationID,
					)
					return
				}
				decision, limitErr := rateLimiter.AllowAuthentication(authenticationContext, limitRequest)
				if limitErr != nil || authenticationContext.Err() != nil || !decision.Valid() {
					writeAuthenticationRateLimitUnavailable(
						response, request, bearerToken, correlationID,
					)
					return
				}
				if !decision.Allowed {
					writeAuthenticationRateLimited(
						response, request, bearerToken, correlationID, decision.RetryAfter,
					)
					return
				}
			}

			bindingInvalid := false
			if dpopBinder != nil {
				boundContext, bindErr := dpopBinder.bind(request.WithContext(authenticationContext), domainID)
				if bindErr != nil {
					clear(bearerToken)
					bearerToken = nil
					bindingInvalid = true
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

			principal, err := authenticator.Authenticate(authenticationContext, bearerToken)
			if bindingInvalid {
				principal = authn.Principal{}
				err = authn.ErrInvalidCredential
			}
			if err != nil {
				if errors.Is(err, authn.ErrInvalidCredential) {
					writeAuthenticationRequired(response, correlationID)
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
	bearerToken []byte,
	correlationID string,
) {
	clear(bearerToken)
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
	bearerToken []byte,
	correlationID string,
	retryAfter time.Duration,
) {
	clear(bearerToken)
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

func parseBearerToken(values []string) ([]byte, bool) {
	if len(values) != 1 || len(values[0]) > maximumAuthorizationHeaderBytes {
		return nil, false
	}
	scheme, token, found := strings.Cut(values[0], " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
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

func writeAuthenticationRequired(response http.ResponseWriter, correlationID string) {
	response.Header().Set("WWW-Authenticate", `Bearer realm="dataground-api"`)
	writeJSON(response, http.StatusUnauthorized, ErrorEnvelope{Error: safeErrorWithCorrelation(
		correlationID,
		"UNAUTHENTICATED",
		"A valid bearer access token is required.",
		false,
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
