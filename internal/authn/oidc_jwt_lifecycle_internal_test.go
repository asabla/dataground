package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestReloadableOIDCJWTVerifierFailsClosedAfterSnapshotExpiry(t *testing.T) {
	delegate := &lifecycleTokenVerifier{}
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	verifier := &ReloadableOIDCJWTVerifier{
		now:       func() time.Time { return now },
		verifier:  delegate,
		expiresAt: now,
	}
	if _, err := verifier.Verify(
		context.Background(),
		[]byte("token-with-at-least-thirty-two-bytes"),
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expired snapshot verification = %v", err)
	}
	if delegate.called {
		t.Fatal("expired snapshot reached pinned verifier")
	}
}

func TestReloadableOIDCJWTVerifierHoldsGenerationThroughVerification(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	delegate := &blockingLifecycleTokenVerifier{started: started, release: release}
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	verifier := &ReloadableOIDCJWTVerifier{
		now:       func() time.Time { return now },
		verifier:  delegate,
		expiresAt: now.Add(time.Hour),
	}
	verified := make(chan error, 1)
	go func() {
		_, err := verifier.Verify(
			context.Background(),
			[]byte("token-with-at-least-thirty-two-bytes"),
		)
		verified <- err
	}()
	<-started
	if verifier.mu.TryLock() {
		verifier.mu.Unlock()
		t.Fatal("keyset generation changed during verification")
	}
	close(release)
	if err := <-verified; err != nil {
		t.Fatalf("verification = %v", err)
	}
	if !verifier.mu.TryLock() {
		t.Fatal("keyset generation remained locked after verification")
	}
	verifier.mu.Unlock()
}

func TestReloadableOIDCJWTVerifierDoesNotInstallAfterCancellation(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: &privateKey.PublicKey, KeyID: "lifecycle-key-1", Algorithm: "RS256", Use: "sig",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	verifier := &ReloadableOIDCJWTVerifier{
		issuer:                 "https://identity.example.invalid",
		audience:               APIAudience,
		providerID:             "primary",
		providerRegistrySHA256: strings.Repeat("1", 64),
		algorithms:             []string{"RS256"},
		clockSkew:              30 * time.Second,
		maximumLifetime:        time.Hour,
		source: lifecycleKeysetSource{snapshot: OIDCJWTKeysetSnapshot{
			Sequence: 1, ProviderID: "primary", ProviderRegistrySHA256: strings.Repeat("1", 64),
			JWKS: jwks, ExpiresAt: now.Add(time.Hour),
		}},
		now: func() time.Time {
			cancel()
			return now
		},
	}
	if err := verifier.Refresh(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled refresh = %v", err)
	}
	if verifier.sequence != 0 || verifier.verifier != nil {
		t.Fatal("cancelled refresh installed a keyset")
	}
}

type lifecycleTokenVerifier struct {
	called bool
}

type blockingLifecycleTokenVerifier struct {
	started chan struct{}
	release chan struct{}
}

type lifecycleKeysetSource struct {
	snapshot OIDCJWTKeysetSnapshot
}

func (source lifecycleKeysetSource) Load(context.Context) (OIDCJWTKeysetSnapshot, error) {
	snapshot := source.snapshot
	snapshot.JWKS = append([]byte(nil), snapshot.JWKS...)
	return snapshot, nil
}

func (verifier *blockingLifecycleTokenVerifier) Verify(
	context.Context,
	[]byte,
) (VerifiedOIDCToken, error) {
	close(verifier.started)
	<-verifier.release
	return VerifiedOIDCToken{}, nil
}

func (verifier *lifecycleTokenVerifier) Verify(
	context.Context,
	[]byte,
) (VerifiedOIDCToken, error) {
	verifier.called = true
	return VerifiedOIDCToken{}, nil
}
