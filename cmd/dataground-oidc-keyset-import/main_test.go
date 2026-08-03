package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	jose "github.com/go-jose/go-jose/v4"
)

func TestRunImportsSelectedAuthenticatedOIDCProvider(t *testing.T) {
	t.Parallel()

	issuer := "https://issuer.example.test/realm"
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       &privateKey.PublicKey,
		KeyID:     "provider-key",
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			if request.Header.Get("Authorization") != "Bearer discovery-secret" {
				http.Error(response, "unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]string{
				"issuer":   issuer,
				"jwks_uri": "https://" + request.Host + "/jwks",
			})
		case "/jwks":
			if request.Header.Get("Authorization") != "Bearer jwks-secret" {
				http.Error(response, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = response.Write(jwks)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	directory := ownerOnlyOIDCTestDirectory(t)
	discoveryCredential := filepath.Join(directory, "discovery-credential.json")
	jwksCredential := filepath.Join(directory, "jwks-credential.json")
	registryPath, registryDigest := writeOIDCProviderRegistry(t, directory, []map[string]any{{
		"id":           "primary",
		"issuer":       issuer,
		"discoveryUrl": server.URL + "/.well-known/openid-configuration",
		"jwksUrl":      server.URL + "/jwks",
		"algorithms":   []string{"RS256"},
		"discoveryAuthentication": map[string]any{
			"kind": "bearer-credential-file", "credentialFile": discoveryCredential,
		},
		"jwksAuthentication": map[string]any{
			"kind": "bearer-credential-file", "credentialFile": jwksCredential,
		},
	}})
	publishOIDCProviderCredentialForImport(
		t, discoveryCredential, registryDigest, "discovery", "discovery-secret",
	)
	publishOIDCProviderCredentialForImport(t, jwksCredential, registryDigest, "jwks", "jwks-secret")
	publicationPath := filepath.Join(directory, "keysets.json")
	requestPath := writeOIDCKeysetImportRequest(
		t, directory, "primary", registryPath, registryDigest, publicationPath,
	)
	if err := runWithTransport(
		context.Background(),
		[]string{"-request-file", requestPath},
		server.Client().Transport.(*http.Transport),
	); err != nil {
		t.Fatal(err)
	}
	source, err := authn.NewOIDCJWTKeysetFileSource(publicationPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer clear(snapshot.JWKS)
	if snapshot.Sequence != 1 || snapshot.ProviderID != "primary" ||
		snapshot.ProviderRegistrySHA256 != registryDigest {
		t.Fatalf("published binding = %#v", snapshot)
	}
	if _, err := authn.NewPinnedOIDCJWTVerifier(authn.PinnedOIDCJWTConfig{
		Issuer:          issuer,
		Audience:        authn.APIAudience,
		Algorithms:      []string{"RS256"},
		JWKS:            snapshot.JWKS,
		ClockSkew:       30 * time.Second,
		MaximumLifetime: time.Hour,
	}); err != nil {
		t.Fatalf("published verifier: %v", err)
	}
}

func TestLoadOIDCProviderProfileRejectsRegistryDriftAndFallback(t *testing.T) {
	t.Parallel()

	directory := ownerOnlyOIDCTestDirectory(t)
	profile := testOIDCProviderProfile("primary")
	registryPath, registryDigest := writeOIDCProviderRegistry(t, directory, []map[string]any{profile})
	publicationPath := filepath.Join(directory, "keysets.json")

	t.Run("unknown provider", func(t *testing.T) {
		requestPath := writeOIDCKeysetImportRequest(
			t, directory, "fallback", registryPath, registryDigest, publicationPath,
		)
		request, err := readOIDCKeysetImportRequest(requestPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := loadOIDCProviderProfile(request); err == nil {
			t.Fatal("unregistered provider was accepted")
		}
	})

	t.Run("digest drift", func(t *testing.T) {
		requestPath := writeOIDCKeysetImportRequest(
			t, directory, "primary", registryPath, strings.Repeat("0", sha256.Size*2), publicationPath,
		)
		request, err := readOIDCKeysetImportRequest(requestPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := loadOIDCProviderProfile(request); err == nil {
			t.Fatal("registry digest drift was accepted")
		}
	})

	t.Run("duplicate profile", func(t *testing.T) {
		duplicateDirectory := ownerOnlyOIDCTestDirectory(t)
		path, digest := writeOIDCProviderRegistry(
			t, duplicateDirectory, []map[string]any{profile, profile},
		)
		requestPath := writeOIDCKeysetImportRequest(
			t, duplicateDirectory, "primary", path, digest,
			filepath.Join(duplicateDirectory, "keysets.json"),
		)
		request, err := readOIDCKeysetImportRequest(requestPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := loadOIDCProviderProfile(request); err == nil {
			t.Fatal("duplicate provider profile was accepted")
		}
	})

	t.Run("null credential reference", func(t *testing.T) {
		nullDirectory := ownerOnlyOIDCTestDirectory(t)
		nullProfile := testOIDCProviderProfile("primary")
		nullProfile["discoveryAuthentication"] = map[string]any{
			"kind": "none", "credentialFile": nil,
		}
		path, digest := writeOIDCProviderRegistry(t, nullDirectory, []map[string]any{nullProfile})
		requestPath := writeOIDCKeysetImportRequest(
			t, nullDirectory, "primary", path, digest,
			filepath.Join(nullDirectory, "keysets.json"),
		)
		request, err := readOIDCKeysetImportRequest(requestPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := loadOIDCProviderProfile(request); err == nil {
			t.Fatal("null provider credential reference was accepted")
		}
	})

	t.Run("legacy raw token profile", func(t *testing.T) {
		legacyDirectory := ownerOnlyOIDCTestDirectory(t)
		legacyProfile := testOIDCProviderProfile("primary")
		legacyProfile["discoveryAuthentication"] = map[string]any{
			"kind": "bearer-token-file", "tokenFile": filepath.Join(legacyDirectory, "token"),
		}
		path, digest := writeOIDCProviderRegistry(t, legacyDirectory, []map[string]any{legacyProfile})
		requestPath := writeOIDCKeysetImportRequest(
			t, legacyDirectory, "primary", path, digest, filepath.Join(legacyDirectory, "keysets.json"),
		)
		request, err := readOIDCKeysetImportRequest(requestPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := loadOIDCProviderProfile(request); err == nil {
			t.Fatal("legacy raw bearer-token profile was accepted")
		}
	})
}

func TestRunRejectsUnsafeOIDCProviderCredentials(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		token string
		mode  os.FileMode
	}{
		"group readable":   {token: "provider-secret", mode: 0o640},
		"header injection": {token: "provider-secret\nInjected", mode: 0o600},
		"empty":            {token: "", mode: 0o600},
	}
	for name, test := range tests {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			directory := ownerOnlyOIDCTestDirectory(t)
			credential := filepath.Join(directory, "credential.json")
			profile := testOIDCProviderProfile("primary")
			profile["discoveryAuthentication"] = map[string]any{
				"kind": "bearer-credential-file", "credentialFile": credential,
			}
			registryPath, digest := writeOIDCProviderRegistry(t, directory, []map[string]any{profile})
			content, err := json.Marshal(map[string]any{
				"contract": "dataground.oidc-provider-credential/v2", "generation": 1,
				"isolationDomainId": "iso_00000000000000000001",
				"providerId":        "primary", "providerRegistrySha256": digest, "endpoint": "discovery",
				"status": "active", "activatedAt": time.Now().UTC().Add(-time.Minute),
				"expiresAt": time.Now().UTC().Add(time.Hour), "bearerToken": test.token,
			})
			if err != nil {
				t.Fatal(err)
			}
			writeOIDCKeysetImportFile(t, directory, "credential.json", string(content), test.mode)
			requestPath := writeOIDCKeysetImportRequest(
				t, directory, "primary", registryPath, digest, filepath.Join(directory, "keysets.json"),
			)
			if err := run(context.Background(), []string{"-request-file", requestPath}); err == nil {
				t.Fatal("unsafe provider credential was accepted")
			}
		})
	}
}

func TestRunRejectsRevokedOIDCProviderCredentialBeforeProviderAccess(t *testing.T) {
	t.Parallel()
	directory := ownerOnlyOIDCTestDirectory(t)
	credential := filepath.Join(directory, "credential.json")
	profile := testOIDCProviderProfile("primary")
	profile["discoveryAuthentication"] = map[string]any{
		"kind": "bearer-credential-file", "credentialFile": credential,
	}
	registryPath, digest := writeOIDCProviderRegistry(t, directory, []map[string]any{profile})
	publishOIDCProviderCredentialForImport(t, credential, digest, "discovery", "provider-secret")
	if err := authn.PublishOIDCProviderCredential(context.Background(), authn.OIDCProviderCredentialPublication{
		Path: credential, IsolationDomainID: "iso_00000000000000000001",
		Generation: 2, ProviderID: "primary", ProviderRegistrySHA256: digest,
		Endpoint: "discovery", RevokedAt: time.Now().UTC(), Revoked: true,
	}); err != nil {
		t.Fatal(err)
	}
	requestPath := writeOIDCKeysetImportRequest(
		t, directory, "primary", registryPath, digest, filepath.Join(directory, "keysets.json"),
	)
	if err := run(context.Background(), []string{"-request-file", requestPath}); err == nil ||
		!strings.Contains(err.Error(), "credential is unavailable") {
		t.Fatalf("revoked import error = %v", err)
	}
}

func TestLoadOIDCProviderProfileRejectsSharedEndpointCredential(t *testing.T) {
	t.Parallel()

	directory := ownerOnlyOIDCTestDirectory(t)
	credential := filepath.Join(directory, "shared.json")
	profile := testOIDCProviderProfile("primary")
	authentication := map[string]any{"kind": "bearer-credential-file", "credentialFile": credential}
	profile["discoveryAuthentication"] = authentication
	profile["jwksAuthentication"] = authentication
	registryPath, digest := writeOIDCProviderRegistry(t, directory, []map[string]any{profile})
	requestPath := writeOIDCKeysetImportRequest(
		t, directory, "primary", registryPath, digest, filepath.Join(directory, "keysets.json"),
	)
	request, err := readOIDCKeysetImportRequest(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadOIDCProviderProfile(request); err == nil {
		t.Fatal("one credential publication was accepted for two endpoints")
	}
}

func TestReadOIDCKeysetImportRequestRejectsDuplicateMembers(t *testing.T) {
	t.Parallel()

	path := filepath.Join(ownerOnlyOIDCTestDirectory(t), "request.json")
	content := []byte(`{"contract":"dataground.oidc-keyset-import/oidc-discovery/v4","sequence":1,"sequence":2}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOIDCKeysetImportRequest(path); err == nil {
		t.Fatal("duplicate request member was accepted")
	}
}

func TestReadOIDCKeysetImportRequestRejectsPathCollisions(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"providerRegistryFile", "publicationFile"} {
		field := field
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			directory := ownerOnlyOIDCTestDirectory(t)
			requestPath := filepath.Join(directory, "request.json")
			request := map[string]any{
				"contract":               oidcKeysetImportRequestContract,
				"isolationDomainId":      "iso_00000000000000000001",
				"providerId":             "primary",
				"providerRegistryFile":   filepath.Join(directory, "providers.json"),
				"providerRegistrySha256": strings.Repeat("0", sha256.Size*2),
				"sequence":               1,
				"expiresAt":              time.Now().UTC().Add(time.Hour).Truncate(time.Second),
				"publicationFile":        filepath.Join(directory, "keysets.json"),
			}
			request[field] = requestPath
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(requestPath, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readOIDCKeysetImportRequest(requestPath); err == nil {
				t.Fatalf("%s may replace the request", field)
			}
		})
	}
}

func testOIDCProviderProfile(id string) map[string]any {
	return map[string]any{
		"id":                      id,
		"issuer":                  "https://issuer.example.test/realm",
		"discoveryUrl":            "https://issuer.example.test/realm/.well-known/openid-configuration",
		"jwksUrl":                 "https://issuer.example.test/realm/jwks",
		"algorithms":              []string{"RS256"},
		"discoveryAuthentication": map[string]any{"kind": "none"},
		"jwksAuthentication":      map[string]any{"kind": "none"},
	}
}

func writeOIDCProviderRegistry(
	t *testing.T,
	directory string,
	profiles []map[string]any,
) (string, string) {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"contract": oidcProviderRegistryContract,
		"profiles": profiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := writeOIDCKeysetImportFile(t, directory, "providers.json", string(encoded), 0o600)
	digest := sha256.Sum256(encoded)
	return path, fmt.Sprintf("%x", digest)
}

func writeOIDCKeysetImportRequest(
	t *testing.T,
	directory string,
	providerID string,
	registryPath string,
	registryDigest string,
	publicationPath string,
) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"contract":               oidcKeysetImportRequestContract,
		"isolationDomainId":      "iso_00000000000000000001",
		"providerId":             providerID,
		"providerRegistryFile":   registryPath,
		"providerRegistrySha256": registryDigest,
		"sequence":               1,
		"expiresAt":              time.Now().UTC().Add(time.Hour).Truncate(time.Second),
		"publicationFile":        publicationPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	return writeOIDCKeysetImportFile(t, directory, providerID+"-request.json", string(encoded), 0o600)
}

func writeOIDCKeysetImportFile(
	t *testing.T,
	directory string,
	name string,
	content string,
	mode os.FileMode,
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

func publishOIDCProviderCredentialForImport(
	t *testing.T, path, registryDigest, endpoint, token string,
) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	if err := authn.PublishOIDCProviderCredential(context.Background(), authn.OIDCProviderCredentialPublication{
		Path: path, IsolationDomainID: "iso_00000000000000000001",
		Generation: 1, ProviderID: "primary", ProviderRegistrySHA256: registryDigest,
		Endpoint: endpoint, ActivatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		BearerToken: []byte(token),
	}); err != nil {
		t.Fatal(err)
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
