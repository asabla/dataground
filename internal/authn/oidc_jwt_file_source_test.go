package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestOIDCJWTKeysetFileSourceLoadsOwnedPublication(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "keyset.json")
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	writeOIDCJWTKeysetPublication(t, path, 7, expiresAt, `{"keys":[]}`)
	source, err := NewOIDCJWTKeysetFileSource(path)
	if err != nil {
		t.Fatalf("create file source: %v", err)
	}
	snapshot, err := source.Load(context.Background())
	if err != nil {
		t.Fatalf("load file publication: %v", err)
	}
	if snapshot.Sequence != 7 || !snapshot.ExpiresAt.Equal(expiresAt) || string(snapshot.JWKS) != `{"keys":[]}` {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	writeOIDCJWTKeysetPublication(t, path, 8, expiresAt.Add(time.Hour), `{"keys":[{}]}`)
	if string(snapshot.JWKS) != `{"keys":[]}` {
		t.Fatal("loaded JWKS aliases the publication buffer")
	}
	if _, err := json.Marshal(source); err == nil {
		t.Fatal("file source serialized its publication path")
	}
}

func TestOIDCJWTKeysetFileSourceInitializesAndRefreshesVerifier(t *testing.T) {
	t.Parallel()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	jwks, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       &privateKey.PublicKey,
		KeyID:     "publication-key-1",
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}}})
	if err != nil {
		t.Fatalf("marshal JWKS: %v", err)
	}
	path := filepath.Join(t.TempDir(), "keyset.json")
	writeOIDCJWTKeysetPublication(t, path, 1, time.Now().Add(time.Hour), string(jwks))
	source, err := NewOIDCJWTKeysetFileSource(path)
	if err != nil {
		t.Fatalf("create file source: %v", err)
	}
	verifier, err := NewReloadableOIDCJWTVerifier(context.Background(), ReloadableOIDCJWTConfig{
		Issuer:          "https://identity.example.invalid",
		Audience:        APIAudience,
		Algorithms:      []string{"RS256"},
		ClockSkew:       30 * time.Second,
		MaximumLifetime: time.Hour,
		Source:          source,
	})
	if err != nil {
		t.Fatalf("initialize verifier from file publication: %v", err)
	}
	writeOIDCJWTKeysetPublication(t, path, 2, time.Now().Add(2*time.Hour), string(jwks))
	if err := verifier.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh verifier from file publication: %v", err)
	}
}

func TestOIDCJWTKeysetFileSourceRejectsUnsafePathsAndFiles(t *testing.T) {
	t.Parallel()

	for name, path := range map[string]string{
		"empty":        "",
		"relative":     "keyset.json",
		"noncanonical": t.TempDir() + string(filepath.Separator) + ".." + string(filepath.Separator) + "keyset.json",
		"nul":          filepath.Join(t.TempDir(), "keyset.json") + "\x00",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewOIDCJWTKeysetFileSource(path); err == nil {
				t.Fatal("unsafe publication path was accepted")
			}
		})
	}

	directory := t.TempDir()
	missing, err := NewOIDCJWTKeysetFileSource(filepath.Join(directory, "missing.json"))
	if err != nil {
		t.Fatalf("create missing source: %v", err)
	}
	if _, err := missing.Load(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing publication error = %v", err)
	}
	source, err := NewOIDCJWTKeysetFileSource(directory)
	if err != nil {
		t.Fatalf("create directory source: %v", err)
	}
	if _, err := source.Load(context.Background()); !errors.Is(err, ErrOIDCJWTKeysetInvalid) {
		t.Fatalf("directory publication error = %v", err)
	}
	target := filepath.Join(t.TempDir(), "target.json")
	writeOIDCJWTKeysetPublication(t, target, 1, time.Now().Add(time.Hour), `{"keys":[]}`)
	symlink := filepath.Join(t.TempDir(), "keyset-link.json")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatalf("create publication symlink: %v", err)
	}
	source, err = NewOIDCJWTKeysetFileSource(symlink)
	if err != nil {
		t.Fatalf("create symlink source: %v", err)
	}
	if _, err := source.Load(context.Background()); !errors.Is(err, ErrOIDCJWTKeysetInvalid) {
		t.Fatalf("symlink publication error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "writable-keyset.json")
	writeOIDCJWTKeysetPublication(t, path, 1, time.Now().Add(time.Hour), `{"keys":[]}`)
	if err := os.Chmod(path, 0o620); err != nil {
		t.Fatalf("make publication group writable: %v", err)
	}
	source, err = NewOIDCJWTKeysetFileSource(path)
	if err != nil {
		t.Fatalf("create writable source: %v", err)
	}
	if _, err := source.Load(context.Background()); !errors.Is(err, ErrOIDCJWTKeysetInvalid) {
		t.Fatalf("writable publication error = %v", err)
	}
}

func TestOIDCJWTKeysetFileSourceRejectsMalformedPublications(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	for name, content := range map[string]string{
		"empty":           "",
		"duplicate field": `{"sequence":1,"sequence":2,"expiresAt":"` + expiresAt + `","jwks":{"keys":[]}}`,
		"unknown field":   `{"sequence":1,"expiresAt":"` + expiresAt + `","jwks":{"keys":[]},"extra":true}`,
		"trailing value":  `{"sequence":1,"expiresAt":"` + expiresAt + `","jwks":{"keys":[]}} {}`,
		"zero sequence":   `{"sequence":0,"expiresAt":"` + expiresAt + `","jwks":{"keys":[]}}`,
		"missing expiry":  `{"sequence":1,"jwks":{"keys":[]}}`,
		"missing JWKS":    `{"sequence":1,"expiresAt":"` + expiresAt + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "keyset.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write publication: %v", err)
			}
			source, err := NewOIDCJWTKeysetFileSource(path)
			if err != nil {
				t.Fatalf("create file source: %v", err)
			}
			if _, err := source.Load(context.Background()); !errors.Is(err, ErrOIDCJWTKeysetInvalid) {
				t.Fatalf("malformed publication error = %v", err)
			}
		})
	}
}

func TestOIDCJWTKeysetFileSourceBoundsReadsAndPreservesCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &OIDCJWTKeysetFileSource{path: filepath.Join(t.TempDir(), "missing.json")}
	if _, err := source.Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled load = %v", err)
	}

	oversized := strings.NewReader(strings.Repeat("x", maximumOIDCJWTKeysetPublicationBytes+1))
	if _, err := readBoundedOIDCJWTKeysetPublication(context.Background(), oversized); !errors.Is(err, ErrOIDCJWTKeysetInvalid) {
		t.Fatalf("oversized read = %v", err)
	}
	if _, err := readBoundedOIDCJWTKeysetPublication(context.Background(), noProgressReader{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("zero-progress read = %v", err)
	}
}

func writeOIDCJWTKeysetPublication(
	t *testing.T,
	path string,
	sequence uint64,
	expiresAt time.Time,
	jwks string,
) {
	t.Helper()
	content := `{"sequence":` + jsonNumber(sequence) + `,"expiresAt":"` +
		expiresAt.UTC().Format(time.RFC3339Nano) + `","jwks":` + jwks + `}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write publication: %v", err)
	}
}

func jsonNumber(value uint64) string {
	content, _ := json.Marshal(value)
	return string(content)
}

type noProgressReader struct{}

func (noProgressReader) Read([]byte) (int, error) {
	return 0, nil
}

var _ io.Reader = noProgressReader{}
