package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	jose "github.com/go-jose/go-jose/v4"
)

func TestRunImportsAndPublishesPinnedOIDCKeyset(t *testing.T) {
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
			_ = json.NewEncoder(response).Encode(map[string]string{
				"issuer":   issuer,
				"jwks_uri": "https://" + request.Host + "/jwks",
			})
		case "/jwks":
			_, _ = response.Write(jwks)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	directory := t.TempDir()
	publicationPath := filepath.Join(directory, "keysets.json")
	requestPath := filepath.Join(directory, "request.json")
	request := map[string]any{
		"contract":        oidcKeysetImportRequestContract,
		"issuer":          issuer,
		"discoveryUrl":    server.URL + "/.well-known/openid-configuration",
		"jwksUrl":         server.URL + "/jwks",
		"sequence":        1,
		"expiresAt":       time.Now().UTC().Add(time.Hour).Truncate(time.Second),
		"algorithms":      []string{"RS256"},
		"publicationFile": publicationPath,
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
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
	if snapshot.Sequence != 1 {
		t.Fatalf("published sequence = %d, want 1", snapshot.Sequence)
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

func TestReadOIDCKeysetImportRequestRejectsDuplicateMembers(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "request.json")
	content := []byte(`{"contract":"dataground.oidc-keyset-import/oidc-discovery/v1","sequence":1,"sequence":2}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOIDCKeysetImportRequest(path); err == nil {
		t.Fatal("duplicate request member was accepted")
	}
}
