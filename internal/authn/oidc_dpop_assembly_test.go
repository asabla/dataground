package authn_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
)

func TestReloadableOIDCDPoPAuthenticatorAssemblesAndRefreshesOneChain(t *testing.T) {
	t.Parallel()

	source := &assemblyKeysetSource{snapshot: assemblyKeysetSnapshot(1)}
	authenticator, err := authn.NewReloadableOIDCDPoPAuthenticator(
		context.Background(),
		assemblyAuthenticationConfig(source),
	)
	if err != nil {
		t.Fatalf("assemble OIDC DPoP authenticator: %v", err)
	}
	if source.calls != 1 {
		t.Fatalf("initial keyset loads = %d, want 1", source.calls)
	}
	if authenticator.AuthenticationMethod() != authn.AuthenticationMethodOIDC {
		t.Fatalf("authentication method = %q", authenticator.AuthenticationMethod())
	}

	source.snapshot = assemblyKeysetSnapshot(2)
	if err := authenticator.RefreshKeyset(context.Background()); err != nil {
		t.Fatalf("refresh keyset: %v", err)
	}
	if source.calls != 2 {
		t.Fatalf("keyset loads = %d, want 2", source.calls)
	}
	if _, err := json.Marshal(authenticator); err == nil {
		t.Fatal("authenticator serialization succeeded")
	}
}

func TestReloadableOIDCDPoPAuthenticatorRejectsInvalidProfileBeforeSourceIO(t *testing.T) {
	t.Parallel()

	source := &assemblyKeysetSource{snapshot: assemblyKeysetSnapshot(1)}
	config := assemblyAuthenticationConfig(source)
	config.MaximumProofAge = 0
	if _, err := authn.NewReloadableOIDCDPoPAuthenticator(
		context.Background(),
		config,
	); err == nil {
		t.Fatal("invalid DPoP profile was accepted")
	}
	if source.calls != 0 {
		t.Fatalf("invalid profile contacted keyset source %d times", source.calls)
	}
}

func assemblyAuthenticationConfig(source authn.OIDCJWTKeysetSource) authn.ReloadableOIDCDPoPConfig {
	return authn.ReloadableOIDCDPoPConfig{
		Issuer:               "https://identity.example.invalid",
		Audience:             authn.APIAudience,
		Algorithms:           []string{"EdDSA"},
		KeysetSource:         source,
		IdentityResolver:     assemblyIdentityResolver{},
		ReplayStore:          assemblyReplayStore{},
		JWTClockSkew:         30 * time.Second,
		MaximumTokenLifetime: time.Hour,
		DPoPClockSkew:        30 * time.Second,
		MaximumProofAge:      time.Minute,
	}
}

func assemblyKeysetSnapshot(sequence uint64) authn.OIDCJWTKeysetSnapshot {
	x := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, ed25519.PublicKeySize))
	return authn.OIDCJWTKeysetSnapshot{
		Sequence: sequence,
		JWKS: []byte(fmt.Sprintf(
			`{"keys":[{"kty":"OKP","use":"sig","kid":"provider-key","alg":"EdDSA","crv":"Ed25519","x":%q}]}`,
			x,
		)),
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

type assemblyKeysetSource struct {
	snapshot authn.OIDCJWTKeysetSnapshot
	calls    int
}

func (source *assemblyKeysetSource) Load(context.Context) (authn.OIDCJWTKeysetSnapshot, error) {
	source.calls++
	snapshot := source.snapshot
	snapshot.JWKS = append([]byte(nil), snapshot.JWKS...)
	return snapshot, nil
}

type assemblyIdentityResolver struct{}

func (assemblyIdentityResolver) Resolve(
	context.Context,
	authn.OIDCIdentity,
) (authn.OIDCIdentityBinding, error) {
	return authn.OIDCIdentityBinding{}, authn.ErrIdentityNotFound
}

type assemblyReplayStore struct{}

func (assemblyReplayStore) ReserveDPoPProof(
	context.Context,
	authn.DPoPReplayReservation,
) error {
	return errors.New("not reached")
}

var _ authn.OIDCJWTKeysetSource = (*assemblyKeysetSource)(nil)
var _ authn.OIDCIdentityResolver = assemblyIdentityResolver{}
var _ authn.DPoPReplayStore = assemblyReplayStore{}
