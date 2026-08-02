package authn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

const oidcJWTKeysetPublicationLockPollInterval = 100 * time.Millisecond

var ErrOIDCJWTKeysetPublicationUncertain = errors.New("OIDC JWT keyset publication durability is uncertain")

// OIDCJWTKeysetFilePublication is one complete public signing-key generation
// to publish through the atomic file contract consumed by
// OIDCJWTKeysetFileSource. JWKS is copied and never modified.
type OIDCJWTKeysetFilePublication struct {
	Path       string
	Sequence   uint64
	ExpiresAt  time.Time
	Algorithms []string
	JWKS       []byte
}

// PublishOIDCJWTKeysetFile validates and canonically publishes one complete
// generation. Concurrent publishers using this function serialize on an
// adjacent advisory lock; exact replay is read-only, while rollback and
// conflicting sequence reuse fail closed.
func PublishOIDCJWTKeysetFile(ctx context.Context, publication OIDCJWTKeysetFilePublication) error {
	if ctx == nil {
		return errors.New("OIDC JWT keyset publication context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := NewOIDCJWTKeysetFileSource(publication.Path)
	if err != nil {
		return err
	}
	now := time.Now()
	expiresAt := publication.ExpiresAt.UTC()
	if publication.Sequence == 0 || publication.ExpiresAt.IsZero() ||
		!expiresAt.After(now) || expiresAt.After(now.Add(maximumOIDCJWTKeysetSnapshotLifetime)) {
		return ErrOIDCJWTKeysetInvalid
	}
	_, algorithms, err := parseOIDCJWTAlgorithms(publication.Algorithms)
	if err != nil {
		return ErrOIDCJWTKeysetInvalid
	}
	canonicalJWKS, err := canonicalOIDCJWKS(publication.JWKS, algorithms)
	if err != nil {
		return ErrOIDCJWTKeysetInvalid
	}
	defer clear(canonicalJWKS)

	lock, err := acquireOIDCJWTKeysetPublicationLock(ctx, publication.Path)
	if err != nil {
		return err
	}
	defer releaseOIDCJWTKeysetPublicationLock(lock)
	if err := ctx.Err(); err != nil {
		return err
	}
	identity, err := inspectOIDCJWTKeysetPublicationTarget(publication.Path)
	if err != nil {
		return err
	}

	current, exists, err := loadCurrentOIDCJWTKeysetPublication(ctx, source, algorithms)
	if err != nil {
		return err
	}
	defer clear(current.JWKS)
	if exists {
		switch {
		case publication.Sequence < current.Sequence:
			return ErrOIDCJWTKeysetRollback
		case publication.Sequence == current.Sequence:
			if !expiresAt.Equal(current.ExpiresAt) || !bytes.Equal(canonicalJWKS, current.JWKS) {
				return ErrOIDCJWTKeysetConflict
			}
			if err := requireUnchangedOIDCJWTKeysetPublicationTarget(publication.Path, identity); err != nil {
				return err
			}
			return syncOIDCJWTKeysetPublicationDirectory(publication.Path)
		}
	}

	encoded, err := json.Marshal(oidcJWTKeysetPublication{
		Sequence:  publication.Sequence,
		ExpiresAt: expiresAt,
		JWKS:      json.RawMessage(canonicalJWKS),
	})
	if err != nil || len(encoded) == 0 || len(encoded) > maximumOIDCJWTKeysetPublicationBytes {
		clear(encoded)
		return ErrOIDCJWTKeysetInvalid
	}
	defer clear(encoded)
	if err := writeAtomicOIDCJWTKeysetPublication(ctx, publication.Path, encoded, identity); err != nil {
		return err
	}
	installed, err := source.Load(context.Background())
	if err != nil {
		return fmt.Errorf("verify installed OIDC JWT keyset publication: %w", err)
	}
	defer clear(installed.JWKS)
	installedJWKS, err := canonicalOIDCJWKS(installed.JWKS, algorithms)
	if err != nil {
		return ErrOIDCJWTKeysetInvalid
	}
	defer clear(installedJWKS)
	if installed.Sequence != publication.Sequence || !installed.ExpiresAt.Equal(expiresAt) ||
		!bytes.Equal(installedJWKS, canonicalJWKS) {
		return ErrOIDCJWTKeysetConflict
	}
	return nil
}

func canonicalOIDCJWKS(content []byte, algorithms map[string]struct{}) ([]byte, error) {
	keys, err := parseOIDCJWKS(content, algorithms)
	if err != nil {
		return nil, err
	}
	keyIDs := make([]string, 0, len(keys))
	for keyID := range keys {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)
	ordered := make([]jose.JSONWebKey, 0, len(keyIDs))
	for _, keyID := range keyIDs {
		ordered = append(ordered, keys[keyID])
	}
	return json.Marshal(jose.JSONWebKeySet{Keys: ordered})
}

func loadCurrentOIDCJWTKeysetPublication(
	ctx context.Context,
	source *OIDCJWTKeysetFileSource,
	algorithms map[string]struct{},
) (OIDCJWTKeysetSnapshot, bool, error) {
	if _, err := os.Lstat(source.path); errors.Is(err, os.ErrNotExist) {
		return OIDCJWTKeysetSnapshot{}, false, nil
	} else if err != nil {
		return OIDCJWTKeysetSnapshot{}, false, fmt.Errorf("inspect current OIDC JWT keyset publication: %w", err)
	}
	snapshot, err := source.Load(ctx)
	if err != nil {
		return OIDCJWTKeysetSnapshot{}, false, fmt.Errorf("load current OIDC JWT keyset publication: %w", err)
	}
	canonicalJWKS, err := canonicalOIDCJWKS(snapshot.JWKS, algorithms)
	clear(snapshot.JWKS)
	if err != nil {
		return OIDCJWTKeysetSnapshot{}, false, ErrOIDCJWTKeysetInvalid
	}
	snapshot.JWKS = canonicalJWKS
	return snapshot, true, nil
}

func acquireOIDCJWTKeysetPublicationLock(ctx context.Context, targetPath string) (*os.File, error) {
	directory := filepath.Dir(targetPath)
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return nil, errors.New("OIDC JWT keyset publication directory must exist without symlinks")
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("OIDC JWT keyset publication directory must not be writable by group or other users")
	}
	lockPath := targetPath + ".lock"
	lock, err := os.OpenFile(
		lockPath,
		os.O_CREATE|os.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("open OIDC JWT keyset publication lock: %w", err)
	}
	lockPathInfo, err := os.Lstat(lockPath)
	if err != nil {
		lock.Close()
		return nil, fmt.Errorf("inspect OIDC JWT keyset publication lock: %w", err)
	}
	lockInfo, err := lock.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0o077 != 0 ||
		!os.SameFile(lockPathInfo, lockInfo) {
		lock.Close()
		return nil, errors.New("OIDC JWT keyset publication lock is unsafe")
	}

	ticker := time.NewTicker(oidcJWTKeysetPublicationLockPollInterval)
	defer ticker.Stop()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			lock.Close()
			return nil, fmt.Errorf("lock OIDC JWT keyset publication: %w", err)
		}
		select {
		case <-ctx.Done():
			lock.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func releaseOIDCJWTKeysetPublicationLock(lock *os.File) {
	if lock == nil {
		return
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_ = lock.Close()
}

type oidcJWTKeysetPublicationTarget struct {
	exists bool
	info   os.FileInfo
}

func inspectOIDCJWTKeysetPublicationTarget(path string) (oidcJWTKeysetPublicationTarget, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return oidcJWTKeysetPublicationTarget{}, nil
	}
	if err != nil {
		return oidcJWTKeysetPublicationTarget{}, fmt.Errorf("inspect OIDC JWT keyset publication target: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return oidcJWTKeysetPublicationTarget{}, errors.New("OIDC JWT keyset publication target is unsafe")
	}
	return oidcJWTKeysetPublicationTarget{exists: true, info: info}, nil
}

func requireUnchangedOIDCJWTKeysetPublicationTarget(
	path string,
	expected oidcJWTKeysetPublicationTarget,
) error {
	actual, err := inspectOIDCJWTKeysetPublicationTarget(path)
	if err != nil {
		return err
	}
	if actual.exists != expected.exists ||
		(actual.exists && (!os.SameFile(actual.info, expected.info) || actual.info.Size() != expected.info.Size() ||
			!actual.info.ModTime().Equal(expected.info.ModTime()) || actual.info.Mode() != expected.info.Mode())) {
		return errors.New("OIDC JWT keyset publication target changed during publication")
	}
	return nil
}

func writeAtomicOIDCJWTKeysetPublication(
	ctx context.Context,
	targetPath string,
	content []byte,
	expected oidcJWTKeysetPublicationTarget,
) error {
	directory := filepath.Dir(targetPath)
	temporary, err := os.CreateTemp(directory, ".dataground-oidc-keyset-*")
	if err != nil {
		return fmt.Errorf("create OIDC JWT keyset publication: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("secure OIDC JWT keyset publication: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write OIDC JWT keyset publication: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync OIDC JWT keyset publication: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close OIDC JWT keyset publication: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireUnchangedOIDCJWTKeysetPublicationTarget(targetPath, expected); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		return fmt.Errorf("install OIDC JWT keyset publication: %w", err)
	}
	committed = true
	return syncOIDCJWTKeysetPublicationDirectory(targetPath)
}

func syncOIDCJWTKeysetPublicationDirectory(targetPath string) error {
	directoryHandle, err := os.Open(filepath.Dir(targetPath))
	if err != nil {
		return fmt.Errorf("%w: open publication directory: %v", ErrOIDCJWTKeysetPublicationUncertain, err)
	}
	syncErr := directoryHandle.Sync()
	closeErr := directoryHandle.Close()
	if syncErr != nil {
		return fmt.Errorf("%w: sync publication directory: %v", ErrOIDCJWTKeysetPublicationUncertain, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("%w: close publication directory: %v", ErrOIDCJWTKeysetPublicationUncertain, closeErr)
	}
	return nil
}
