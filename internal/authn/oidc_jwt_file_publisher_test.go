package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestPublishOIDCJWTKeysetFileInstallsCanonicalOwnedPublication(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "keysets.json")
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	publication := testOIDCJWTKeysetFilePublication(t, path, 1, expiresAt, "key-b")
	if err := PublishOIDCJWTKeysetFile(context.Background(), publication); err != nil {
		t.Fatalf("publish OIDC JWT keyset: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect publication: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("publication mode = %o, want 600", info.Mode().Perm())
	}
	source, err := NewOIDCJWTKeysetFileSource(path)
	if err != nil {
		t.Fatalf("create publication source: %v", err)
	}
	snapshot, err := source.Load(context.Background())
	if err != nil {
		t.Fatalf("load publication: %v", err)
	}
	defer clear(snapshot.JWKS)
	if snapshot.Sequence != 1 || !snapshot.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	before := info.ModTime()
	if err := PublishOIDCJWTKeysetFile(context.Background(), publication); err != nil {
		t.Fatalf("replay publication: %v", err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("inspect replayed publication: %v", err)
	}
	if !before.Equal(after.ModTime()) {
		t.Fatal("exact replay rewrote the publication")
	}
}

func TestPublishOIDCJWTKeysetFileRejectsRollbackAndSequenceConflict(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "keysets.json")
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	current := testOIDCJWTKeysetFilePublication(t, path, 4, expiresAt, "key-current")
	if err := PublishOIDCJWTKeysetFile(context.Background(), current); err != nil {
		t.Fatalf("publish current keyset: %v", err)
	}
	rollback := testOIDCJWTKeysetFilePublication(t, path, 3, expiresAt, "key-old")
	if err := PublishOIDCJWTKeysetFile(context.Background(), rollback); !errors.Is(err, ErrOIDCJWTKeysetRollback) {
		t.Fatalf("rollback error = %v", err)
	}
	conflict := testOIDCJWTKeysetFilePublication(t, path, 4, expiresAt, "key-conflict")
	if err := PublishOIDCJWTKeysetFile(context.Background(), conflict); !errors.Is(err, ErrOIDCJWTKeysetConflict) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestPublishOIDCJWTKeysetFileSerializesConcurrentGenerations(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "keysets.json")
	expiresAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	publications := []OIDCJWTKeysetFilePublication{
		testOIDCJWTKeysetFilePublication(t, path, 2, expiresAt, "key-2"),
		testOIDCJWTKeysetFilePublication(t, path, 3, expiresAt, "key-3"),
	}
	errorsByGeneration := make([]error, len(publications))
	var wait sync.WaitGroup
	for index := range publications {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsByGeneration[index] = PublishOIDCJWTKeysetFile(context.Background(), publications[index])
		}()
	}
	wait.Wait()
	if errorsByGeneration[1] != nil {
		t.Fatalf("latest generation failed: %v", errorsByGeneration[1])
	}
	if errorsByGeneration[0] != nil && !errors.Is(errorsByGeneration[0], ErrOIDCJWTKeysetRollback) {
		t.Fatalf("earlier generation error = %v", errorsByGeneration[0])
	}
	source, err := NewOIDCJWTKeysetFileSource(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer clear(snapshot.JWKS)
	if snapshot.Sequence != 3 {
		t.Fatalf("final sequence = %d, want 3", snapshot.Sequence)
	}
}

func TestPublishOIDCJWTKeysetFileHonorsCancellationWhileWaitingForWriter(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "keysets.json")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	publication := testOIDCJWTKeysetFilePublication(
		t,
		path,
		1,
		time.Now().UTC().Add(time.Hour),
		"key-1",
	)
	if err := PublishOIDCJWTKeysetFile(ctx, publication); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled publication error = %v", err)
	}
}

func TestPublishOIDCJWTKeysetFileRejectsUnsafeInputs(t *testing.T) {
	t.Parallel()

	expiresAt := time.Now().UTC().Add(time.Hour)
	valid := testOIDCJWTKeysetFilePublication(
		t,
		filepath.Join(t.TempDir(), "keysets.json"),
		1,
		expiresAt,
		"key-1",
	)
	tests := map[string]OIDCJWTKeysetFilePublication{
		"relative path":         {Path: "keysets.json", Sequence: 1, ExpiresAt: expiresAt, Algorithms: valid.Algorithms, JWKS: valid.JWKS},
		"zero sequence":         {Path: valid.Path, ExpiresAt: expiresAt, Algorithms: valid.Algorithms, JWKS: valid.JWKS},
		"expired generation":    {Path: valid.Path, Sequence: 1, ExpiresAt: time.Now().Add(-time.Minute), Algorithms: valid.Algorithms, JWKS: valid.JWKS},
		"unsupported algorithm": {Path: valid.Path, Sequence: 1, ExpiresAt: expiresAt, Algorithms: []string{"HS256"}, JWKS: valid.JWKS},
		"malformed JWKS":        {Path: valid.Path, Sequence: 1, ExpiresAt: expiresAt, Algorithms: valid.Algorithms, JWKS: []byte(`{"keys":[]}`)},
	}
	for name, publication := range tests {
		name, publication := name, publication
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := PublishOIDCJWTKeysetFile(context.Background(), publication); err == nil {
				t.Fatal("unsafe publication was accepted")
			}
		})
	}

	unsafeDirectory := t.TempDir()
	if err := os.Chmod(unsafeDirectory, 0o777); err != nil {
		t.Fatal(err)
	}
	unsafe := valid
	unsafe.Path = filepath.Join(unsafeDirectory, "keysets.json")
	if err := PublishOIDCJWTKeysetFile(context.Background(), unsafe); err == nil {
		t.Fatal("unsafe publication directory was accepted")
	}
}

func testOIDCJWTKeysetFilePublication(
	t *testing.T,
	path string,
	sequence uint64,
	expiresAt time.Time,
	keyID string,
) OIDCJWTKeysetFilePublication {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key:       &privateKey.PublicKey,
		KeyID:     keyID,
		Algorithm: string(jose.RS256),
		Use:       "sig",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return OIDCJWTKeysetFilePublication{
		Path:       path,
		Sequence:   sequence,
		ExpiresAt:  expiresAt,
		Algorithms: []string{"RS256"},
		JWKS:       jwks,
	}
}
