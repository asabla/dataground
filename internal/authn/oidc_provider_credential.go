package authn

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	oidcProviderCredentialContract         = "dataground.oidc-provider-credential/v1"
	maximumOIDCProviderCredentialBytes     = 16 << 10
	maximumOIDCProviderBearerTokenBytes    = 8 << 10
	maximumOIDCProviderCredentialLifetime  = 31 * 24 * time.Hour
	oidcProviderCredentialClockSkew        = 5 * time.Minute
	oidcProviderCredentialLockPollInterval = 100 * time.Millisecond
)

var (
	ErrOIDCProviderCredentialInvalid              = errors.New("OIDC provider credential is invalid")
	ErrOIDCProviderCredentialUnavailable          = errors.New("OIDC provider credential is unavailable")
	ErrOIDCProviderCredentialRevoked              = errors.New("OIDC provider credential is revoked")
	ErrOIDCProviderCredentialRollback             = errors.New("OIDC provider credential generation rollback")
	ErrOIDCProviderCredentialConflict             = errors.New("OIDC provider credential generation conflict")
	ErrOIDCProviderCredentialPublicationUncertain = errors.New("OIDC provider credential publication durability is uncertain")
)

// OIDCProviderCredentialPublication is one complete local credential state.
// BearerToken is copied and never modified. A revoked publication must omit it.
type OIDCProviderCredentialPublication struct {
	Path                   string
	Generation             uint64
	ProviderID             string
	ProviderRegistrySHA256 string
	Endpoint               string
	ActivatedAt            time.Time
	ExpiresAt              time.Time
	RevokedAt              time.Time
	Revoked                bool
	BearerToken            []byte
}

func (OIDCProviderCredentialPublication) MarshalJSON() ([]byte, error) {
	return nil, errors.New("OIDC provider credential publications cannot be serialized")
}

type oidcProviderCredentialDocument struct {
	Contract               string          `json:"contract"`
	Generation             uint64          `json:"generation"`
	ProviderID             string          `json:"providerId"`
	ProviderRegistrySHA256 string          `json:"providerRegistrySha256"`
	Endpoint               string          `json:"endpoint"`
	Status                 string          `json:"status"`
	ActivatedAt            json.RawMessage `json:"activatedAt,omitempty"`
	ExpiresAt              json.RawMessage `json:"expiresAt,omitempty"`
	RevokedAt              json.RawMessage `json:"revokedAt,omitempty"`
	BearerToken            json.RawMessage `json:"bearerToken,omitempty"`
}

type oidcProviderCredentialSnapshot struct {
	Generation             uint64
	ProviderID             string
	ProviderRegistrySHA256 string
	Endpoint               string
	Revoked                bool
	ActivatedAt            time.Time
	ExpiresAt              time.Time
	RevokedAt              time.Time
	BearerToken            []byte
}

// LoadOIDCProviderBearerCredential loads one exact active endpoint credential.
// The returned token is owned by the caller and must be cleared after use.
func LoadOIDCProviderBearerCredential(
	ctx context.Context,
	path string,
	providerID string,
	providerRegistrySHA256 string,
	endpoint string,
) ([]byte, error) {
	snapshot, err := loadOIDCProviderCredential(ctx, path)
	if err != nil {
		return nil, err
	}
	defer clear(snapshot.BearerToken)
	if snapshot.ProviderID != providerID ||
		snapshot.ProviderRegistrySHA256 != providerRegistrySHA256 || snapshot.Endpoint != endpoint {
		return nil, ErrOIDCProviderCredentialInvalid
	}
	if snapshot.Revoked {
		return nil, ErrOIDCProviderCredentialRevoked
	}
	now := time.Now()
	if now.Before(snapshot.ActivatedAt) || !snapshot.ExpiresAt.After(now) {
		return nil, ErrOIDCProviderCredentialUnavailable
	}
	return append([]byte(nil), snapshot.BearerToken...), nil
}

// PublishOIDCProviderCredential atomically installs one sequential local
// credential state. Exact replay is read-only; gaps, rollback, and conflicting
// reuse fail closed. Revocation installs a durable tombstone instead of deleting
// the last known state.
func PublishOIDCProviderCredential(ctx context.Context, publication OIDCProviderCredentialPublication) error {
	if ctx == nil {
		return errors.New("OIDC provider credential publication context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validOIDCProviderCredentialPath(publication.Path) || publication.Generation == 0 ||
		!ValidOIDCProviderBinding(publication.ProviderID, publication.ProviderRegistrySHA256) ||
		!ValidOIDCProviderEndpoint(publication.Endpoint) {
		return ErrOIDCProviderCredentialInvalid
	}
	now := time.Now()
	activatedAt := publication.ActivatedAt.UTC()
	expiresAt := publication.ExpiresAt.UTC()
	revokedAt := publication.RevokedAt.UTC()
	if publication.Revoked {
		if !publication.ActivatedAt.IsZero() || !publication.ExpiresAt.IsZero() ||
			publication.RevokedAt.IsZero() || len(publication.BearerToken) != 0 {
			return ErrOIDCProviderCredentialInvalid
		}
	} else if publication.ActivatedAt.IsZero() || publication.ExpiresAt.IsZero() ||
		!publication.RevokedAt.IsZero() ||
		!expiresAt.After(activatedAt) || expiresAt.Sub(activatedAt) > maximumOIDCProviderCredentialLifetime ||
		!ValidOIDCProviderBearerToken(publication.BearerToken) {
		return ErrOIDCProviderCredentialInvalid
	}

	lock, err := acquireOIDCProviderCredentialLock(ctx, publication.Path)
	if err != nil {
		return err
	}
	defer releaseOIDCProviderCredentialLock(lock)
	identity, err := inspectOIDCProviderCredentialTarget(publication.Path)
	if err != nil {
		return err
	}
	current, exists, err := loadCurrentOIDCProviderCredential(ctx, publication.Path)
	if err != nil {
		return err
	}
	defer clear(current.BearerToken)
	if exists {
		if publication.ProviderID != current.ProviderID || publication.Endpoint != current.Endpoint {
			return ErrOIDCProviderCredentialConflict
		}
		switch {
		case publication.Generation < current.Generation:
			return ErrOIDCProviderCredentialRollback
		case publication.Generation == current.Generation:
			if !sameOIDCProviderCredentialPublication(publication, current) {
				return ErrOIDCProviderCredentialConflict
			}
			if err := requireUnchangedOIDCProviderCredentialTarget(publication.Path, identity); err != nil {
				return err
			}
			return syncOIDCProviderCredentialDirectory(publication.Path, lock)
		case publication.Generation != current.Generation+1:
			return ErrOIDCProviderCredentialRollback
		}
	} else if publication.Generation != 1 {
		return ErrOIDCProviderCredentialRollback
	}
	if publication.Revoked {
		if revokedAt.Before(now.Add(-oidcProviderCredentialClockSkew)) ||
			revokedAt.After(now.Add(oidcProviderCredentialClockSkew)) {
			return ErrOIDCProviderCredentialInvalid
		}
	} else if activatedAt.After(now.Add(oidcProviderCredentialClockSkew)) || !expiresAt.After(now) {
		return ErrOIDCProviderCredentialInvalid
	}

	document := map[string]any{
		"contract":               oidcProviderCredentialContract,
		"generation":             publication.Generation,
		"providerId":             publication.ProviderID,
		"providerRegistrySha256": publication.ProviderRegistrySHA256,
		"endpoint":               publication.Endpoint,
	}
	if publication.Revoked {
		document["status"] = "revoked"
		document["revokedAt"] = revokedAt
	} else {
		document["status"] = "active"
		document["activatedAt"] = activatedAt
		document["expiresAt"] = expiresAt
		document["bearerToken"] = string(publication.BearerToken)
	}
	encoded, err := json.Marshal(document)
	if err != nil || len(encoded) == 0 || len(encoded) > maximumOIDCProviderCredentialBytes {
		clear(encoded)
		return ErrOIDCProviderCredentialInvalid
	}
	defer clear(encoded)
	if err := writeAtomicOIDCProviderCredential(
		ctx, publication.Path, encoded, identity, lock,
	); err != nil {
		return err
	}
	installed, err := loadOIDCProviderCredential(context.Background(), publication.Path)
	if err != nil {
		return fmt.Errorf("verify installed OIDC provider credential: %w", err)
	}
	defer clear(installed.BearerToken)
	if !sameOIDCProviderCredentialPublication(publication, installed) {
		return ErrOIDCProviderCredentialConflict
	}
	return nil
}

func ValidOIDCProviderEndpoint(endpoint string) bool {
	return endpoint == "discovery" || endpoint == "jwks"
}

func ValidOIDCProviderBearerToken(token []byte) bool {
	if len(token) == 0 || len(token) > maximumOIDCProviderBearerTokenBytes {
		return false
	}
	padding := false
	for _, value := range token {
		if value == '=' {
			padding = true
			continue
		}
		if padding || !((value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') ||
			(value >= '0' && value <= '9') || strings.ContainsRune("-._~+/", rune(value))) {
			return false
		}
	}
	return true
}

func loadOIDCProviderCredential(ctx context.Context, path string) (oidcProviderCredentialSnapshot, error) {
	var snapshot oidcProviderCredentialSnapshot
	if ctx == nil || !validOIDCProviderCredentialPath(path) {
		return snapshot, ErrOIDCProviderCredentialInvalid
	}
	if err := ctx.Err(); err != nil {
		return snapshot, err
	}
	content, err := readStableOIDCProviderCredentialFile(ctx, path)
	if err != nil {
		return snapshot, err
	}
	defer clear(content)
	if err := requireUniqueJSONObject(content); err != nil {
		return snapshot, ErrOIDCProviderCredentialInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var document oidcProviderCredentialDocument
	if err := decoder.Decode(&document); err != nil {
		return snapshot, ErrOIDCProviderCredentialInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) ||
		document.Contract != oidcProviderCredentialContract || document.Generation == 0 ||
		!ValidOIDCProviderBinding(document.ProviderID, document.ProviderRegistrySHA256) ||
		!ValidOIDCProviderEndpoint(document.Endpoint) {
		return snapshot, ErrOIDCProviderCredentialInvalid
	}
	snapshot = oidcProviderCredentialSnapshot{
		Generation: document.Generation, ProviderID: document.ProviderID,
		ProviderRegistrySHA256: document.ProviderRegistrySHA256, Endpoint: document.Endpoint,
	}
	switch document.Status {
	case "revoked":
		if len(document.ActivatedAt) != 0 || len(document.ExpiresAt) != 0 || len(document.BearerToken) != 0 ||
			bytes.Equal(bytes.TrimSpace(document.RevokedAt), []byte("null")) ||
			json.Unmarshal(document.RevokedAt, &snapshot.RevokedAt) != nil || snapshot.RevokedAt.IsZero() {
			return oidcProviderCredentialSnapshot{}, ErrOIDCProviderCredentialInvalid
		}
		snapshot.RevokedAt = snapshot.RevokedAt.UTC()
		snapshot.Revoked = true
	case "active":
		token := bytes.TrimSpace(document.BearerToken)
		if len(document.RevokedAt) != 0 || bytes.Equal(bytes.TrimSpace(document.ActivatedAt), []byte("null")) ||
			bytes.Equal(bytes.TrimSpace(document.ExpiresAt), []byte("null")) ||
			bytes.Equal(token, []byte("null")) || len(token) < 3 || token[0] != '"' || token[len(token)-1] != '"' ||
			bytes.IndexByte(token[1:len(token)-1], '\\') >= 0 || bytes.IndexByte(token[1:len(token)-1], '"') >= 0 ||
			json.Unmarshal(document.ActivatedAt, &snapshot.ActivatedAt) != nil ||
			json.Unmarshal(document.ExpiresAt, &snapshot.ExpiresAt) != nil ||
			snapshot.ActivatedAt.IsZero() || snapshot.ExpiresAt.IsZero() ||
			!snapshot.ExpiresAt.After(snapshot.ActivatedAt) ||
			snapshot.ExpiresAt.Sub(snapshot.ActivatedAt) > maximumOIDCProviderCredentialLifetime ||
			!ValidOIDCProviderBearerToken(token[1:len(token)-1]) {
			clear(snapshot.BearerToken)
			return oidcProviderCredentialSnapshot{}, ErrOIDCProviderCredentialInvalid
		}
		snapshot.BearerToken = append([]byte(nil), token[1:len(token)-1]...)
		snapshot.ActivatedAt = snapshot.ActivatedAt.UTC()
		snapshot.ExpiresAt = snapshot.ExpiresAt.UTC()
	default:
		return oidcProviderCredentialSnapshot{}, ErrOIDCProviderCredentialInvalid
	}
	return snapshot, nil
}

func loadCurrentOIDCProviderCredential(ctx context.Context, path string) (oidcProviderCredentialSnapshot, bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return oidcProviderCredentialSnapshot{}, false, nil
	} else if err != nil {
		return oidcProviderCredentialSnapshot{}, false, ErrOIDCProviderCredentialUnavailable
	}
	snapshot, err := loadOIDCProviderCredential(ctx, path)
	return snapshot, err == nil, err
}

func sameOIDCProviderCredentialPublication(publication OIDCProviderCredentialPublication, current oidcProviderCredentialSnapshot) bool {
	return publication.Generation == current.Generation && publication.ProviderID == current.ProviderID &&
		publication.ProviderRegistrySHA256 == current.ProviderRegistrySHA256 && publication.Endpoint == current.Endpoint &&
		publication.Revoked == current.Revoked && (publication.Revoked ||
		(publication.ActivatedAt.UTC().Equal(current.ActivatedAt) && publication.ExpiresAt.UTC().Equal(current.ExpiresAt) &&
			bytes.Equal(publication.BearerToken, current.BearerToken))) &&
		(!publication.Revoked || publication.RevokedAt.UTC().Equal(current.RevokedAt))
}

func validOIDCProviderCredentialPath(path string) bool {
	return path != "" && strings.IndexByte(path, 0) < 0 && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func readStableOIDCProviderCredentialFile(ctx context.Context, path string) ([]byte, error) {
	directoryInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode().Perm()&0o077 != 0 {
		return nil, ErrOIDCProviderCredentialInvalid
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return nil, ErrOIDCProviderCredentialUnavailable
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Mode().Perm()&0o177 != 0 ||
		pathInfo.Size() <= 0 || pathInfo.Size() > maximumOIDCProviderCredentialBytes {
		return nil, ErrOIDCProviderCredentialInvalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrOIDCProviderCredentialUnavailable
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, before) {
		return nil, ErrOIDCProviderCredentialUnavailable
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumOIDCProviderCredentialBytes+1))
	if err != nil || len(content) == 0 || len(content) > maximumOIDCProviderCredentialBytes {
		clear(content)
		return nil, ErrOIDCProviderCredentialInvalid
	}
	after, err := file.Stat()
	pathAfter, pathErr := os.Lstat(path)
	resolvedAfter, resolveErr := filepath.EvalSymlinks(path)
	directoryAfter, directoryErr := os.Lstat(filepath.Dir(path))
	if err != nil || pathErr != nil || resolveErr != nil || resolvedAfter != path || !os.SameFile(before, after) ||
		!os.SameFile(after, pathAfter) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) ||
		before.Mode() != after.Mode() || int64(len(content)) != after.Size() || directoryErr != nil ||
		!os.SameFile(directoryInfo, directoryAfter) || directoryInfo.Mode() != directoryAfter.Mode() {
		clear(content)
		return nil, ErrOIDCProviderCredentialUnavailable
	}
	if err := ctx.Err(); err != nil {
		clear(content)
		return nil, err
	}
	return content, nil
}

type oidcProviderCredentialTarget struct {
	exists bool
	info   os.FileInfo
}

func inspectOIDCProviderCredentialTarget(path string) (oidcProviderCredentialTarget, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return oidcProviderCredentialTarget{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o177 != 0 {
		return oidcProviderCredentialTarget{}, ErrOIDCProviderCredentialInvalid
	}
	return oidcProviderCredentialTarget{exists: true, info: info}, nil
}

func requireUnchangedOIDCProviderCredentialTarget(path string, expected oidcProviderCredentialTarget) error {
	actual, err := inspectOIDCProviderCredentialTarget(path)
	if err != nil {
		return err
	}
	if actual.exists != expected.exists || (actual.exists && (!os.SameFile(actual.info, expected.info) ||
		actual.info.Size() != expected.info.Size() || !actual.info.ModTime().Equal(expected.info.ModTime()) ||
		actual.info.Mode() != expected.info.Mode())) {
		return ErrOIDCProviderCredentialConflict
	}
	return nil
}

type oidcProviderCredentialLock struct {
	file          *os.File
	directory     *os.File
	directoryInfo os.FileInfo
}

func acquireOIDCProviderCredentialLock(ctx context.Context, targetPath string) (*oidcProviderCredentialLock, error) {
	directory := filepath.Dir(targetPath)
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return nil, ErrOIDCProviderCredentialInvalid
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrOIDCProviderCredentialInvalid
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return nil, ErrOIDCProviderCredentialUnavailable
	}
	directoryHandleInfo, err := directoryHandle.Stat()
	if err != nil || !os.SameFile(info, directoryHandleInfo) {
		directoryHandle.Close()
		return nil, ErrOIDCProviderCredentialConflict
	}
	lockDescriptor, err := syscall.Openat(
		int(directoryHandle.Fd()), filepath.Base(targetPath)+".lock",
		syscall.O_CREATE|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		directoryHandle.Close()
		return nil, ErrOIDCProviderCredentialUnavailable
	}
	lock := os.NewFile(uintptr(lockDescriptor), filepath.Base(targetPath)+".lock")
	lockInfo, statErr := lock.Stat()
	if statErr != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0o077 != 0 {
		lock.Close()
		directoryHandle.Close()
		return nil, ErrOIDCProviderCredentialInvalid
	}
	ticker := time.NewTicker(oidcProviderCredentialLockPollInterval)
	defer ticker.Stop()
	for {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			if err := requireUnchangedOIDCProviderCredentialDirectory(targetPath, info); err != nil {
				lock.Close()
				directoryHandle.Close()
				return nil, err
			}
			return &oidcProviderCredentialLock{
				file: lock, directory: directoryHandle, directoryInfo: info,
			}, nil
		} else if !errors.Is(err, syscall.EWOULDBLOCK) {
			lock.Close()
			directoryHandle.Close()
			return nil, ErrOIDCProviderCredentialUnavailable
		}
		select {
		case <-ctx.Done():
			lock.Close()
			directoryHandle.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func releaseOIDCProviderCredentialLock(lock *oidcProviderCredentialLock) {
	if lock == nil {
		return
	}
	_ = syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	_ = lock.file.Close()
	_ = lock.directory.Close()
}

func writeAtomicOIDCProviderCredential(
	ctx context.Context,
	path string,
	content []byte,
	expected oidcProviderCredentialTarget,
	lock *oidcProviderCredentialLock,
) error {
	if lock == nil || lock.directory == nil {
		return ErrOIDCProviderCredentialInvalid
	}
	temporaryName, temporary, err := createOIDCProviderCredentialTemporary(lock.directory)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = syscall.Unlinkat(int(lock.directory.Fd()), temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return ErrOIDCProviderCredentialUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return ErrOIDCProviderCredentialUnavailable
	}
	if err := temporary.Sync(); err != nil {
		return ErrOIDCProviderCredentialUnavailable
	}
	if err := temporary.Close(); err != nil {
		return ErrOIDCProviderCredentialUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireUnchangedOIDCProviderCredentialTarget(path, expected); err != nil {
		return err
	}
	if err := requireUnchangedOIDCProviderCredentialDirectory(path, lock.directoryInfo); err != nil {
		return err
	}
	if err := syscall.Renameat(
		int(lock.directory.Fd()), temporaryName,
		int(lock.directory.Fd()), filepath.Base(path),
	); err != nil {
		return ErrOIDCProviderCredentialUnavailable
	}
	committed = true
	return syncOIDCProviderCredentialDirectory(path, lock)
}

func createOIDCProviderCredentialTemporary(directory *os.File) (string, *os.File, error) {
	for attempt := 0; attempt < 16; attempt++ {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return "", nil, ErrOIDCProviderCredentialUnavailable
		}
		name := ".dataground-oidc-provider-credential-" + hex.EncodeToString(random)
		descriptor, err := syscall.Openat(
			int(directory.Fd()), name,
			syscall.O_CREATE|syscall.O_EXCL|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
			0o600,
		)
		if err == nil {
			return name, os.NewFile(uintptr(descriptor), name), nil
		}
		if !errors.Is(err, syscall.EEXIST) {
			return "", nil, ErrOIDCProviderCredentialUnavailable
		}
	}
	return "", nil, ErrOIDCProviderCredentialUnavailable
}

func requireUnchangedOIDCProviderCredentialDirectory(path string, expected os.FileInfo) error {
	directoryPath := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(directoryPath)
	actual, statErr := os.Lstat(directoryPath)
	if err != nil || statErr != nil || resolved != directoryPath || expected == nil ||
		!os.SameFile(expected, actual) || expected.Mode() != actual.Mode() {
		return ErrOIDCProviderCredentialConflict
	}
	return nil
}

func syncOIDCProviderCredentialDirectory(path string, lock *oidcProviderCredentialLock) error {
	if lock == nil || lock.directory == nil ||
		requireUnchangedOIDCProviderCredentialDirectory(path, lock.directoryInfo) != nil {
		return ErrOIDCProviderCredentialConflict
	}
	actual, statErr := lock.directory.Stat()
	if statErr != nil || !os.SameFile(lock.directoryInfo, actual) {
		return ErrOIDCProviderCredentialConflict
	}
	if err := lock.directory.Sync(); err != nil {
		return ErrOIDCProviderCredentialPublicationUncertain
	}
	if err := requireUnchangedOIDCProviderCredentialDirectory(path, lock.directoryInfo); err != nil {
		return err
	}
	return nil
}
