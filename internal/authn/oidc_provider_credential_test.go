package authn

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOIDCProviderCredentialPublicationLifecycle(t *testing.T) {
	t.Parallel()
	directory := ownerOnlyOIDCTestDirectory(t)
	path := filepath.Join(directory, "provider-credential.json")
	now := time.Now().UTC().Truncate(time.Second)
	binding := strings.Repeat("1", 64)
	domainID := "iso_00000000000000000001"
	active := OIDCProviderCredentialPublication{
		Path: path, IsolationDomainID: domainID, Generation: 1,
		ProviderID: "primary", ProviderRegistrySHA256: binding,
		Endpoint: "jwks", ActivatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		BearerToken: []byte("first-secret"),
	}
	if err := PublishOIDCProviderCredential(context.Background(), active); err != nil {
		t.Fatalf("publish active credential: %v", err)
	}
	if err := PublishOIDCProviderCredential(context.Background(), active); err != nil {
		t.Fatalf("replay active credential: %v", err)
	}
	token, err := LoadOIDCProviderBearerCredential(context.Background(), path, domainID, "primary", binding, "jwks")
	if err != nil {
		t.Fatalf("load active credential: %v", err)
	}
	if string(token) != "first-secret" {
		t.Fatalf("token = %q", token)
	}
	clear(token)

	revoked := OIDCProviderCredentialPublication{
		Path: path, IsolationDomainID: domainID, Generation: 2,
		ProviderID: "primary", ProviderRegistrySHA256: binding,
		Endpoint: "jwks", RevokedAt: now, Revoked: true,
	}
	if err := PublishOIDCProviderCredential(context.Background(), revoked); err != nil {
		t.Fatalf("revoke credential: %v", err)
	}
	if _, err := LoadOIDCProviderBearerCredential(
		context.Background(), path, domainID, "primary", binding, "jwks",
	); !errors.Is(err, ErrOIDCProviderCredentialRevoked) {
		t.Fatalf("revoked load error = %v", err)
	}

	rotated := active
	rotated.Generation = 3
	rotated.BearerToken = []byte("rotated-secret")
	rotated.ExpiresAt = now.Add(2 * time.Hour)
	if err := PublishOIDCProviderCredential(context.Background(), rotated); err != nil {
		t.Fatalf("reactivate credential: %v", err)
	}
	token, err = LoadOIDCProviderBearerCredential(context.Background(), path, domainID, "primary", binding, "jwks")
	if err != nil || string(token) != "rotated-secret" {
		t.Fatalf("rotated token = %q, err = %v", token, err)
	}
	clear(token)
}

func TestOIDCProviderCredentialPublicationRejectsGenerationAndBindingDrift(t *testing.T) {
	t.Parallel()
	path := filepath.Join(ownerOnlyOIDCTestDirectory(t), "provider-credential.json")
	now := time.Now().UTC().Truncate(time.Second)
	publication := OIDCProviderCredentialPublication{
		Path: path, IsolationDomainID: "iso_00000000000000000001", Generation: 1,
		ProviderID: "primary", ProviderRegistrySHA256: strings.Repeat("1", 64),
		Endpoint: "discovery", ActivatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		BearerToken: []byte("provider-secret"),
	}
	if err := PublishOIDCProviderCredential(context.Background(), publication); err != nil {
		t.Fatal(err)
	}
	conflict := publication
	conflict.BearerToken = []byte("different-secret")
	if err := PublishOIDCProviderCredential(context.Background(), conflict); !errors.Is(err, ErrOIDCProviderCredentialConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	gap := publication
	gap.Generation = 3
	if err := PublishOIDCProviderCredential(context.Background(), gap); !errors.Is(err, ErrOIDCProviderCredentialRollback) {
		t.Fatalf("gap error = %v", err)
	}
	wrongEndpoint := publication
	wrongEndpoint.Generation = 2
	wrongEndpoint.Endpoint = "jwks"
	if err := PublishOIDCProviderCredential(context.Background(), wrongEndpoint); !errors.Is(err, ErrOIDCProviderCredentialConflict) {
		t.Fatalf("endpoint repurpose error = %v", err)
	}
	if _, err := LoadOIDCProviderBearerCredential(
		context.Background(), path, publication.IsolationDomainID,
		"secondary", publication.ProviderRegistrySHA256, "discovery",
	); !errors.Is(err, ErrOIDCProviderCredentialInvalid) {
		t.Fatalf("provider drift error = %v", err)
	}
	if _, err := LoadOIDCProviderBearerCredential(
		context.Background(), path, publication.IsolationDomainID,
		"primary", publication.ProviderRegistrySHA256, "jwks",
	); !errors.Is(err, ErrOIDCProviderCredentialInvalid) {
		t.Fatalf("endpoint drift error = %v", err)
	}
	if _, err := LoadOIDCProviderBearerCredential(
		context.Background(), path, "iso_00000000000000000002",
		"primary", publication.ProviderRegistrySHA256, "discovery",
	); !errors.Is(err, ErrOIDCProviderCredentialInvalid) {
		t.Fatalf("isolation-domain drift error = %v", err)
	}
}

func TestOIDCProviderCredentialRejectsExpiredAndUnsafeFiles(t *testing.T) {
	t.Parallel()
	directory := ownerOnlyOIDCTestDirectory(t)
	path := filepath.Join(directory, "expired.json")
	now := time.Now().UTC().Truncate(time.Second)
	publication := OIDCProviderCredentialPublication{
		Path: path, IsolationDomainID: "iso_00000000000000000001", Generation: 1,
		ProviderID: "primary", ProviderRegistrySHA256: strings.Repeat("1", 64),
		Endpoint: "jwks", ActivatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
		BearerToken: []byte("provider-secret"),
	}
	if err := PublishOIDCProviderCredential(context.Background(), publication); !errors.Is(err, ErrOIDCProviderCredentialInvalid) {
		t.Fatalf("expired publication error = %v", err)
	}
	unsafe := filepath.Join(directory, "unsafe.json")
	if err := os.WriteFile(unsafe, []byte(`{"contract":"dataground.oidc-provider-credential/v2"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOIDCProviderBearerCredential(
		context.Background(), unsafe, publication.IsolationDomainID,
		"primary", strings.Repeat("1", 64), "jwks",
	); !errors.Is(err, ErrOIDCProviderCredentialInvalid) {
		t.Fatalf("unsafe file error = %v", err)
	}
}

func ownerOnlyOIDCTestDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestOIDCProviderCredentialPublicationCannotBeSerialized(t *testing.T) {
	t.Parallel()
	publication := OIDCProviderCredentialPublication{BearerToken: []byte("must-not-serialize")}
	if encoded, err := json.Marshal(publication); err == nil || len(encoded) != 0 {
		t.Fatalf("serialized credential publication = %q, err = %v", encoded, err)
	}
}
