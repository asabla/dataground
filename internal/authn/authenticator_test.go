package authn_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/authn"
)

const (
	testToken  = "development-token-with-at-least-thirty-two-bytes"
	testActor  = "usr_00000000000000000001"
	testDomain = "iso_00000000000000000001"
)

func TestDevelopmentAuthenticatorConsumesConfigurationAndReturnsOwnedPrincipal(t *testing.T) {
	t.Parallel()

	token := []byte(testToken)
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{
		BearerToken: token, PrincipalID: testActor, IsolationDomainID: testDomain,
	})
	if err != nil {
		t.Fatalf("create development authenticator: %v", err)
	}
	for _, value := range token {
		if value != 0 {
			t.Fatal("development authenticator retained caller token bytes")
		}
	}

	principal, err := authenticator.Authenticate(context.Background(), []byte(testToken))
	if err != nil {
		t.Fatalf("authenticate development token: %v", err)
	}
	if principal.ID() != testActor || principal.Kind() != authn.PrincipalHuman {
		t.Fatalf("unexpected principal: %q %q", principal.ID(), principal.Kind())
	}
	if !principal.AllowsIsolationDomain(testDomain) {
		t.Fatal("principal lost its configured isolation domain")
	}
	if principal.AllowsIsolationDomain("iso_00000000000000000002") {
		t.Fatal("principal gained an unconfigured isolation domain")
	}

	replayed, err := authenticator.Authenticate(context.Background(), []byte(testToken))
	if err != nil || replayed.ID() != principal.ID() {
		t.Fatalf("development authentication was not stable: %v", err)
	}
}

func TestDevelopmentAuthenticatorRejectsInvalidAndCancelledCredentials(t *testing.T) {
	t.Parallel()

	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{
		BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain,
	})
	if err != nil {
		t.Fatalf("create development authenticator: %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), []byte("different-development-token-with-thirty-two-bytes")); !errors.Is(err, authn.ErrInvalidCredential) {
		t.Fatalf("expected invalid credential, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authenticator.Authenticate(ctx, []byte(testToken)); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestDevelopmentAuthenticationStateCannotSerialize(t *testing.T) {
	t.Parallel()

	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{
		BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain,
	})
	if err != nil {
		t.Fatalf("create development authenticator: %v", err)
	}
	principal, err := authenticator.Authenticate(context.Background(), []byte(testToken))
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if _, err := json.Marshal(principal); err == nil {
		t.Fatal("principal serialized")
	}
	if _, err := json.Marshal(authenticator); err == nil {
		t.Fatal("authenticator serialized")
	}
}
