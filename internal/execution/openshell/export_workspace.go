package openshell

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	exportFilePrefix = "dataground-export-"
	exportFileSuffix = ".artifact"
	exportWorkspaceLock = ".dataground-export-workspace.lock"
)

var (
	ErrExportWorkspaceUnavailable = errors.New("OpenShell export workspace is unavailable")
	ErrExportWorkspaceBusy = errors.New("OpenShell export workspace is already in use")
	ErrExportWorkspaceUnsafe = errors.New("OpenShell export workspace is unsafe")
	ErrExportWorkspaceFailure = errors.New("OpenShell export workspace operation failed")
	ErrExportTooLarge = errors.New("OpenShell export exceeds the configured limit")
)

// ExportWorkspace owns the short-lived host destinations required by the
// pinned OpenShell CLI. It never exposes those destinations outside the
// provider and returns content only after verified cleanup.
type ExportWorkspace struct {
	mu sync.Mutex
	root string
	directory *os.File
	lock *os.File
	maximumBytes int64
	active int
	closed bool
}

func OpenExportWorkspace(root string, maximumBytes int64) (*ExportWorkspace, error) {
	if root == "" || !filepath.IsAbs(root) || maximumBytes <= 0 {
		return nil, fmt.Errorf("%w: absolute root and positive maximum are required", ErrExportWorkspaceUnsafe)
	}
	root = filepath.Clean(root)
	if root == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: root must be non-root", ErrExportWorkspaceUnsafe)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, ErrExportWorkspaceFailure
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
		info.Mode().Perm() != 0o700 || !policyWorkspaceOwnedByCurrentUser(info) {
		return nil, fmt.Errorf("%w: root must be an owned mode-0700 directory", ErrExportWorkspaceUnsafe)
	}
	directory, err := os.Open(root)
	if err != nil {
		return nil, ErrExportWorkspaceFailure
	}
	opened, err := directory.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = directory.Close()
		return nil, ErrExportWorkspaceUnsafe
	}
	lockPath := filepath.Join(root, exportWorkspaceLock)
	prior, priorErr := os.Lstat(lockPath)
	if priorErr != nil && !errors.Is(priorErr, os.ErrNotExist) {
		_ = directory.Close()
		return nil, ErrExportWorkspaceFailure
	}
	if priorErr == nil && (prior.Mode()&os.ModeSymlink != 0 || !prior.Mode().IsRegular()) {
		_ = directory.Close()
		return nil, ErrExportWorkspaceUnsafe
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = directory.Close()
		return nil, ErrExportWorkspaceFailure
	}
	lockInfo, err := lock.Stat()
	if err != nil || !securePolicyWorkspaceFile(lockInfo) ||
		(priorErr == nil && !os.SameFile(prior, lockInfo)) {
		_ = lock.Close()
		_ = directory.Close()
		return nil, ErrExportWorkspaceUnsafe
	}
	if err := lockPolicyWorkspace(lock); err != nil {
		_ = lock.Close()
		_ = directory.Close()
		if errors.Is(err, errPolicyWorkspaceLocked) {
			return nil, ErrExportWorkspaceBusy
		}
		return nil, ErrExportWorkspaceFailure
	}
	workspace := &ExportWorkspace{
		root: root, directory: directory, lock: lock, maximumBytes: maximumBytes,
	}
	if err := workspace.reclaimOrphans(); err != nil {
		_ = unlockPolicyWorkspace(lock)
		_ = lock.Close()
		_ = directory.Close()
		return nil, err
	}
	return workspace, nil
}

func (workspace *ExportWorkspace) destination() (string, func() error, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.closed {
		return "", nil, ErrExportWorkspaceUnavailable
	}
	file, err := os.CreateTemp(workspace.root, exportFilePrefix+"*"+exportFileSuffix)
	if err != nil {
		return "", nil, ErrExportWorkspaceFailure
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", nil, ErrExportWorkspaceFailure
	}
	if err := os.Remove(path); err != nil {
		return "", nil, ErrExportWorkspaceFailure
	}
	workspace.active++
	var once sync.Once
	var cleanupErr error
	cleanup := func() error {
		once.Do(func() {
			removeErr := os.Remove(path)
			if errors.Is(removeErr, os.ErrNotExist) {
				removeErr = nil
			}
			syncErr := workspace.directory.Sync()
			workspace.mu.Lock()
			workspace.active--
			workspace.mu.Unlock()
			if removeErr != nil || syncErr != nil {
				cleanupErr = ErrExportWorkspaceFailure
			}
		})
		return cleanupErr
	}
	return path, cleanup, nil
}

func (workspace *ExportWorkspace) consume(path string) ([]byte, error) {
	if filepath.Dir(path) != workspace.root ||
		!strings.HasPrefix(filepath.Base(path), exportFilePrefix) ||
		!strings.HasSuffix(filepath.Base(path), exportFileSuffix) {
		return nil, ErrExportWorkspaceUnsafe
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, ErrExportWorkspaceFailure
	}
	if !securePolicyWorkspaceFile(info) {
		return nil, ErrExportWorkspaceUnsafe
	}
	if info.Size() > workspace.maximumBytes {
		return nil, ErrExportTooLarge
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrExportWorkspaceFailure
	}
	content, readErr := io.ReadAll(io.LimitReader(file, workspace.maximumBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, ErrExportWorkspaceFailure
	}
	if int64(len(content)) > workspace.maximumBytes {
		return nil, ErrExportTooLarge
	}
	return content, nil
}

func (workspace *ExportWorkspace) Close() error {
	workspace.mu.Lock()
	if workspace.closed {
		workspace.mu.Unlock()
		return nil
	}
	if workspace.active != 0 {
		workspace.mu.Unlock()
		return ErrExportWorkspaceBusy
	}
	workspace.closed = true
	workspace.mu.Unlock()
	return errors.Join(
		unlockPolicyWorkspace(workspace.lock),
		workspace.lock.Close(),
		workspace.directory.Close(),
	)
}

func (workspace *ExportWorkspace) reclaimOrphans() error {
	entries, err := os.ReadDir(workspace.root)
	if err != nil {
		return ErrExportWorkspaceFailure
	}
	removed := false
	for _, entry := range entries {
		name := entry.Name()
		if name == exportWorkspaceLock {
			continue
		}
		if !strings.HasPrefix(name, exportFilePrefix) || !strings.HasSuffix(name, exportFileSuffix) {
			return fmt.Errorf("%w: unexpected workspace entry", ErrExportWorkspaceUnsafe)
		}
		info, err := entry.Info()
		if err != nil || !securePolicyWorkspaceFile(info) {
			return ErrExportWorkspaceUnsafe
		}
		if err := os.Remove(filepath.Join(workspace.root, name)); err != nil {
			return ErrExportWorkspaceFailure
		}
		removed = true
	}
	if removed {
		if err := workspace.directory.Sync(); err != nil {
			return ErrExportWorkspaceFailure
		}
	}
	return nil
}
