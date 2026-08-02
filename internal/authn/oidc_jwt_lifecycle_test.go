package authn_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	jose "github.com/go-jose/go-jose/v4"
)

func TestReloadableOIDCJWTVerifierRotatesCompleteKeysets(t *testing.T) {
	firstKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	source := &oidcJWTKeysetSource{snapshot: oidcJWTKeysetSnapshot(
		t, 1, time.Now().Add(time.Hour), &firstKey.PublicKey, "lifecycle-key-1",
	)}
	verifier := newReloadableOIDCJWTVerifier(t, source)
	firstFixture := oidcJWTFixture{privateKey: firstKey}
	firstToken := firstFixture.sign(
		t, validOIDCJWTClaims(time.Now()), jose.RS256, "lifecycle-key-1", nil,
	)
	if _, err := verifier.Verify(context.Background(), []byte(firstToken)); err != nil {
		t.Fatalf("verify initial keyset: %v", err)
	}
	if err := verifier.Refresh(context.Background()); err != nil {
		t.Fatalf("replay initial keyset: %v", err)
	}

	source.setSnapshot(oidcJWTKeysetSnapshot(
		t, 2, time.Now().Add(time.Hour), &secondKey.PublicKey, "lifecycle-key-2",
	))
	if err := verifier.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh keyset: %v", err)
	}
	if _, err := verifier.Verify(context.Background(), []byte(firstToken)); !errors.Is(err, authn.ErrInvalidCredential) {
		t.Fatalf("retired key verification = %v", err)
	}
	secondFixture := oidcJWTFixture{privateKey: secondKey}
	secondToken := secondFixture.sign(
		t, validOIDCJWTClaims(time.Now()), jose.RS256, "lifecycle-key-2", nil,
	)
	if _, err := verifier.Verify(context.Background(), []byte(secondToken)); err != nil {
		t.Fatalf("verify rotated keyset: %v", err)
	}
}

func TestReloadableOIDCJWTVerifierRejectsRollbackAndConflictingReuse(t *testing.T) {
	firstKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().Add(time.Hour)
	source := &oidcJWTKeysetSource{snapshot: oidcJWTKeysetSnapshot(
		t, 2, expiresAt, &secondKey.PublicKey, "lifecycle-key-2",
	)}
	verifier := newReloadableOIDCJWTVerifier(t, source)
	secondFixture := oidcJWTFixture{privateKey: secondKey}
	secondToken := secondFixture.sign(
		t, validOIDCJWTClaims(time.Now()), jose.RS256, "lifecycle-key-2", nil,
	)

	source.setSnapshot(oidcJWTKeysetSnapshot(
		t, 1, expiresAt, &firstKey.PublicKey, "lifecycle-key-1",
	))
	if err := verifier.Refresh(context.Background()); !errors.Is(err, authn.ErrOIDCJWTKeysetRollback) {
		t.Fatalf("keyset rollback = %v", err)
	}
	source.setSnapshot(oidcJWTKeysetSnapshot(
		t, 2, expiresAt, &firstKey.PublicKey, "lifecycle-key-1",
	))
	if err := verifier.Refresh(context.Background()); !errors.Is(err, authn.ErrOIDCJWTKeysetConflict) {
		t.Fatalf("conflicting keyset sequence = %v", err)
	}
	if _, err := verifier.Verify(context.Background(), []byte(secondToken)); err != nil {
		t.Fatalf("verify retained keyset: %v", err)
	}
}

func TestReloadableOIDCJWTVerifierRejectsInvalidSourcesAndSnapshots(t *testing.T) {
	var typedNil *oidcJWTKeysetSource
	if _, err := authn.NewReloadableOIDCJWTVerifier(context.Background(), authn.ReloadableOIDCJWTConfig{
		Issuer: testOIDCIssuer, Audience: testOIDCAudience, ProviderID: "primary",
		ProviderRegistrySHA256: strings.Repeat("1", 64), Algorithms: []string{"RS256"},
		ClockSkew: 30 * time.Second, MaximumLifetime: time.Hour, Source: typedNil,
	}); err == nil {
		t.Fatal("typed-nil keyset source was accepted")
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	wrongProvider := oidcJWTKeysetSnapshot(
		t, 1, time.Now().Add(time.Hour), &privateKey.PublicKey, "lifecycle-key-1",
	)
	wrongProvider.ProviderID = "secondary"
	for name, snapshot := range map[string]authn.OIDCJWTKeysetSnapshot{
		"provider mismatch": wrongProvider,
		"zero sequence": oidcJWTKeysetSnapshot(
			t, 0, time.Now().Add(time.Hour), &privateKey.PublicKey, "lifecycle-key-1",
		),
		"expired": oidcJWTKeysetSnapshot(
			t, 1, time.Now().Add(-time.Minute), &privateKey.PublicKey, "lifecycle-key-1",
		),
		"unbounded lifetime": oidcJWTKeysetSnapshot(
			t, 1, time.Now().Add(25*time.Hour), &privateKey.PublicKey, "lifecycle-key-1",
		),
		"invalid JWKS": {Sequence: 1, JWKS: []byte(`{"keys":[]}`), ExpiresAt: time.Now().Add(time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			source := &oidcJWTKeysetSource{snapshot: snapshot}
			if _, err := authn.NewReloadableOIDCJWTVerifier(
				context.Background(),
				reloadableOIDCJWTConfig(source),
			); !errors.Is(err, authn.ErrOIDCJWTKeysetInvalid) {
				t.Fatalf("invalid keyset snapshot = %v", err)
			}
		})
	}
}

func TestReloadableOIDCJWTVerifierValidatesProfileBeforeLoadingSource(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	source := &oidcJWTKeysetSource{snapshot: oidcJWTKeysetSnapshot(
		t, 1, time.Now().Add(time.Hour), &privateKey.PublicKey, "lifecycle-key-1",
	)}
	config := reloadableOIDCJWTConfig(source)
	config.Issuer = "http://identity.example.invalid"
	if _, err := authn.NewReloadableOIDCJWTVerifier(context.Background(), config); err == nil {
		t.Fatal("invalid reloadable profile was accepted")
	}
	if loads := source.loadCount(); loads != 0 {
		t.Fatalf("invalid profile loaded source %d times", loads)
	}
}

func TestReloadableOIDCJWTVerifierRetainsValidGenerationWhenSourceIsUnavailable(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	source := &oidcJWTKeysetSource{snapshot: oidcJWTKeysetSnapshot(
		t, 1, time.Now().Add(time.Hour), &privateKey.PublicKey, "lifecycle-key-1",
	)}
	verifier := newReloadableOIDCJWTVerifier(t, source)
	fixture := oidcJWTFixture{privateKey: privateKey}
	token := fixture.sign(
		t, validOIDCJWTClaims(time.Now()), jose.RS256, "lifecycle-key-1", nil,
	)
	source.setError(errors.New("source unavailable"))
	if err := verifier.Refresh(context.Background()); !errors.Is(err, authn.ErrUnavailable) {
		t.Fatalf("unavailable refresh = %v", err)
	}
	source.setError(authn.ErrOIDCJWTKeysetInvalid)
	if err := verifier.Refresh(context.Background()); !errors.Is(err, authn.ErrOIDCJWTKeysetInvalid) {
		t.Fatalf("invalid publication refresh = %v", err)
	}
	if _, err := verifier.Verify(context.Background(), []byte(token)); err != nil {
		t.Fatalf("verify retained generation: %v", err)
	}
}

func TestReloadableOIDCJWTVerifierPreservesCancellationAndCannotSerialize(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	source := &oidcJWTKeysetSource{snapshot: oidcJWTKeysetSnapshot(
		t, 1, time.Now().Add(time.Hour), &privateKey.PublicKey, "lifecycle-key-1",
	)}
	verifier := newReloadableOIDCJWTVerifier(t, source)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := verifier.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled refresh = %v", err)
	}
	if _, err := verifier.Verify(ctx, []byte("token-with-at-least-thirty-two-bytes")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled verification = %v", err)
	}
	if _, err := json.Marshal(verifier); err == nil {
		t.Fatal("reloadable OIDC JWT verifier serialized")
	}
}

type oidcJWTKeysetSource struct {
	mu       sync.Mutex
	snapshot authn.OIDCJWTKeysetSnapshot
	err      error
	loads    int
}

func (source *oidcJWTKeysetSource) Load(
	ctx context.Context,
) (authn.OIDCJWTKeysetSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return authn.OIDCJWTKeysetSnapshot{}, err
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	source.loads++
	if source.err != nil {
		return authn.OIDCJWTKeysetSnapshot{}, source.err
	}
	snapshot := source.snapshot
	snapshot.JWKS = append([]byte(nil), snapshot.JWKS...)
	return snapshot, nil
}

func (source *oidcJWTKeysetSource) loadCount() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.loads
}

func (source *oidcJWTKeysetSource) setError(err error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.err = err
}

func (source *oidcJWTKeysetSource) setSnapshot(snapshot authn.OIDCJWTKeysetSnapshot) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.snapshot = snapshot
}

func oidcJWTKeysetSnapshot(
	t *testing.T,
	sequence uint64,
	expiresAt time.Time,
	key *rsa.PublicKey,
	keyID string,
) authn.OIDCJWTKeysetSnapshot {
	t.Helper()
	return authn.OIDCJWTKeysetSnapshot{
		Sequence:               sequence,
		ProviderID:             "primary",
		ProviderRegistrySHA256: strings.Repeat("1", 64),
		JWKS: marshalOIDCJWKS(t, []jose.JSONWebKey{
			oidcJWTJWK(key, jose.RS256, keyID),
		}),
		ExpiresAt: expiresAt,
	}
}

func newReloadableOIDCJWTVerifier(
	t *testing.T,
	source authn.OIDCJWTKeysetSource,
) *authn.ReloadableOIDCJWTVerifier {
	t.Helper()
	verifier, err := authn.NewReloadableOIDCJWTVerifier(
		context.Background(),
		reloadableOIDCJWTConfig(source),
	)
	if err != nil {
		t.Fatalf("create reloadable OIDC JWT verifier: %v", err)
	}
	return verifier
}

func reloadableOIDCJWTConfig(source authn.OIDCJWTKeysetSource) authn.ReloadableOIDCJWTConfig {
	return authn.ReloadableOIDCJWTConfig{
		Issuer:                 testOIDCIssuer,
		Audience:               testOIDCAudience,
		ProviderID:             "primary",
		ProviderRegistrySHA256: strings.Repeat("1", 64),
		Algorithms:             []string{"RS256"},
		ClockSkew:              30 * time.Second,
		MaximumLifetime:        time.Hour,
		Source:                 source,
	}
}
