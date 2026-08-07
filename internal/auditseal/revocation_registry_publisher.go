package auditseal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const revocationSourceRegistryPublicationLockPollInterval = 100 * time.Millisecond

var (
	ErrRevocationSourceRegistryPublicationConflict = errors.New("audit export revocation source registry publication conflicts with installed content")
	ErrRevocationSourceRegistryPublicationUncertain = errors.New("audit export revocation source registry publication durability is uncertain")
)

type RevocationSourceRegistryFilePublication struct {
	InputPath string
	Path      string
	Purpose   string
	SourceID  string
}

// PublishRevocationSourceRegistryFile validates one complete canonical
// registry and installs it at an immutable owner-only path. Exact replay is
// read-only; changing an existing publication fails closed.
func PublishRevocationSourceRegistryFile(
	ctx context.Context,
	publication RevocationSourceRegistryFilePublication,
) (RevocationSourceEvidence, error) {
	var evidence RevocationSourceEvidence
	if ctx == nil {
		return evidence, errors.New("audit export revocation source registry publication context is required")
	}
	if err := ctx.Err(); err != nil {
		return evidence, err
	}
	if !canonicalAbsolutePath(publication.InputPath) ||
		!canonicalAbsolutePath(publication.Path) || publication.InputPath == publication.Path ||
		!validRevocationNoticePurpose(publication.Purpose) ||
		!auditExportDeliveryRecipientPattern.MatchString(publication.SourceID) {
		return evidence, ErrRevocationNoticeAcquisitionInvalid
	}
	encoded, err := readStablePrivateFile(publication.InputPath, maximumRevocationSourceRegistryBytes)
	if err != nil {
		return evidence, ErrRevocationNoticeAcquisitionInvalid
	}
	defer clear(encoded)
	if _, err := selectRevocationSourceProfile(encoded, publication.Purpose, publication.SourceID); err != nil {
		return evidence, err
	}
	digest := sha256.Sum256(encoded)
	evidence = RevocationSourceEvidence{
		Purpose: publication.Purpose, SourceID: publication.SourceID,
		SourceRegistrySHA256: digestString(digest),
	}

	lock, err := acquireRevocationSourceRegistryPublicationLock(ctx, publication.Path)
	if err != nil {
		return RevocationSourceEvidence{}, err
	}
	defer releaseRevocationSourceRegistryPublicationLock(lock)
	if err := ctx.Err(); err != nil {
		return RevocationSourceEvidence{}, err
	}
	target, err := inspectRevocationSourceRegistryPublicationTarget(publication.Path)
	if err != nil {
		return RevocationSourceEvidence{}, err
	}
	if target.exists {
		installed, err := readStablePrivateFile(publication.Path, maximumRevocationSourceRegistryBytes)
		if err != nil {
			return RevocationSourceEvidence{}, ErrRevocationNoticeAcquisitionInvalid
		}
		defer clear(installed)
		if !bytes.Equal(installed, encoded) {
			return RevocationSourceEvidence{}, ErrRevocationSourceRegistryPublicationConflict
		}
		if err := requireUnchangedRevocationSourceRegistryPublicationTarget(publication.Path, target); err != nil {
			return RevocationSourceEvidence{}, err
		}
		return evidence, syncRevocationSourceRegistryPublicationDirectory(publication.Path)
	}
	if err := writeAtomicRevocationSourceRegistryPublication(ctx, publication.Path, encoded, target); err != nil {
		return RevocationSourceEvidence{}, err
	}
	installed, err := InspectRevocationSourceRegistryFile(publication.Path, publication.Purpose, publication.SourceID)
	if err != nil {
		return RevocationSourceEvidence{}, fmt.Errorf("verify installed audit export revocation source registry: %w", err)
	}
	if installed != evidence {
		return RevocationSourceEvidence{}, ErrRevocationSourceRegistryPublicationConflict
	}
	return evidence, nil
}

func acquireRevocationSourceRegistryPublicationLock(ctx context.Context, targetPath string) (*os.File, error) {
	directory := filepath.Dir(targetPath)
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return nil, errors.New("audit export revocation source registry publication directory must exist without symlinks")
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("audit export revocation source registry publication directory must not be writable by group or other users")
	}
	lockPath := targetPath + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit export revocation source registry publication lock: %w", err)
	}
	lockPathInfo, err := os.Lstat(lockPath)
	if err != nil {
		lock.Close()
		return nil, fmt.Errorf("inspect audit export revocation source registry publication lock: %w", err)
	}
	lockInfo, err := lock.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0o077 != 0 ||
		!os.SameFile(lockPathInfo, lockInfo) {
		lock.Close()
		return nil, errors.New("audit export revocation source registry publication lock is unsafe")
	}
	ticker := time.NewTicker(revocationSourceRegistryPublicationLockPollInterval)
	defer ticker.Stop()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			lock.Close()
			return nil, fmt.Errorf("lock audit export revocation source registry publication: %w", err)
		}
		select {
		case <-ctx.Done():
			lock.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func releaseRevocationSourceRegistryPublicationLock(lock *os.File) {
	if lock == nil {
		return
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_ = lock.Close()
}

type revocationSourceRegistryPublicationTarget struct {
	exists bool
	info   os.FileInfo
}

func inspectRevocationSourceRegistryPublicationTarget(path string) (revocationSourceRegistryPublicationTarget, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return revocationSourceRegistryPublicationTarget{}, nil
	}
	if err != nil {
		return revocationSourceRegistryPublicationTarget{}, fmt.Errorf("inspect audit export revocation source registry publication target: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return revocationSourceRegistryPublicationTarget{}, errors.New("audit export revocation source registry publication target is unsafe")
	}
	return revocationSourceRegistryPublicationTarget{exists: true, info: info}, nil
}

func requireUnchangedRevocationSourceRegistryPublicationTarget(
	path string,
	expected revocationSourceRegistryPublicationTarget,
) error {
	actual, err := inspectRevocationSourceRegistryPublicationTarget(path)
	if err != nil {
		return err
	}
	if actual.exists != expected.exists ||
		(actual.exists && (!os.SameFile(actual.info, expected.info) || actual.info.Size() != expected.info.Size() ||
			!actual.info.ModTime().Equal(expected.info.ModTime()) || actual.info.Mode() != expected.info.Mode())) {
		return errors.New("audit export revocation source registry publication target changed during publication")
	}
	return nil
}

func writeAtomicRevocationSourceRegistryPublication(
	ctx context.Context,
	targetPath string,
	content []byte,
	expected revocationSourceRegistryPublicationTarget,
) error {
	directory := filepath.Dir(targetPath)
	temporary, err := os.CreateTemp(directory, ".dataground-audit-revocation-registry-*")
	if err != nil {
		return fmt.Errorf("create audit export revocation source registry publication: %w", err)
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
		return fmt.Errorf("secure audit export revocation source registry publication: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write audit export revocation source registry publication: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync audit export revocation source registry publication: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close audit export revocation source registry publication: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := requireUnchangedRevocationSourceRegistryPublicationTarget(targetPath, expected); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		return fmt.Errorf("install audit export revocation source registry publication: %w", err)
	}
	committed = true
	return syncRevocationSourceRegistryPublicationDirectory(targetPath)
}

func syncRevocationSourceRegistryPublicationDirectory(targetPath string) error {
	directory, err := os.Open(filepath.Dir(targetPath))
	if err != nil {
		return fmt.Errorf("%w: open publication directory: %v", ErrRevocationSourceRegistryPublicationUncertain, err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("%w: sync publication directory: %v", ErrRevocationSourceRegistryPublicationUncertain, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("%w: close publication directory: %v", ErrRevocationSourceRegistryPublicationUncertain, closeErr)
	}
	return nil
}
