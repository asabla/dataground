package canaryworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/asabla/dataground/internal/security/canaryevidence"
)

const workspacePrefix = "dg-canary-"

var (
	ErrInvalidConfiguration   = errors.New("invalid credential verifier workspace configuration")
	ErrWorkspaceExists        = errors.New("credential verifier workspace already exists")
	ErrWorkspaceUnsafe        = errors.New("credential verifier workspace is unsafe")
	ErrWorkspaceFailure       = errors.New("credential verifier workspace operation failed")
	ErrWorkspaceNotEmpty      = errors.New("credential verifier workspace is not empty")
	ErrWorkspaceUncertain     = errors.New("credential verifier workspace removal is uncertain")
	ErrWorkspaceSerialization = errors.New("credential verifier workspace cannot be serialized")
	runIDPattern              = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

type Config struct {
	Root  string
	RunID string
}

// Workspace is a fresh, private filesystem identity for one credential
// evidence run. Its host path and file handles never cross the boundary.
type Workspace struct {
	mu        sync.Mutex
	runID     string
	name      string
	path      string
	parent    *os.File
	directory *os.File
	removed   bool
}

// Open requires a pre-existing deployment-owned parent and creates exactly one
// run-bound workspace. Existing paths are never adopted or reclaimed.
func Open(config Config) (*Workspace, error) {
	if !runIDPattern.MatchString(config.RunID) || config.Root == "" || !filepath.IsAbs(config.Root) {
		return nil, ErrInvalidConfiguration
	}
	root := filepath.Clean(config.Root)
	if root == string(filepath.Separator) {
		return nil, ErrInvalidConfiguration
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, ErrWorkspaceFailure
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 ||
		!ownedByCurrentUser(info) {
		return nil, ErrWorkspaceUnsafe
	}
	parent, err := os.Open(root)
	if err != nil {
		return nil, ErrWorkspaceFailure
	}
	openedRoot, err := parent.Stat()
	if err != nil || !os.SameFile(info, openedRoot) {
		_ = parent.Close()
		return nil, ErrWorkspaceUnsafe
	}

	name := workspacePrefix + config.RunID
	path := filepath.Join(root, name)
	if _, err := os.Lstat(path); err == nil {
		_ = parent.Close()
		return nil, ErrWorkspaceExists
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = parent.Close()
		return nil, ErrWorkspaceFailure
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		_ = parent.Close()
		if errors.Is(err, os.ErrExist) {
			return nil, ErrWorkspaceExists
		}
		return nil, ErrWorkspaceFailure
	}
	removeCreated := func() {
		_ = os.Remove(path)
		_ = parent.Sync()
		_ = parent.Close()
	}
	created, err := os.Lstat(path)
	if err != nil || created.Mode()&os.ModeSymlink != 0 || !created.IsDir() ||
		created.Mode().Perm() != 0o700 || !ownedByCurrentUser(created) {
		removeCreated()
		return nil, ErrWorkspaceUnsafe
	}
	directory, err := os.Open(path)
	if err != nil {
		removeCreated()
		return nil, ErrWorkspaceFailure
	}
	opened, err := directory.Stat()
	if err != nil || !os.SameFile(created, opened) {
		_ = directory.Close()
		removeCreated()
		return nil, ErrWorkspaceUnsafe
	}
	if err := parent.Sync(); err != nil {
		_ = directory.Close()
		removeCreated()
		return nil, ErrWorkspaceFailure
	}
	return &Workspace{
		runID:     config.RunID,
		name:      name,
		path:      path,
		parent:    parent,
		directory: directory,
	}, nil
}

func (workspace *Workspace) Name() string {
	if workspace == nil {
		return ""
	}
	return workspace.name
}

// Cleanup returns a canary-evidence cleanup adapter that accepts only the
// exact run-owned workspace identity. A missing workspace is safe only after
// this instance recorded a successful removal.
func (workspace *Workspace) Cleanup(ctx context.Context, request canaryevidence.CleanupRequest) error {
	if workspace == nil || ctx == nil ||
		request.RunID != workspace.runID ||
		request.ResourceKind != "workspace" ||
		request.ResourceName != workspace.name {
		return ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrWorkspaceFailure, err)
	}

	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.removed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrWorkspaceFailure, err)
	}
	info, err := os.Lstat(workspace.path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrWorkspaceUncertain
	}
	if err != nil {
		return ErrWorkspaceFailure
	}
	opened, err := workspace.directory.Stat()
	if err != nil || !os.SameFile(info, opened) || info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() || info.Mode().Perm() != 0o700 || !ownedByCurrentUser(info) {
		return ErrWorkspaceUnsafe
	}
	entries, err := os.ReadDir(workspace.path)
	if err != nil {
		return ErrWorkspaceFailure
	}
	if len(entries) != 0 {
		return ErrWorkspaceNotEmpty
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrWorkspaceFailure, err)
	}
	if err := os.Remove(workspace.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrWorkspaceUncertain
		}
		return ErrWorkspaceFailure
	}
	if err := workspace.parent.Sync(); err != nil {
		return ErrWorkspaceUncertain
	}
	workspace.removed = true
	return errors.Join(workspace.directory.Close(), workspace.parent.Close())
}

func (workspace *Workspace) MarshalJSON() ([]byte, error) {
	return nil, ErrWorkspaceSerialization
}

var (
	_ json.Marshaler             = (*Workspace)(nil)
	_ canaryevidence.CleanupFunc = (&Workspace{}).Cleanup
)
