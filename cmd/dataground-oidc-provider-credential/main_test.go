package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
)

func TestRunActivatesAndRevokesOIDCProviderCredential(t *testing.T) {
	t.Parallel()
	directory := ownerOnlyOIDCTestDirectory(t)
	publication := filepath.Join(directory, "credential.json")
	token := writeOIDCProviderCredentialCommandFile(t, directory, "token", "provider-secret", 0o600)
	activate := writeOIDCProviderCredentialCommandRequest(t, directory, "activate.json", map[string]any{
		"contract": oidcProviderCredentialRequestContract, "operation": "activate", "generation": 1,
		"providerId": "primary", "providerRegistrySha256": strings.Repeat("1", 64), "endpoint": "jwks",
		"activatedAt":     time.Now().UTC().Add(-time.Minute).Truncate(time.Second),
		"expiresAt":       time.Now().UTC().Add(time.Hour).Truncate(time.Second),
		"bearerTokenFile": token, "publicationFile": publication,
	})
	if err := run(context.Background(), []string{"-request-file", activate}); err != nil {
		t.Fatalf("activate credential: %v", err)
	}
	loaded, err := authn.LoadOIDCProviderBearerCredential(
		context.Background(), publication, "primary", strings.Repeat("1", 64), "jwks",
	)
	if err != nil || string(loaded) != "provider-secret" {
		t.Fatalf("loaded token = %q, err = %v", loaded, err)
	}
	clear(loaded)

	revoke := writeOIDCProviderCredentialCommandRequest(t, directory, "revoke.json", map[string]any{
		"contract": oidcProviderCredentialRequestContract, "operation": "revoke", "generation": 2,
		"providerId": "primary", "providerRegistrySha256": strings.Repeat("1", 64), "endpoint": "jwks",
		"revokedAt":       time.Now().UTC().Truncate(time.Second),
		"publicationFile": publication,
	})
	if err := run(context.Background(), []string{"-request-file", revoke}); err != nil {
		t.Fatalf("revoke credential: %v", err)
	}
	if _, err := authn.LoadOIDCProviderBearerCredential(
		context.Background(), publication, "primary", strings.Repeat("1", 64), "jwks",
	); !errors.Is(err, authn.ErrOIDCProviderCredentialRevoked) {
		t.Fatalf("revoked credential error = %v", err)
	}
}

func TestReadOIDCProviderCredentialRequestRejectsAmbiguousOperations(t *testing.T) {
	t.Parallel()
	directory := ownerOnlyOIDCTestDirectory(t)
	publication := filepath.Join(directory, "credential.json")
	token := writeOIDCProviderCredentialCommandFile(t, directory, "token", "provider-secret", 0o600)
	base := map[string]any{
		"contract": oidcProviderCredentialRequestContract, "operation": "activate", "generation": 1,
		"providerId": "primary", "providerRegistrySha256": strings.Repeat("1", 64), "endpoint": "discovery",
		"activatedAt":     time.Now().UTC().Add(-time.Minute).Truncate(time.Second),
		"expiresAt":       time.Now().UTC().Add(time.Hour).Truncate(time.Second),
		"bearerTokenFile": token, "publicationFile": publication,
	}
	for name, mutate := range map[string]func(map[string]any){
		"null token": func(value map[string]any) { value["bearerTokenFile"] = nil },
		"revoke with token": func(value map[string]any) {
			value["operation"] = "revoke"
			value["revokedAt"] = time.Now().UTC()
		},
		"unknown operation": func(value map[string]any) { value["operation"] = "issue" },
		"path collision":    func(value map[string]any) { value["publicationFile"] = token },
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			value := make(map[string]any, len(base))
			for key, member := range base {
				value[key] = member
			}
			mutate(value)
			path := writeOIDCProviderCredentialCommandRequest(
				t, ownerOnlyOIDCTestDirectory(t), "request.json", value,
			)
			if _, err := readOIDCProviderCredentialRequest(path); err == nil {
				t.Fatal("ambiguous request was accepted")
			}
		})
	}
}

func TestReadOIDCProviderCredentialRequestRejectsDuplicateAndReadableInput(t *testing.T) {
	t.Parallel()
	directory := ownerOnlyOIDCTestDirectory(t)
	path := writeOIDCProviderCredentialCommandFile(
		t, directory, "duplicate.json",
		`{"contract":"dataground.oidc-provider-credential-request/v1","generation":1,"generation":2}`,
		0o600,
	)
	if _, err := readOIDCProviderCredentialRequest(path); err == nil {
		t.Fatal("duplicate request member was accepted")
	}
	readable := writeOIDCProviderCredentialCommandFile(t, directory, "readable.json", `{}`, 0o644)
	if _, err := readOIDCProviderCredentialRequest(readable); err == nil {
		t.Fatal("group-readable request was accepted")
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

func writeOIDCProviderCredentialCommandRequest(t *testing.T, directory, name string, value map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return writeOIDCProviderCredentialCommandFile(t, directory, name, string(encoded), 0o600)
}

func writeOIDCProviderCredentialCommandFile(
	t *testing.T, directory, name, content string, mode os.FileMode,
) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}
