package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	jose "github.com/go-jose/go-jose/v4"
)

func TestRunPublishesStrictOIDCJWTKeysetRequest(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	jwksPath := writeOIDCJWTKeysetCommandFile(t, directory, "jwks.json", testOIDCJWTKeysetCommandJWKS(t), 0o600)
	publicationPath := filepath.Join(directory, "publication.json")
	requestPath := writeOIDCJWTKeysetCommandFile(
		t,
		directory,
		"request.json",
		testOIDCJWTKeysetCommandRequest(jwksPath, publicationPath, 7),
		0o600,
	)
	if err := run(context.Background(), []string{"-request-file", requestPath}); err != nil {
		t.Fatalf("publish keyset: %v", err)
	}
	source, err := authn.NewOIDCJWTKeysetFileSource(publicationPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Sequence != 7 {
		t.Fatalf("sequence = %d, want 7", snapshot.Sequence)
	}
	clear(snapshot.JWKS)
}

func TestReadOIDCJWTKeysetPublicationRequestRejectsUnsafeDocuments(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	jwksPath := writeOIDCJWTKeysetCommandFile(t, directory, "jwks.json", testOIDCJWTKeysetCommandJWKS(t), 0o600)
	publicationPath := filepath.Join(directory, "publication.json")
	valid := testOIDCJWTKeysetCommandRequest(jwksPath, publicationPath, 1)
	tests := map[string]string{
		"duplicate member":  strings.Replace(valid, `"sequence":1`, `"sequence":1,"sequence":2`, 1),
		"unknown member":    strings.Replace(valid, `"sequence":1`, `"sequence":1,"issuer":"untrusted"`, 1),
		"trailing data":     valid + `{}`,
		"wrong contract":    strings.Replace(valid, oidcJWTKeysetPublicationRequestContract, "unknown/v1", 1),
		"same input output": testOIDCJWTKeysetCommandRequest(jwksPath, jwksPath, 1),
	}
	for name, document := range tests {
		name, document := name, document
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := writeOIDCJWTKeysetCommandFile(t, t.TempDir(), "request.json", document, 0o600)
			if _, err := readOIDCJWTKeysetPublicationRequest(path); err == nil {
				t.Fatal("unsafe request was accepted")
			}
		})
	}
	if _, err := readOIDCJWTKeysetPublicationRequest(
		writeOIDCJWTKeysetCommandFile(t, directory, "readable.json", valid, 0o644),
	); err == nil {
		t.Fatal("group-readable request was accepted")
	}
	target := writeOIDCJWTKeysetCommandFile(t, directory, "target.json", valid, 0o600)
	symlink := filepath.Join(directory, "request-link.json")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readOIDCJWTKeysetPublicationRequest(symlink); err == nil {
		t.Fatal("request symlink was accepted")
	}
}

func TestRunRejectsMutableOrPrivateJWKSInput(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	publicationPath := filepath.Join(directory, "publication.json")
	mutable := writeOIDCJWTKeysetCommandFile(t, directory, "mutable.json", testOIDCJWTKeysetCommandJWKS(t), 0o622)
	request := writeOIDCJWTKeysetCommandFile(
		t,
		directory,
		"mutable-request.json",
		testOIDCJWTKeysetCommandRequest(mutable, publicationPath, 1),
		0o600,
	)
	if err := run(context.Background(), []string{"-request-file", request}); err == nil {
		t.Fatal("group-writable JWKS was accepted")
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateJWKS, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: privateKey, KeyID: "private", Algorithm: string(jose.RS256), Use: "sig",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	private := writeOIDCJWTKeysetCommandFile(t, directory, "private.json", string(privateJWKS), 0o600)
	privateRequest := writeOIDCJWTKeysetCommandFile(
		t,
		directory,
		"private-request.json",
		testOIDCJWTKeysetCommandRequest(private, publicationPath, 1),
		0o600,
	)
	if err := run(context.Background(), []string{"-request-file", privateRequest}); err == nil {
		t.Fatal("private JWKS was accepted")
	}
}

func testOIDCJWTKeysetCommandRequest(jwksPath, publicationPath string, sequence uint64) string {
	request := map[string]any{
		"contract":               oidcJWTKeysetPublicationRequestContract,
		"sequence":               sequence,
		"providerId":             "primary",
		"providerRegistrySha256": strings.Repeat("1", 64),
		"expiresAt":              time.Now().UTC().Add(time.Hour).Truncate(time.Second),
		"algorithms":             []string{"RS256"},
		"jwksFile":               jwksPath,
		"publicationFile":        publicationPath,
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func testOIDCJWTKeysetCommandJWKS(t *testing.T) string {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       &privateKey.PublicKey,
		KeyID:     "provider-key-1",
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func writeOIDCJWTKeysetCommandFile(
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
