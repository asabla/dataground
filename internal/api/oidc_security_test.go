package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5/pgxpool"
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

func TestDurableOIDCDPoPAssemblyRejectsUnconfiguredRepositoryBeforeSourceIO(t *testing.T) {
	t.Parallel()

	source := &apiAssemblyKeysetSource{snapshot: apiAssemblyKeysetSnapshot(1)}
	config := apiAssemblyConfig(t, source)
	config.Repository = persistence.NewRepository(nil)
	if _, err := api.NewDurableOIDCDPoPAssembly(context.Background(), config); err == nil {
		t.Fatal("unconfigured durable repository was accepted")
	}
	if source.calls != 0 {
		t.Fatalf("unconfigured repository contacted keyset source %d times", source.calls)
	}
}

func TestDurableOIDCDPoPAssemblyNilHandlerFailsClosed(t *testing.T) {
	t.Parallel()

	var assembly *api.DurableOIDCDPoPAssembly
	request := httptest.NewRequest(http.MethodPost, "/v1/unavailable", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("DPoP", "proof")
	response := httptest.NewRecorder()

	assembly.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if request.Header.Get("Authorization") != "" || request.Header.Get("DPoP") != "" {
		t.Fatal("unavailable assembly retained credential headers")
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
		Repository:     apiAssemblyRepository(t),
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

func apiAssemblyRepository(t *testing.T) *persistence.Repository {
	t.Helper()
	config, err := pgxpool.ParseConfig(
		"postgres://dataground:dataground@127.0.0.1:1/dataground?sslmode=disable",
	)
	if err != nil {
		t.Fatalf("parse test pool configuration: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("create test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return persistence.NewRepository(pool)
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
