package openshell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	policyFilePrefix = "dataground-enforcement-"
	policyFileSuffix = ".yaml"
	workspaceLock    = ".dataground-policy-workspace.lock"
)

var (
	ErrPolicyWorkspaceUnavailable = errors.New("OpenShell policy workspace is unavailable")
	ErrPolicyWorkspaceBusy        = errors.New("OpenShell policy workspace is already in use")
	ErrPolicyWorkspaceUnsafe      = errors.New("OpenShell policy workspace is unsafe")
	ErrPolicyWorkspaceFailure     = errors.New("OpenShell policy workspace operation failed")
)

// PolicyWorkspace owns the short-lived named files required by the pinned
// OpenShell CLI. Its advisory lock makes startup orphan cleanup safe across
// worker crashes and prevents two processes from sharing the same directory.
type PolicyWorkspace struct {
	mu        sync.Mutex
	root      string
	directory *os.File
	lock      *os.File
	active    int
	closed    bool
}

// OpenPolicyWorkspace acquires an exclusive private directory and removes only
// regular policy files left by a prior crashed owner. Unexpected content fails
// closed instead of being deleted.
func OpenPolicyWorkspace(root string) (*PolicyWorkspace, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: root must be a non-root absolute path", ErrPolicyWorkspaceUnsafe)
	}
	root = filepath.Clean(root)
	if root == string(filepath.Separator) {
		return nil, fmt.Errorf("%w: root must be a non-root absolute path", ErrPolicyWorkspaceUnsafe)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, ErrPolicyWorkspaceFailure
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, ErrPolicyWorkspaceFailure
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 || !policyWorkspaceOwnedByCurrentUser(info) {
		return nil, fmt.Errorf("%w: root must be a mode-0700 directory", ErrPolicyWorkspaceUnsafe)
	}

	directory, err := os.Open(root)
	if err != nil {
		return nil, ErrPolicyWorkspaceFailure
	}
	openedInfo, err := directory.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = directory.Close()
		return nil, ErrPolicyWorkspaceUnsafe
	}
	lockPath := filepath.Join(root, workspaceLock)
	priorLockInfo, priorLockErr := os.Lstat(lockPath)
	if priorLockErr != nil && !errors.Is(priorLockErr, os.ErrNotExist) {
		_ = directory.Close()
		return nil, ErrPolicyWorkspaceFailure
	}
	if priorLockErr == nil && (priorLockInfo.Mode()&os.ModeSymlink != 0 || !priorLockInfo.Mode().IsRegular()) {
		_ = directory.Close()
		return nil, fmt.Errorf("%w: lock must not be a symlink", ErrPolicyWorkspaceUnsafe)
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = directory.Close()
		return nil, ErrPolicyWorkspaceFailure
	}
	lockInfo, err := lock.Stat()
	if err != nil {
		_ = lock.Close()
		_ = directory.Close()
		return nil, ErrPolicyWorkspaceFailure
	}
	if !securePolicyWorkspaceFile(lockInfo) {
		_ = lock.Close()
		_ = directory.Close()
		return nil, fmt.Errorf("%w: lock must be a mode-0600 regular file", ErrPolicyWorkspaceUnsafe)
	}
	if priorLockErr == nil && !os.SameFile(priorLockInfo, lockInfo) {
		_ = lock.Close()
		_ = directory.Close()
		return nil, fmt.Errorf("%w: lock changed during acquisition", ErrPolicyWorkspaceUnsafe)
	}
	if err := lockPolicyWorkspace(lock); err != nil {
		_ = lock.Close()
		_ = directory.Close()
		if errors.Is(err, errPolicyWorkspaceLocked) {
			return nil, ErrPolicyWorkspaceBusy
		}
		return nil, ErrPolicyWorkspaceFailure
	}

	workspace := &PolicyWorkspace{root: root, directory: directory, lock: lock}
	if err := workspace.reclaimOrphans(); err != nil {
		_ = unlockPolicyWorkspace(lock)
		_ = lock.Close()
		_ = directory.Close()
		return nil, err
	}
	return workspace, nil
}

// materialize persists one verified policy with owner-only access. Cleanup is
// idempotent and reports deletion or directory-sync failures to the caller.
func (workspace *PolicyWorkspace) materialize(content []byte) (string, func() error, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.closed {
		return "", nil, ErrPolicyWorkspaceUnavailable
	}
	file, err := os.CreateTemp(workspace.root, policyFilePrefix+"*"+policyFileSuffix)
	if err != nil {
		return "", nil, ErrPolicyWorkspaceFailure
	}
	path := file.Name()
	removePartial := func() {
		_ = file.Close()
		_ = os.Remove(path)
		_ = workspace.directory.Sync()
	}
	if err := file.Chmod(0o600); err != nil {
		removePartial()
		return "", nil, ErrPolicyWorkspaceFailure
	}
	if _, err := file.Write(content); err != nil {
		removePartial()
		return "", nil, ErrPolicyWorkspaceFailure
	}
	if err := file.Sync(); err != nil {
		removePartial()
		return "", nil, ErrPolicyWorkspaceFailure
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		_ = workspace.directory.Sync()
		return "", nil, ErrPolicyWorkspaceFailure
	}
	if err := workspace.directory.Sync(); err != nil {
		_ = os.Remove(path)
		_ = workspace.directory.Sync()
		return "", nil, ErrPolicyWorkspaceFailure
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
				cleanupErr = ErrPolicyWorkspaceFailure
			}
		})
		return cleanupErr
	}
	return path, cleanup, nil
}

// Close releases ownership. It refuses to close while a CLI call can still be
// reading a materialized policy.
func (workspace *PolicyWorkspace) Close() error {
	workspace.mu.Lock()
	if workspace.closed {
		workspace.mu.Unlock()
		return nil
	}
	if workspace.active != 0 {
		workspace.mu.Unlock()
		return ErrPolicyWorkspaceBusy
	}
	workspace.closed = true
	workspace.mu.Unlock()
	return errors.Join(
		unlockPolicyWorkspace(workspace.lock),
		workspace.lock.Close(),
		workspace.directory.Close(),
	)
}

func (workspace *PolicyWorkspace) reclaimOrphans() error {
	entries, err := os.ReadDir(workspace.root)
	if err != nil {
		return ErrPolicyWorkspaceFailure
	}
	removed := false
	for _, entry := range entries {
		name := entry.Name()
		if name == workspaceLock {
			continue
		}
		if !strings.HasPrefix(name, policyFilePrefix) || !strings.HasSuffix(name, policyFileSuffix) {
			return fmt.Errorf("%w: unexpected workspace entry", ErrPolicyWorkspaceUnsafe)
		}
		info, err := entry.Info()
		if err != nil {
			return ErrPolicyWorkspaceFailure
		}
		if !securePolicyWorkspaceFile(info) {
			return fmt.Errorf("%w: managed entries must be mode-0600 regular files", ErrPolicyWorkspaceUnsafe)
		}
		if err := os.Remove(filepath.Join(workspace.root, name)); err != nil {
			return ErrPolicyWorkspaceFailure
		}
		removed = true
	}
	if removed {
		if err := workspace.directory.Sync(); err != nil {
			return ErrPolicyWorkspaceFailure
		}
	}
	return nil
}

func securePolicyWorkspaceFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm() == 0o600 &&
		policyWorkspaceOwnedByCurrentUser(info) && policyWorkspaceSingleLink(info)
}
