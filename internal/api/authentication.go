package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"reflect"
	"regexp"
	"strings"

	"github.com/asabla/dataground/internal/authn"
)

const maximumAuthorizationHeaderBytes = 8 << 10

var bearerTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)

type authenticatedPrincipalKey struct{}

func newProtectedRoute(
	authenticator authn.Authenticator,
) (func(http.HandlerFunc) http.Handler, error) {
	if authenticator == nil || isNilInterface(authenticator) {
		return nil, errors.New("API authenticator is required")
	}
	return func(next http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			values := request.Header.Values("Authorization")
			request.Header.Del("Authorization")
			bearerToken, ok := parseBearerToken(values)
			if !ok {
				writeAuthenticationRequired(response)
				return
			}
			defer clear(bearerToken)

			principal, err := authenticator.Authenticate(request.Context(), bearerToken)
			if err != nil {
				if errors.Is(err, authn.ErrInvalidCredential) {
					writeAuthenticationRequired(response)
					return
				}
				writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError(
					"AUTHENTICATION_UNAVAILABLE",
					"Authentication is temporarily unavailable.",
					true,
				)})
				return
			}
			if !principal.Valid() {
				writeJSON(response, http.StatusServiceUnavailable, ErrorEnvelope{Error: safeError(
					"AUTHENTICATION_UNAVAILABLE",
					"Authentication is temporarily unavailable.",
					true,
				)})
				return
			}

			domainID := request.PathValue("isolationDomainId")
			if !isolationDomainPattern.MatchString(domainID) {
				writeJSON(response, http.StatusBadRequest, ErrorEnvelope{Error: safeError(
					"INVALID_ISOLATION_DOMAIN",
					"Isolation domain identifier is invalid.",
					false,
				)})
				return
			}
			if !principal.AllowsIsolationDomain(domainID) {
				writeJSON(response, http.StatusForbidden, ErrorEnvelope{Error: safeError(
					"ISOLATION_DOMAIN_FORBIDDEN",
					"The authenticated principal cannot access this isolation domain.",
					false,
				)})
				return
			}

			ctx := context.WithValue(request.Context(), authenticatedPrincipalKey{}, principal)
			next.ServeHTTP(response, request.WithContext(ctx))
		})
	}, nil
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

func authenticatedRequestDigest(actorID string, body []byte) [sha256.Size]byte {
	digest := sha256.New()
	_, _ = digest.Write([]byte(actorID))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(body)
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func writeAuthenticationRequired(response http.ResponseWriter) {
	response.Header().Set("WWW-Authenticate", `Bearer realm="dataground-api"`)
	writeJSON(response, http.StatusUnauthorized, ErrorEnvelope{Error: safeError(
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
