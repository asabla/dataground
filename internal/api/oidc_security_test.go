package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/persistence"
)

const (
	assemblyDomainID    = "iso_00000000000000000001"
	assemblyPrincipalID = "usr_00000000000000000001"
)

func TestDurableOIDCDPoPAssemblyOwnsHandlerAndKeysetLifecycle(t *testing.T) {
	t.Parallel()

	source := &apiAssemblyKeysetSource{snapshot: apiAssemblyKeysetSnapshot(1)}
	assembly, err := api.NewDurableOIDCDPoPAssembly(
		context.Background(),
		apiAssemblyConfig(t, source),
	)
	if err != nil {
		t.Fatalf("assemble durable OIDC DPoP API: %v", err)
	}
	if assembly.Handler() == nil {
		t.Fatal("assembly returned a nil handler")
	}
	source.snapshot = apiAssemblyKeysetSnapshot(2)
	if err := assembly.RefreshOIDCKeyset(context.Background()); err != nil {
		t.Fatalf("refresh assembly keyset: %v", err)
	}
	if source.calls != 2 {
		t.Fatalf("keyset loads = %d, want 2", source.calls)
	}
	if _, err := json.Marshal(assembly); err == nil {
		t.Fatal("assembly serialization succeeded")
	}
}

func TestDurableOIDCDPoPAssemblyValidatesHTTPBoundaryBeforeSourceIO(t *testing.T) {
	t.Parallel()

	source := &apiAssemblyKeysetSource{snapshot: apiAssemblyKeysetSnapshot(1)}
	config := apiAssemblyConfig(t, source)
	config.ExternalOrigin = "http://api.example.invalid"
	if _, err := api.NewDurableOIDCDPoPAssembly(context.Background(), config); err == nil {
		t.Fatal("invalid external origin was accepted")
	}
	if source.calls != 0 {
		t.Fatalf("invalid HTTP boundary contacted keyset source %d times", source.calls)
	}
}

func apiAssemblyConfig(
	t *testing.T,
	source authn.OIDCJWTKeysetSource,
) api.DurableOIDCDPoPConfig {
	t.Helper()
	authorizer, err := authz.NewDevelopmentCedarAuthorizer(
		assemblyPrincipalID,
		assemblyDomainID,
	)
	if err != nil {
		t.Fatalf("create test authorizer: %v", err)
	}
	return api.DurableOIDCDPoPConfig{
		Repository:     persistence.NewRepository(nil),
		Authorizer:     authorizer,
		RateLimiter:    apiAssemblyRateLimiter{},
		ExternalOrigin: "https://api.example.invalid",
		OIDC: authn.ReloadableOIDCJWTConfig{
			Issuer:          "https://identity.example.invalid",
			Audience:        authn.APIAudience,
			Algorithms:      []string{"EdDSA"},
			Source:          source,
			ClockSkew:       30 * time.Second,
			MaximumLifetime: time.Hour,
		},
		DPoPClockSkew:   30 * time.Second,
		MaximumProofAge: time.Minute,
	}
}

func apiAssemblyKeysetSnapshot(sequence uint64) authn.OIDCJWTKeysetSnapshot {
	x := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, ed25519.PublicKeySize))
	return authn.OIDCJWTKeysetSnapshot{
		Sequence: sequence,
		JWKS: []byte(fmt.Sprintf(
			`{"keys":[{"kty":"OKP","use":"sig","kid":"provider-key","alg":"EdDSA","crv":"Ed25519","x":%q}]}`,
			x,
		)),
		ExpiresAt: time.Now().Add(time.Hour),
	}
}

type apiAssemblyKeysetSource struct {
	snapshot authn.OIDCJWTKeysetSnapshot
	calls    int
}

func (source *apiAssemblyKeysetSource) Load(context.Context) (authn.OIDCJWTKeysetSnapshot, error) {
	source.calls++
	snapshot := source.snapshot
	snapshot.JWKS = append([]byte(nil), snapshot.JWKS...)
	return snapshot, nil
}

type apiAssemblyRateLimiter struct{}

func (apiAssemblyRateLimiter) AllowAuthentication(
	context.Context,
	api.AuthenticationRateLimitRequest,
) (api.AuthenticationRateLimitDecision, error) {
	return api.AuthenticationRateLimitDecision{Allowed: true}, nil
}

var _ authn.OIDCJWTKeysetSource = (*apiAssemblyKeysetSource)(nil)
var _ api.AuthenticationRateLimiter = apiAssemblyRateLimiter{}
