package authn

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maximumOIDCAudiences = 16
	maximumOIDCValueBytes = 512
)

var ErrIdentityNotFound = errors.New("OIDC identity is not registered")

// VerifiedOIDCToken is the minimal result accepted from a deployment-owned
// signature, issuer, time, and revocation verifier. Raw claims stay behind that
// boundary so caller-controlled groups cannot become platform authority.
type VerifiedOIDCToken struct {
	Issuer    string
	Subject   string
	Audiences []string
}

type OIDCTokenVerifier interface {
	Verify(context.Context, []byte) (VerifiedOIDCToken, error)
}

type OIDCIdentity struct {
	Issuer  string
	Subject string
}

type OIDCIdentityBinding struct {
	PrincipalID       string
	PrincipalKind     PrincipalKind
	IsolationDomains []string
}

type OIDCIdentityResolver interface {
	Resolve(context.Context, OIDCIdentity) (OIDCIdentityBinding, error)
}

type OIDCConfig struct {
	Issuer   string
	Audience string
	Verifier OIDCTokenVerifier
	Resolver OIDCIdentityResolver
}

type OIDCAuthenticator struct {
	issuer   string
	audience string
	verifier OIDCTokenVerifier
	resolver OIDCIdentityResolver
}

func NewOIDCAuthenticator(config OIDCConfig) (*OIDCAuthenticator, error) {
	if !validOIDCIssuer(config.Issuer) {
		return nil, errors.New("OIDC issuer is invalid")
	}
	if config.Audience != APIAudience {
		return nil, errors.New("OIDC audience is invalid")
	}
	if nilOIDCDependency(config.Verifier) {
		return nil, errors.New("OIDC token verifier is required")
	}
	if nilOIDCDependency(config.Resolver) {
		return nil, errors.New("OIDC identity resolver is required")
	}
	return &OIDCAuthenticator{
		issuer:   config.Issuer,
		audience: config.Audience,
		verifier: config.Verifier,
		resolver: config.Resolver,
	}, nil
}

func (authenticator *OIDCAuthenticator) Authenticate(
	ctx context.Context,
	bearerToken []byte,
) (Principal, error) {
	if authenticator == nil || nilOIDCDependency(authenticator.verifier) || nilOIDCDependency(authenticator.resolver) {
		return Principal{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	if len(bearerToken) < minimumBearerTokenBytes || len(bearerToken) > maximumBearerTokenBytes {
		return Principal{}, ErrInvalidCredential
	}

	// The verifier receives an owned transient copy. Clearing it prevents a
	// verifier implementation from retaining usable token bytes through a slice.
	candidate := append([]byte(nil), bearerToken...)
	verified, err := authenticator.verifier.Verify(ctx, candidate)
	clear(candidate)
	if err != nil {
		return Principal{}, classifyOIDCDependencyError(err)
	}
	if !authenticator.accepts(verified) {
		return Principal{}, ErrInvalidCredential
	}

	identity := OIDCIdentity{Issuer: verified.Issuer, Subject: verified.Subject}
	binding, err := authenticator.resolver.Resolve(ctx, identity)
	if err != nil {
		if errors.Is(err, ErrIdentityNotFound) {
			return Principal{}, ErrInvalidCredential
		}
		return Principal{}, classifyOIDCDependencyError(err)
	}
	principal, err := NewPrincipal(PrincipalInput{
		ID:               binding.PrincipalID,
		Kind:             binding.PrincipalKind,
		Issuer:           verified.Issuer,
		Subject:          verified.Subject,
		Audience:         authenticator.audience,
		IsolationDomains: append([]string(nil), binding.IsolationDomains...),
	})
	if err != nil {
		// A verifier-accepted identity with an invalid platform binding is an
		// operator or registry failure, not an invalid caller credential.
		return Principal{}, ErrUnavailable
	}
	return principal, nil
}

func (authenticator *OIDCAuthenticator) accepts(token VerifiedOIDCToken) bool {
	if token.Issuer != authenticator.issuer || !validOIDCValue(token.Subject) {
		return false
	}
	if len(token.Audiences) == 0 || len(token.Audiences) > maximumOIDCAudiences {
		return false
	}
	seen := make(map[string]struct{}, len(token.Audiences))
	accepted := false
	for _, audience := range token.Audiences {
		if !validOIDCValue(audience) {
			return false
		}
		if _, duplicate := seen[audience]; duplicate {
			return false
		}
		seen[audience] = struct{}{}
		if audience == authenticator.audience {
			accepted = true
		}
	}
	return accepted
}

func classifyOIDCDependencyError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrInvalidCredential):
		return ErrInvalidCredential
	case errors.Is(err, ErrUnavailable):
		return ErrUnavailable
	default:
		return ErrUnavailable
	}
}

func validOIDCIssuer(issuer string) bool {
	if !validOIDCValue(issuer) {
		return false
	}
	parsed, err := url.Parse(issuer)
	return err == nil &&
		parsed.Scheme == "https" &&
		parsed.Host != "" &&
		parsed.User == nil &&
		parsed.RawQuery == "" &&
		parsed.Fragment == "" &&
		parsed.String() == issuer
}

func validOIDCValue(value string) bool {
	if value == "" || len(value) > maximumOIDCValueBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func nilOIDCDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func (*OIDCAuthenticator) MarshalJSON() ([]byte, error) {
	return nil, errors.New("authenticators cannot be serialized")
}

var _ Authenticator = (*OIDCAuthenticator)(nil)
var _ json.Marshaler = (*OIDCAuthenticator)(nil)
