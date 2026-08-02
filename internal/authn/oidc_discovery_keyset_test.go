package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
)

func TestOIDCDiscoveryKeysetImporterPinsMetadataAndCanonicalizesKeys(t *testing.T) {
	t.Parallel()

	issuer := "https://issuer.example.test/realm"
	jwks := testOIDCDiscoveryJWKS(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"issuer":           issuer,
				"jwks_uri":         "https://" + request.Host + "/jwks",
				"scopes_supported": []string{"openid"},
			})
		case "/jwks":
			_, _ = response.Write(jwks)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	importer, err := NewOIDCDiscoveryKeysetImporter(OIDCDiscoveryKeysetImportConfig{
		Issuer:       issuer,
		DiscoveryURL: server.URL + "/.well-known/openid-configuration",
		JWKSURL:      server.URL + "/jwks",
		Algorithms:   []string{"RS256"},
		Transport:    server.Client().Transport.(*http.Transport),
	})
	if err != nil {
		t.Fatal(err)
	}
	imported, err := importer.Import(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer clear(imported)
	if _, err := parseOIDCJWKS(imported, map[string]struct{}{"RS256": {}}); err != nil {
		t.Fatalf("parse imported JWKS: %v", err)
	}
}

func TestOIDCDiscoveryKeysetImporterRejectsMetadataEndpointChange(t *testing.T) {
	t.Parallel()

	issuer := "https://issuer.example.test"
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"issuer":"https://issuer.example.test","jwks_uri":"https://other.example.test/jwks"}`))
	}))
	defer server.Close()
	importer, err := NewOIDCDiscoveryKeysetImporter(OIDCDiscoveryKeysetImportConfig{
		Issuer:       issuer,
		DiscoveryURL: server.URL + "/.well-known/openid-configuration",
		JWKSURL:      server.URL + "/jwks",
		Algorithms:   []string{"RS256"},
		Transport:    server.Client().Transport.(*http.Transport),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(context.Background()); !errors.Is(err, ErrOIDCDiscoveryInvalid) {
		t.Fatalf("metadata error = %v", err)
	}
}

func TestOIDCDiscoveryKeysetImporterDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var followed atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		followed.Store(true)
	}))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL+"/metadata", http.StatusFound)
	}))
	defer server.Close()
	importer, err := NewOIDCDiscoveryKeysetImporter(OIDCDiscoveryKeysetImportConfig{
		Issuer:       "https://issuer.example.test",
		DiscoveryURL: server.URL + "/.well-known/openid-configuration",
		JWKSURL:      server.URL + "/jwks",
		Algorithms:   []string{"RS256"},
		Transport:    server.Client().Transport.(*http.Transport),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importer.Import(context.Background()); !errors.Is(err, ErrOIDCDiscoveryUnavailable) {
		t.Fatalf("redirect error = %v", err)
	}
	if followed.Load() {
		t.Fatal("redirect target was contacted")
	}
}

func TestOIDCDiscoveryKeysetImporterEnforcesTLSMinimum(t *testing.T) {
	t.Parallel()

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS10}
	importer, err := NewOIDCDiscoveryKeysetImporter(OIDCDiscoveryKeysetImportConfig{
		Issuer:       "https://issuer.example.test",
		DiscoveryURL: "https://issuer.example.test/.well-known/openid-configuration",
		JWKSURL:      "https://issuer.example.test/jwks",
		Algorithms:   []string{"RS256"},
		Transport:    transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	configured := importer.client.Transport.(*http.Transport)
	if configured.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS minimum = %d, want TLS 1.2", configured.TLSClientConfig.MinVersion)
	}
	if transport.TLSClientConfig.MinVersion != tls.VersionTLS10 {
		t.Fatal("caller transport was modified")
	}
}

func testOIDCDiscoveryJWKS(t *testing.T) []byte {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       &privateKey.PublicKey,
		KeyID:     "provider-key",
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
