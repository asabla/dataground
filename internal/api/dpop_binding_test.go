package api

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	dpopBindingDomain = "iso_00000000000000000001"
	dpopBindingOrigin = "https://api.example.invalid"
	dpopBindingPath   = "/v1/isolation-domains/iso_00000000000000000001/agent-services"
)

func TestDPoPRequestBinderUsesOnlyPinnedOriginAndCanonicalPath(t *testing.T) {
	t.Parallel()
	binder, err := NewDPoPRequestBinder(dpopBindingOrigin)
	if err != nil {
		t.Fatalf("create DPoP request binder: %v", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	accessToken := []byte("signed-access-token-with-at-least-thirty-two-bytes")
	externalURI := dpopBindingOrigin + dpopBindingPath
	proof := signedDPoPBindingProof(t, privateKey, publicKey, http.MethodPost, externalURI, accessToken)

	request := httptest.NewRequest(http.MethodPost, dpopBindingPath+"?ignored=true", nil)
	request.Host = "attacker.example.invalid"
	request.Header.Set("Forwarded", "proto=http;host=attacker.example.invalid")
	request.Header.Set("X-Forwarded-Host", "attacker.example.invalid")
	request.Header.Set("X-Forwarded-Proto", "http")
	request.Header.Set("DPoP", proof)

	ctx, err := binder.bind(request, dpopBindingDomain)
	if err != nil {
		t.Fatalf("bind DPoP request: %v", err)
	}
	if request.Header.Get("DPoP") != "" {
		t.Fatal("DPoP proof remained available after request binding")
	}
	thumbprint := dpopBindingThumbprint(t, publicKey)
	replays := &dpopBindingReplayStore{}
	verifier, err := authn.NewDPoPTokenVerifier(authn.DPoPConfig{
		Verifier: &dpopBindingTokenVerifier{thumbprint: thumbprint},
		Replays:  replays, ClockSkew: 30 * time.Second, MaximumProofAge: time.Minute,
	})
	if err != nil {
		t.Fatalf("create DPoP verifier: %v", err)
	}
	if _, err := verifier.Verify(ctx, accessToken); err != nil {
		t.Fatalf("verify bound request: %v", err)
	}
	if len(replays.reservations) != 1 ||
		replays.reservations[0].IsolationDomainID != dpopBindingDomain {
		t.Fatalf("reservations = %#v", replays.reservations)
	}
}

func TestDPoPRequestBinderRejectsAmbiguousRequestTargetsAndHeaders(t *testing.T) {
	t.Parallel()
	binder, err := NewDPoPRequestBinder(dpopBindingOrigin)
	if err != nil {
		t.Fatalf("create DPoP request binder: %v", err)
	}

	tests := map[string]func(*http.Request){
		"missing proof": func(request *http.Request) {
			request.Header.Del("DPoP")
		},
		"duplicate proof": func(request *http.Request) {
			request.Header.Add("DPoP", "second.proof.value")
		},
		"oversized proof": func(request *http.Request) {
			request.Header.Set("DPoP", strings.Repeat("a", maximumDPoPHeaderBytes+1))
		},
		"absolute target": func(request *http.Request) {
			request.URL.Scheme = "https"
			request.URL.Host = "internal.example.invalid"
		},
		"dot segment": func(request *http.Request) {
			request.URL.Path = "/v1/../other"
		},
		"duplicate slash": func(request *http.Request) {
			request.URL.Path = "/v1//other"
		},
		"encoded separator": func(request *http.Request) {
			request.URL.Path = "/v1/other"
			request.URL.RawPath = "/v1%2Fother"
		},
		"lowercase method": func(request *http.Request) {
			request.Method = "post"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, dpopBindingPath, nil)
			request.Header.Set("DPoP", "header.payload.signature")
			mutate(request)
			if _, err := binder.bind(request, dpopBindingDomain); err == nil {
				t.Fatal("ambiguous DPoP request binding was accepted")
			}
			if request.Header.Get("DPoP") != "" {
				t.Fatal("rejected DPoP proof remained in request headers")
			}
		})
	}
}

func TestDPoPBoundHandlerRejectsBindingFailureEvenWhenAuthenticatorAcceptsEmptyInput(t *testing.T) {
	t.Parallel()
	binder, err := NewDPoPRequestBinder(dpopBindingOrigin)
	if err != nil {
		t.Fatalf("create DPoP request binder: %v", err)
	}
	principal, err := authn.NewPrincipal(authn.PrincipalInput{
		ID: "usr_00000000000000000001", Kind: authn.PrincipalHuman,
		Issuer: "test", Subject: "test", Audience: authn.APIAudience,
		IsolationDomains: []string{dpopBindingDomain},
	})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	handler, err := NewDPoPBoundHandler(
		dpopBindingPermissiveAuthenticator{principal: principal},
		dpopBindingAllowAuthorizer{},
		binder,
	)
	if err != nil {
		t.Fatalf("create DPoP-bound handler: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, dpopBindingPath, nil)
	request.Header.Set("Authorization", "Bearer token-with-at-least-thirty-two-bytes")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
	if response.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing authentication challenge")
	}
}

func TestDPoPRequestBinderRejectsUntrustedOrigins(t *testing.T) {
	t.Parallel()
	for name, origin := range map[string]string{
		"plaintext":      "http://api.example.invalid",
		"path":           "https://api.example.invalid/v1",
		"query":          "https://api.example.invalid?tenant=other",
		"userinfo":       "https://user@api.example.invalid",
		"uppercase":      "https://API.example.invalid",
		"default port":   "https://api.example.invalid:443",
		"padded port":    "https://api.example.invalid:08443",
		"zero port":      "https://api.example.invalid:0",
		"overflow port":  "https://api.example.invalid:65536",
		"empty port":     "https://api.example.invalid:",
		"trailing dot":   "https://api.example.invalid.",
		"unicode host":   "https://äpple.example.invalid",
		"trailing slash": "https://api.example.invalid/",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewDPoPRequestBinder(origin); err == nil {
				t.Fatal("untrusted external origin was accepted")
			}
		})
	}
}

func signedDPoPBindingProof(
	t *testing.T,
	privateKey ed25519.PrivateKey,
	publicKey ed25519.PublicKey,
	method string,
	uri string,
	accessToken []byte,
) string {
	t.Helper()
	options := (&jose.SignerOptions{}).
		WithType("dpop+jwt").
		WithHeader("jwk", jose.JSONWebKey{Key: publicKey})
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: privateKey}, options)
	if err != nil {
		t.Fatalf("create DPoP signer: %v", err)
	}
	digest := sha256.Sum256(accessToken)
	proof, err := jwt.Signed(signer).Claims(map[string]any{
		"jti": "proof-id-00000000000001",
		"htm": method,
		"htu": uri,
		"iat": time.Now().Unix(),
		"ath": base64.RawURLEncoding.EncodeToString(digest[:]),
	}).Serialize()
	if err != nil {
		t.Fatalf("serialize DPoP proof: %v", err)
	}
	return proof
}

func dpopBindingThumbprint(t *testing.T, publicKey ed25519.PublicKey) string {
	t.Helper()
	key := jose.JSONWebKey{Key: publicKey}
	thumbprint, err := key.Thumbprint(crypto.SHA256)
	if err != nil {
		t.Fatalf("compute DPoP thumbprint: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(thumbprint)
}

type dpopBindingPermissiveAuthenticator struct {
	principal authn.Principal
}

func (authenticator dpopBindingPermissiveAuthenticator) Authenticate(
	_ context.Context,
	_ []byte,
) (authn.Principal, error) {
	return authenticator.principal, nil
}

type dpopBindingAllowAuthorizer struct{}

func (dpopBindingAllowAuthorizer) Authorize(context.Context, authz.Request) error {
	return nil
}

type dpopBindingTokenVerifier struct {
	thumbprint string
}

func (verifier *dpopBindingTokenVerifier) Verify(
	ctx context.Context,
	_ []byte,
) (authn.VerifiedOIDCToken, error) {
	if err := ctx.Err(); err != nil {
		return authn.VerifiedOIDCToken{}, err
	}
	if verifier == nil || verifier.thumbprint == "" {
		return authn.VerifiedOIDCToken{}, authn.ErrUnavailable
	}
	return authn.VerifiedOIDCToken{
		Issuer:                 "https://identity.example.invalid",
		Subject:                "provider-subject",
		Audiences:              []string{authn.APIAudience},
		ConfirmationThumbprint: verifier.thumbprint,
	}, nil
}

type dpopBindingReplayStore struct {
	reservations []authn.DPoPReplayReservation
}

func (store *dpopBindingReplayStore) ReserveDPoPProof(
	_ context.Context,
	reservation authn.DPoPReplayReservation,
) error {
	if store == nil {
		return errors.New("replay store is unavailable")
	}
	store.reservations = append(store.reservations, reservation)
	return nil
}

var _ authn.Authenticator = dpopBindingPermissiveAuthenticator{}
var _ authz.Authorizer = dpopBindingAllowAuthorizer{}
var _ authn.OIDCTokenVerifier = (*dpopBindingTokenVerifier)(nil)
var _ authn.DPoPReplayStore = (*dpopBindingReplayStore)(nil)
