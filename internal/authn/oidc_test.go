package authn_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/authn"
)

const (
	testOIDCIssuer   = "https://identity.example.invalid/realms/dataground"
	testOIDCAudience = "dataground-api"
	testOIDCSubject  = "subject-0001"
)

func TestOIDCAuthenticatorBindsVerifiedSubjectToPlatformIdentity(t *testing.T) {
	t.Parallel()

	verifier := &recordingOIDCVerifier{token: authn.VerifiedOIDCToken{
		Issuer: testOIDCIssuer, Subject: testOIDCSubject,
		Audiences: []string{"another-audience", testOIDCAudience},
	}}
	resolver := &recordingOIDCResolver{binding: authn.OIDCIdentityBinding{
		PrincipalID: testActor, PrincipalKind: authn.PrincipalHuman,
		IsolationDomains: []string{testDomain},
	}}
	authenticator := newOIDCAuthenticator(t, verifier, resolver)
	bearer := []byte("signed-oidc-access-token-with-at-least-thirty-two-bytes")

	principal, err := authenticator.Authenticate(context.Background(), bearer)
	if err != nil {
		t.Fatalf("authenticate OIDC token: %v", err)
	}
	if principal.ID() != testActor || principal.Kind() != authn.PrincipalHuman {
		t.Fatalf("unexpected principal: %q %q", principal.ID(), principal.Kind())
	}
	if !principal.AllowsIsolationDomain(testDomain) {
		t.Fatal("principal lost registered isolation domain")
	}
	if resolver.identity != (authn.OIDCIdentity{Issuer: testOIDCIssuer, Subject: testOIDCSubject}) {
		t.Fatalf("resolved identity = %#v", resolver.identity)
	}
	if string(bearer) != "signed-oidc-access-token-with-at-least-thirty-two-bytes" {
		t.Fatal("authenticator modified caller-owned bearer token")
	}
	for _, value := range verifier.input {
		if value != 0 {
			t.Fatal("verifier retained usable token bytes")
		}
	}
}

func TestOIDCAuthenticatorRejectsUntrustedIssuerAudienceAndSubject(t *testing.T) {
	t.Parallel()

	tests := map[string]authn.VerifiedOIDCToken{
		"wrong issuer": {
			Issuer: "https://other.example.invalid", Subject: testOIDCSubject,
			Audiences: []string{testOIDCAudience},
		},
		"missing subject": {
			Issuer: testOIDCIssuer, Audiences: []string{testOIDCAudience},
		},
		"control subject": {
			Issuer: testOIDCIssuer, Subject: "subject\t0001",
			Audiences: []string{testOIDCAudience},
		},
		"wrong audience": {
			Issuer: testOIDCIssuer, Subject: testOIDCSubject,
			Audiences: []string{"another-audience"},
		},
		"duplicate audience": {
			Issuer: testOIDCIssuer, Subject: testOIDCSubject,
			Audiences: []string{testOIDCAudience, testOIDCAudience},
		},
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			verifier := &recordingOIDCVerifier{token: token}
			resolver := &recordingOIDCResolver{binding: validOIDCBinding()}
			authenticator := newOIDCAuthenticator(t, verifier, resolver)
			if _, err := authenticator.Authenticate(
				context.Background(),
				[]byte("signed-oidc-access-token-with-at-least-thirty-two-bytes"),
			); !errors.Is(err, authn.ErrInvalidCredential) {
				t.Fatalf("error = %v, want invalid credential", err)
			}
			if resolver.calls != 0 {
				t.Fatal("untrusted token reached identity resolver")
			}
		})
	}
}

func TestOIDCAuthenticatorSeparatesUnknownIdentityFromRegistryFailure(t *testing.T) {
	t.Parallel()

	verifier := &recordingOIDCVerifier{token: validVerifiedOIDCToken()}
	for name, resolver := range map[string]*recordingOIDCResolver{
		"unknown identity": {err: authn.ErrIdentityNotFound},
		"registry failure": {err: errors.New("private registry detail")},
		"invalid binding":  {binding: authn.OIDCIdentityBinding{PrincipalID: "invalid"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			authenticator := newOIDCAuthenticator(t, verifier.clone(), resolver)
			_, err := authenticator.Authenticate(
				context.Background(),
				[]byte("signed-oidc-access-token-with-at-least-thirty-two-bytes"),
			)
			if name == "unknown identity" {
				if !errors.Is(err, authn.ErrInvalidCredential) {
					t.Fatalf("error = %v, want invalid credential", err)
				}
			} else if !errors.Is(err, authn.ErrUnavailable) {
				t.Fatalf("error = %v, want unavailable", err)
			}
		})
	}
}

func TestOIDCAuthenticatorSanitizesVerifierFailureAndPreservesCancellation(t *testing.T) {
	t.Parallel()

	for name, verifierError := range map[string]error{
		"invalid":       authn.ErrInvalidCredential,
		"unavailable":   authn.ErrUnavailable,
		"private error": errors.New("private provider detail"),
		"cancelled":     context.Canceled,
		"deadline":      context.DeadlineExceeded,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			authenticator := newOIDCAuthenticator(
				t,
				&recordingOIDCVerifier{err: verifierError},
				&recordingOIDCResolver{binding: validOIDCBinding()},
			)
			_, err := authenticator.Authenticate(
				context.Background(),
				[]byte("signed-oidc-access-token-with-at-least-thirty-two-bytes"),
			)
			want := authn.ErrUnavailable
			switch name {
			case "invalid":
				want = authn.ErrInvalidCredential
			case "cancelled":
				want = context.Canceled
			case "deadline":
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("error = %v, want %v", err, want)
			}
		})
	}
}

func TestOIDCAuthenticatorRejectsIncompleteAssemblyAndSerialization(t *testing.T) {
	t.Parallel()

	var verifier *recordingOIDCVerifier
	if _, err := authn.NewOIDCAuthenticator(authn.OIDCConfig{
		Issuer: testOIDCIssuer, Audience: testOIDCAudience,
		Verifier: verifier, Resolver: &recordingOIDCResolver{},
	}); err == nil {
		t.Fatal("typed-nil verifier was accepted")
	}
	var resolver *recordingOIDCResolver
	if _, err := authn.NewOIDCAuthenticator(authn.OIDCConfig{
		Issuer: testOIDCIssuer, Audience: testOIDCAudience,
		Verifier: &recordingOIDCVerifier{}, Resolver: resolver,
	}); err == nil {
		t.Fatal("typed-nil resolver was accepted")
	}
	if _, err := authn.NewOIDCAuthenticator(authn.OIDCConfig{
		Issuer: "http://identity.example.invalid", Audience: testOIDCAudience,
		Verifier: &recordingOIDCVerifier{}, Resolver: &recordingOIDCResolver{},
	}); err == nil {
		t.Fatal("plaintext issuer was accepted")
	}
	if _, err := authn.NewOIDCAuthenticator(authn.OIDCConfig{
		Issuer: testOIDCIssuer, Audience: "other-api",
		Verifier: &recordingOIDCVerifier{}, Resolver: &recordingOIDCResolver{},
	}); err == nil {
		t.Fatal("non-API audience was accepted")
	}

	authenticator := newOIDCAuthenticator(
		t,
		&recordingOIDCVerifier{token: validVerifiedOIDCToken()},
		&recordingOIDCResolver{binding: validOIDCBinding()},
	)
	if _, err := json.Marshal(authenticator); err == nil {
		t.Fatal("OIDC authenticator serialized")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authenticator.Authenticate(
		ctx,
		[]byte("signed-oidc-access-token-with-at-least-thirty-two-bytes"),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled authentication = %v", err)
	}
}

func TestOIDCIdentityAndBindingValidation(t *testing.T) {
	t.Parallel()

	identity := authn.OIDCIdentity{Issuer: testOIDCIssuer, Subject: testOIDCSubject}
	if !identity.Valid() {
		t.Fatal("valid OIDC identity was rejected")
	}
	binding := validOIDCBinding()
	if !binding.ValidFor(identity) {
		t.Fatal("valid OIDC identity binding was rejected")
	}
	if (authn.OIDCIdentity{Issuer: "http://identity.example.invalid", Subject: testOIDCSubject}).Valid() {
		t.Fatal("plaintext OIDC identity was accepted")
	}
	binding.IsolationDomains = append(binding.IsolationDomains, binding.IsolationDomains[0])
	if binding.ValidFor(identity) {
		t.Fatal("duplicate-domain OIDC binding was accepted")
	}
	binding = validOIDCBinding()
	binding.PrincipalKind = authn.PrincipalSandboxWorkload
	if binding.ValidFor(identity) {
		t.Fatal("internal workload OIDC binding was accepted")
	}
}

func newOIDCAuthenticator(
	t *testing.T,
	verifier authn.OIDCTokenVerifier,
	resolver authn.OIDCIdentityResolver,
) *authn.OIDCAuthenticator {
	t.Helper()
	authenticator, err := authn.NewOIDCAuthenticator(authn.OIDCConfig{
		Issuer: testOIDCIssuer, Audience: testOIDCAudience,
		Verifier: verifier, Resolver: resolver,
	})
	if err != nil {
		t.Fatalf("create OIDC authenticator: %v", err)
	}
	return authenticator
}

func validVerifiedOIDCToken() authn.VerifiedOIDCToken {
	return authn.VerifiedOIDCToken{
		Issuer: testOIDCIssuer, Subject: testOIDCSubject,
		Audiences: []string{testOIDCAudience},
	}
}

func validOIDCBinding() authn.OIDCIdentityBinding {
	return authn.OIDCIdentityBinding{
		PrincipalID: testActor, PrincipalKind: authn.PrincipalHuman,
		IsolationDomains: []string{testDomain},
	}
}

type recordingOIDCVerifier struct {
	token authn.VerifiedOIDCToken
	err   error
	input []byte
}

func (verifier *recordingOIDCVerifier) Verify(
	_ context.Context,
	bearerToken []byte,
) (authn.VerifiedOIDCToken, error) {
	verifier.input = bearerToken
	return verifier.token, verifier.err
}

func (verifier *recordingOIDCVerifier) clone() *recordingOIDCVerifier {
	return &recordingOIDCVerifier{token: verifier.token, err: verifier.err}
}

type recordingOIDCResolver struct {
	binding  authn.OIDCIdentityBinding
	err      error
	identity authn.OIDCIdentity
	calls    int
}

func (resolver *recordingOIDCResolver) Resolve(
	_ context.Context,
	identity authn.OIDCIdentity,
) (authn.OIDCIdentityBinding, error) {
	resolver.calls++
	resolver.identity = identity
	return resolver.binding, resolver.err
}

var _ authn.OIDCTokenVerifier = (*recordingOIDCVerifier)(nil)
var _ authn.OIDCIdentityResolver = (*recordingOIDCResolver)(nil)
