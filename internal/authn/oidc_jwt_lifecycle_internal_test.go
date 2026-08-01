package authn

import (
	"context"
	"errors"
	"testing"
	"time"
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

type lifecycleTokenVerifier struct {
	called bool
}

type blockingLifecycleTokenVerifier struct {
	started chan struct{}
	release chan struct{}
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
