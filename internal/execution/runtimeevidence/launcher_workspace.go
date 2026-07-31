package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/asabla/dataground/internal/execution/openshell"
)

const (
	runtimeLauncherWorkspacePrefix = "dg-runtime-launcher-"
	runtimeLauncherPolicyDirectory = "policy"
	runtimeLauncherExportDirectory = "export"
	runtimeLauncherPolicyLock      = ".dataground-policy-workspace.lock"
	runtimeLauncherExportLock      = ".dataground-export-workspace.lock"
	runtimeLauncherExportMaxBytes  = 64 << 10
)

type launcherWorkspace interface {
	Cleanup(context.Context, CleanupRequest) error
}

type runtimeLauncherWorkspace struct {
	mu            sync.Mutex
	runID         string
	resourceName  string
	parentPath    string
	path          string
	policyPath    string
	exportPath    string
	parent        *os.File
	directory     *os.File
	parentInfo    os.FileInfo
	directoryInfo os.FileInfo
	policyInfo    os.FileInfo
	exportInfo    os.FileInfo
	policy        *openshell.PolicyWorkspace
	export        *openshell.ExportWorkspace
	policyRemoved bool
	exportRemoved bool
	directoryGone bool
	removed       bool
	cleaning      bool
}

func newRuntimeLauncherWorkspace(
	root string,
	runID string,
) (launcherWorkspace, error) {
	if !runIDPattern.MatchString(runID) {
		return nil, ErrLauncherConfiguration
	}
	parentPath, err := resolveRuntimeTopologyDirectory(root, true)
	if err != nil {
		return nil, ErrLauncherConfiguration
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil || !safeRuntimeCredentialDirectory(parentInfo) {
		return nil, ErrLauncherConfiguration
	}
	parent, err := os.Open(parentPath)
	if err != nil {
		return nil, ErrLauncherConfiguration
	}
	openedParent, err := parent.Stat()
	if err != nil || !os.SameFile(parentInfo, openedParent) {
		_ = parent.Close()
		return nil, ErrLauncherConfiguration
	}

	path := filepath.Join(parentPath, runtimeLauncherWorkspacePrefix+runID)
	if err := os.Mkdir(path, 0o700); err != nil {
		_ = parent.Close()
		return nil, ErrLauncherConfiguration
	}
	directory, err := os.Open(path)
	if err != nil {
		_ = os.Remove(path)
		_ = parent.Sync()
		_ = parent.Close()
		return nil, ErrLauncherConfiguration
	}
	directoryInfo, err := directory.Stat()
	current, currentErr := os.Lstat(path)
	if err != nil || currentErr != nil ||
		!safeRuntimeCredentialDirectory(directoryInfo) ||
		!safeRuntimeCredentialDirectory(current) ||
		!os.SameFile(directoryInfo, current) {
		_ = directory.Close()
		_ = os.Remove(path)
		_ = parent.Sync()
		_ = parent.Close()
		return nil, ErrLauncherConfiguration
	}
	workspace := &runtimeLauncherWorkspace{
		runID:         runID,
		resourceName:  namesForRun(runID).Workspace,
		parentPath:    parentPath,
		path:          path,
		policyPath:    filepath.Join(path, runtimeLauncherPolicyDirectory),
		exportPath:    filepath.Join(path, runtimeLauncherExportDirectory),
		parent:        parent,
		directory:     directory,
		parentInfo:    openedParent,
		directoryInfo: directoryInfo,
	}
	fail := func() (launcherWorkspace, error) {
		_ = workspace.removePartial()
		return nil, ErrLauncherConfiguration
	}
	policy, err := openshell.OpenPolicyWorkspace(workspace.policyPath)
	if err != nil {
		return fail()
	}
	workspace.policy = policy
	workspace.policyInfo, err = runtimeLauncherDirectoryIdentity(workspace.policyPath)
	if err != nil {
		return fail()
	}
	exported, err := openshell.OpenExportWorkspace(
		workspace.exportPath,
		runtimeLauncherExportMaxBytes,
	)
	if err != nil {
		return fail()
	}
	workspace.export = exported
	workspace.exportInfo, err = runtimeLauncherDirectoryIdentity(workspace.exportPath)
	if err != nil || parent.Sync() != nil {
		return fail()
	}
	return workspace, nil
}

func (workspace *runtimeLauncherWorkspace) Cleanup(
	ctx context.Context,
	request CleanupRequest,
) error {
	if workspace == nil || ctx == nil ||
		request.RunID != workspace.runID ||
		request.ResourceKind != "workspace" ||
		request.ResourceName != workspace.resourceName {
		return ErrLauncherCleanup
	}
	workspace.mu.Lock()
	if workspace.removed {
		workspace.mu.Unlock()
		return nil
	}
	if workspace.cleaning {
		workspace.mu.Unlock()
		return ErrLauncherCleanup
	}
	workspace.cleaning = true
	workspace.mu.Unlock()

	err := workspace.removeOwned()
	workspace.mu.Lock()
	workspace.cleaning = false
	if err == nil {
		workspace.removed = true
	}
	workspace.mu.Unlock()
	if err != nil {
		return ErrLauncherCleanup
	}
	return nil
}

func (workspace *runtimeLauncherWorkspace) removeOwned() error {
	if workspace.policy != nil {
		if err := workspace.policy.Close(); err != nil {
			return ErrLauncherCleanup
		}
	}
	if workspace.export != nil {
		if err := workspace.export.Close(); err != nil {
			return ErrLauncherCleanup
		}
	}
	if !workspace.policyRemoved {
		if err := removeRuntimeLauncherDirectory(
			workspace.policyPath,
			workspace.policyInfo,
			runtimeLauncherPolicyLock,
		); err != nil {
			return err
		}
		workspace.policyRemoved = true
	}
	if !workspace.exportRemoved {
		if err := removeRuntimeLauncherDirectory(
			workspace.exportPath,
			workspace.exportInfo,
			runtimeLauncherExportLock,
		); err != nil {
			return err
		}
		workspace.exportRemoved = true
	}
	if !workspace.directoryGone {
		if !workspace.validParentAndDirectory() {
			return ErrLauncherCleanup
		}
		entries, err := os.ReadDir(workspace.path)
		if err != nil || len(entries) != 0 || workspace.directory.Sync() != nil {
			return ErrLauncherCleanup
		}
		if err := os.Remove(workspace.path); err != nil {
			return ErrLauncherCleanup
		}
		if _, err := os.Lstat(workspace.path); !errors.Is(err, os.ErrNotExist) {
			return ErrLauncherCleanup
		}
		workspace.directoryGone = true
	}
	if !workspace.validParent() || workspace.parent.Sync() != nil {
		return ErrLauncherCleanup
	}
	_ = workspace.directory.Close()
	_ = workspace.parent.Close()
	return nil
}

func (workspace *runtimeLauncherWorkspace) removePartial() error {
	if workspace == nil {
		return nil
	}
	if workspace.export != nil {
		_ = workspace.export.Close()
	}
	if workspace.policy != nil {
		_ = workspace.policy.Close()
	}
	if workspace.exportInfo != nil {
		_ = removeRuntimeLauncherDirectory(
			workspace.exportPath,
			workspace.exportInfo,
			runtimeLauncherExportLock,
		)
	}
	if workspace.policyInfo != nil {
		_ = removeRuntimeLauncherDirectory(
			workspace.policyPath,
			workspace.policyInfo,
			runtimeLauncherPolicyLock,
		)
	}
	if workspace.directory != nil {
		_ = workspace.directory.Close()
	}
	_ = os.Remove(workspace.path)
	if workspace.parent != nil {
		_ = workspace.parent.Sync()
		_ = workspace.parent.Close()
	}
	return nil
}

func runtimeLauncherDirectoryIdentity(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || !safeRuntimeCredentialDirectory(info) {
		return nil, ErrLauncherConfiguration
	}
	return info, nil
}

func removeRuntimeLauncherDirectory(
	path string,
	expected os.FileInfo,
	lockName string,
) error {
	current, err := os.Lstat(path)
	if err != nil || expected == nil ||
		!safeRuntimeCredentialDirectory(current) ||
		!os.SameFile(expected, current) {
		return ErrLauncherCleanup
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 1 || entries[0].Name() != lockName {
		return ErrLauncherCleanup
	}
	lockPath := filepath.Join(path, lockName)
	lock, err := os.Lstat(lockPath)
	if err != nil || !safeRuntimeLauncherLock(lock) {
		return ErrLauncherCleanup
	}
	directory, err := os.Open(path)
	if err != nil {
		return ErrLauncherCleanup
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil || !os.SameFile(expected, opened) {
		return ErrLauncherCleanup
	}
	if err := os.Remove(lockPath); err != nil || directory.Sync() != nil {
		return ErrLauncherCleanup
	}
	if err := os.Remove(path); err != nil {
		return ErrLauncherCleanup
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return ErrLauncherCleanup
	}
	return nil
}

func safeRuntimeLauncherLock(info os.FileInfo) bool {
	return info != nil &&
		info.Mode()&os.ModeSymlink == 0 &&
		info.Mode().IsRegular() &&
		info.Mode().Perm() == 0o600 &&
		runtimeCredentialOwnedByCurrentUser(info) &&
		runtimeCredentialSingleLink(info)
}

func (workspace *runtimeLauncherWorkspace) validParentAndDirectory() bool {
	if !workspace.validParent() {
		return false
	}
	current, err := os.Lstat(workspace.path)
	opened, openedErr := workspace.directory.Stat()
	return err == nil &&
		openedErr == nil &&
		safeRuntimeCredentialDirectory(current) &&
		safeRuntimeCredentialDirectory(opened) &&
		os.SameFile(workspace.directoryInfo, current) &&
		os.SameFile(workspace.directoryInfo, opened)
}

func (workspace *runtimeLauncherWorkspace) validParent() bool {
	current, err := os.Lstat(workspace.parentPath)
	opened, openedErr := workspace.parent.Stat()
	return err == nil &&
		openedErr == nil &&
		safeRuntimeCredentialDirectory(current) &&
		safeRuntimeCredentialDirectory(opened) &&
		os.SameFile(workspace.parentInfo, current) &&
		os.SameFile(workspace.parentInfo, opened)
}

func (runtimeLauncherWorkspace) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var _ json.Marshaler = runtimeLauncherWorkspace{}
