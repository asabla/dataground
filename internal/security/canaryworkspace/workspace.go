package canaryworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	state *workspaceState
}

type workspaceState struct {
	mu         sync.Mutex
	runID      string
	name       string
	root       string
	path       string
	parent     *os.File
	directory  *os.File
	removed    bool
	cleanupErr error
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
	currentRoot, err := os.Lstat(root)
	if err != nil {
		_ = parent.Close()
		return nil, ErrWorkspaceFailure
	}
	if !os.SameFile(openedRoot, currentRoot) {
		_ = parent.Close()
		return nil, ErrWorkspaceUnsafe
	}
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
	created, err := os.Lstat(path)
	if err != nil || created.Mode()&os.ModeSymlink != 0 || !created.IsDir() ||
		created.Mode().Perm() != 0o700 || !ownedByCurrentUser(created) {
		_ = parent.Close()
		return nil, ErrWorkspaceUnsafe
	}
	directory, err := os.Open(path)
	if err != nil {
		_ = parent.Close()
		return nil, ErrWorkspaceFailure
	}
	opened, err := directory.Stat()
	if err != nil || !os.SameFile(created, opened) {
		_ = directory.Close()
		_ = parent.Close()
		return nil, ErrWorkspaceUnsafe
	}
	currentRoot, err = os.Lstat(root)
	if err != nil || !os.SameFile(openedRoot, currentRoot) ||
		currentRoot.Mode()&os.ModeSymlink != 0 || !currentRoot.IsDir() ||
		currentRoot.Mode().Perm() != 0o700 || !ownedByCurrentUser(currentRoot) {
		_ = directory.Close()
		_ = parent.Close()
		return nil, ErrWorkspaceUnsafe
	}
	removeVerified := func() {
		_ = directory.Close()
		_ = os.Remove(path)
		_ = parent.Sync()
		_ = parent.Close()
	}
	if err := parent.Sync(); err != nil {
		removeVerified()
		return nil, ErrWorkspaceFailure
	}
	return &Workspace{
		state: &workspaceState{
			runID:     config.RunID,
			name:      name,
			root:      root,
			path:      path,
			parent:    parent,
			directory: directory,
		},
	}, nil
}

func (workspace *Workspace) Name() string {
	if workspace == nil || workspace.state == nil {
		return ""
	}
	return workspace.state.name
}

// Cleanup returns a canary-evidence cleanup adapter that accepts only the
// exact run-owned workspace identity. A missing workspace is safe only after
// this instance recorded a successful removal.
func (workspace *Workspace) Cleanup(ctx context.Context, request canaryevidence.CleanupRequest) error {
	if workspace == nil || workspace.state == nil || ctx == nil {
		return ErrInvalidConfiguration
	}
	state := workspace.state
	if request.RunID != state.runID ||
		request.ResourceKind != "workspace" ||
		request.ResourceName != state.name {
		return ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrWorkspaceFailure, err)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.removed {
		return state.cleanupErr
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrWorkspaceFailure, err)
	}
	rootInfo, err := os.Lstat(state.root)
	if errors.Is(err, os.ErrNotExist) {
		return ErrWorkspaceUncertain
	}
	if err != nil {
		return ErrWorkspaceFailure
	}
	openedRoot, err := state.parent.Stat()
	if err != nil || !os.SameFile(rootInfo, openedRoot) || rootInfo.Mode()&os.ModeSymlink != 0 ||
		!rootInfo.IsDir() || rootInfo.Mode().Perm() != 0o700 || !ownedByCurrentUser(rootInfo) {
		return ErrWorkspaceUnsafe
	}
	info, err := os.Lstat(state.path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrWorkspaceUncertain
	}
	if err != nil {
		return ErrWorkspaceFailure
	}
	opened, err := state.directory.Stat()
	if err != nil || !os.SameFile(info, opened) || info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() || info.Mode().Perm() != 0o700 || !ownedByCurrentUser(info) {
		return ErrWorkspaceUnsafe
	}
	_, err = state.directory.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return ErrWorkspaceFailure
	}
	if err == nil {
		return ErrWorkspaceNotEmpty
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrWorkspaceFailure, err)
	}
	if err := os.Remove(state.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrWorkspaceUncertain
		}
		return ErrWorkspaceFailure
	}
	if err := state.parent.Sync(); err != nil {
		return ErrWorkspaceUncertain
	}
	state.removed = true
	state.cleanupErr = errors.Join(state.directory.Close(), state.parent.Close())
	return state.cleanupErr
}

func (Workspace) MarshalJSON() ([]byte, error) {
	return nil, ErrWorkspaceSerialization
}

var (
	_ json.Marshaler             = Workspace{}
	_ canaryevidence.CleanupFunc = (&Workspace{}).Cleanup
)
