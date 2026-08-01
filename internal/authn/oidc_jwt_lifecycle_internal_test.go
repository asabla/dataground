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

type lifecycleTokenVerifier struct {
	called bool
}

func (verifier *lifecycleTokenVerifier) Verify(
	context.Context,
	[]byte,
) (VerifiedOIDCToken, error) {
	verifier.called = true
	return VerifiedOIDCToken{}, nil
}
