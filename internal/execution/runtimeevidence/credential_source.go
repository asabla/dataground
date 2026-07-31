package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/asabla/dataground/internal/execution"
)

const runtimeCredentialSourceMaxBytes = 64 << 10

var (
	ErrCredentialSourceConfiguration = errors.New("invalid runtime credential source configuration")
	ErrCredentialSourceLoad          = errors.New("runtime credential source load failed")
	ErrCredentialSourceOrder         = errors.New("runtime credential source order is invalid")
	ErrCredentialSourceCleanup       = errors.New("runtime credential source cleanup failed")
)

var runtimeCredentialSourceNames = [...]string{
	"access_token",
	"refresh_token",
	"account_id",
	"id_token",
}

type CredentialSourceConfig struct {
	Directory string
}

// RuntimeCredentialSource adopts one fresh owner-only credential bundle. It
// returns credentials only after removing the exact files and bundle directory.
type RuntimeCredentialSource struct {
	state *runtimeCredentialSourceState
}

type runtimeCredentialSourceState struct {
	mu            sync.Mutex
	path          string
	parentPath    string
	parent        *os.File
	parentInfo    os.FileInfo
	directory     *os.File
	directoryInfo os.FileInfo
	files         [len(runtimeCredentialSourceNames)]*ownedCredentialFile
	read          runtimeCredentialRead
	started       bool
	loading       bool
	cleaning      bool
	consumed      bool
	failed        bool
}

type ownedCredentialFile struct {
	name    string
	path    string
	file    *os.File
	info    os.FileInfo
	removed bool
}

type runtimeCredentialRead func(
	context.Context,
	*ownedCredentialFile,
) ([]byte, error)

func NewRuntimeCredentialSource(
	config CredentialSourceConfig,
) (*RuntimeCredentialSource, error) {
	return newRuntimeCredentialSource(config, readRuntimeCredentialFile)
}

func newRuntimeCredentialSource(
	config CredentialSourceConfig,
	read runtimeCredentialRead,
) (*RuntimeCredentialSource, error) {
	if config.Directory == "" ||
		!filepath.IsAbs(config.Directory) ||
		read == nil {
		return nil, ErrCredentialSourceConfiguration
	}
	path := filepath.Clean(config.Directory)
	if path == string(filepath.Separator) {
		return nil, ErrCredentialSourceConfiguration
	}
	state, err := openRuntimeCredentialSource(path, read)
	if err != nil {
		return nil, err
	}
	return &RuntimeCredentialSource{state: state}, nil
}

func openRuntimeCredentialSource(
	path string,
	read runtimeCredentialRead,
) (*runtimeCredentialSourceState, error) {
	parentPath := filepath.Dir(path)
	parentInfo, err := os.Lstat(parentPath)
	if err != nil || !safeRuntimeCredentialParent(parentInfo) {
		return nil, ErrCredentialSourceConfiguration
	}
	parent, err := os.Open(parentPath)
	if err != nil {
		return nil, ErrCredentialSourceConfiguration
	}
	openedParent, err := parent.Stat()
	if err != nil || !os.SameFile(parentInfo, openedParent) {
		_ = parent.Close()
		return nil, ErrCredentialSourceConfiguration
	}

	directoryInfo, err := os.Lstat(path)
	if err != nil || !safeRuntimeCredentialDirectory(directoryInfo) {
		_ = parent.Close()
		return nil, ErrCredentialSourceConfiguration
	}
	directory, err := os.Open(path)
	if err != nil {
		_ = parent.Close()
		return nil, ErrCredentialSourceConfiguration
	}
	openedDirectory, err := directory.Stat()
	if err != nil || !os.SameFile(directoryInfo, openedDirectory) {
		_ = directory.Close()
		_ = parent.Close()
		return nil, ErrCredentialSourceConfiguration
	}

	state := &runtimeCredentialSourceState{
		path:          path,
		parentPath:    parentPath,
		parent:        parent,
		parentInfo:    openedParent,
		directory:     directory,
		directoryInfo: openedDirectory,
		read:          read,
	}
	fail := func() (*runtimeCredentialSourceState, error) {
		state.closeHandles()
		return nil, ErrCredentialSourceConfiguration
	}
	entries, err := os.ReadDir(path)
	if err != nil || !exactRuntimeCredentialEntries(entries) {
		return fail()
	}
	for index, name := range runtimeCredentialSourceNames {
		owned, err := openRuntimeCredentialFile(path, name)
		if err != nil {
			return fail()
		}
		state.files[index] = owned
	}
	if !state.validFilesystemIdentity() {
		return fail()
	}
	return state, nil
}

func openRuntimeCredentialFile(
	directory string,
	name string,
) (*ownedCredentialFile, error) {
	path := filepath.Join(directory, name)
	info, err := os.Lstat(path)
	if err != nil || !safeRuntimeCredentialFile(info) {
		return nil, ErrCredentialSourceConfiguration
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrCredentialSourceConfiguration
	}
	opened, err := file.Stat()
	current, currentErr := os.Lstat(path)
	if err != nil ||
		currentErr != nil ||
		!safeRuntimeCredentialFile(opened) ||
		!safeRuntimeCredentialFile(current) ||
		!os.SameFile(info, opened) ||
		!os.SameFile(opened, current) {
		_ = file.Close()
		return nil, ErrCredentialSourceConfiguration
	}
	return &ownedCredentialFile{
		name: name,
		path: path,
		file: file,
		info: opened,
	}, nil
}

func (source *RuntimeCredentialSource) Load(
	ctx context.Context,
) (execution.RuntimeConformanceCredentials, error) {
	if source == nil || source.state == nil || ctx == nil {
		return execution.RuntimeConformanceCredentials{}, ErrCredentialSourceConfiguration
	}
	state := source.state
	state.mu.Lock()
	if state.started || state.failed || state.consumed {
		state.failed = true
		state.mu.Unlock()
		return execution.RuntimeConformanceCredentials{}, errors.Join(
			ErrCredentialSourceLoad,
			ErrCredentialSourceOrder,
		)
	}
	state.started = true
	state.loading = true
	read := state.read
	files := state.files
	state.mu.Unlock()

	values := make([][]byte, len(files))
	var readErr error
	for index, file := range files {
		if state.invalidAfter(ctx) {
			readErr = ErrCredentialSourceOrder
			break
		}
		values[index], readErr = read(ctx, file)
		if readErr != nil {
			break
		}
	}
	credentials := execution.RuntimeConformanceCredentials{}
	if readErr == nil {
		credentials = execution.RuntimeConformanceCredentials{
			AccessToken:  values[0],
			RefreshToken: values[1],
			AccountID:    values[2],
			IDToken:      values[3],
		}
	}
	cleanupErr := state.consumeOwned()

	state.mu.Lock()
	state.loading = false
	if cleanupErr == nil {
		state.consumed = true
	}
	poisoned := state.failed
	if readErr != nil || cleanupErr != nil || ctx.Err() != nil {
		state.failed = true
	}
	state.mu.Unlock()

	if readErr != nil || cleanupErr != nil || poisoned || ctx.Err() != nil {
		clearRuntimeProviderCredentials(&credentials)
		for _, value := range values {
			clear(value)
		}
		if poisoned && readErr == nil {
			readErr = ErrCredentialSourceOrder
		}
		return execution.RuntimeConformanceCredentials{}, errors.Join(
			ErrCredentialSourceLoad,
			readErr,
			cleanupErr,
			ctx.Err(),
		)
	}
	return credentials, nil
}

// Cleanup consumes the exact adopted bundle without returning credentials. It
// remains available after a failed Load so a caller can retry uncertain removal.
func (source *RuntimeCredentialSource) Cleanup(ctx context.Context) error {
	if source == nil || source.state == nil || ctx == nil {
		return ErrCredentialSourceConfiguration
	}
	state := source.state
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrCredentialSourceCleanup, err)
	}
	state.mu.Lock()
	if state.consumed {
		state.mu.Unlock()
		return nil
	}
	if state.loading || state.cleaning {
		if state.loading {
			state.failed = true
		}
		state.mu.Unlock()
		return ErrCredentialSourceCleanup
	}
	state.started = true
	state.cleaning = true
	state.mu.Unlock()

	err := state.consumeOwned()
	state.mu.Lock()
	state.cleaning = false
	if err != nil {
		state.failed = true
	} else {
		state.consumed = true
	}
	state.mu.Unlock()
	if err != nil {
		return ErrCredentialSourceCleanup
	}
	return nil
}

func readRuntimeCredentialFile(
	ctx context.Context,
	owned *ownedCredentialFile,
) ([]byte, error) {
	if ctx == nil || owned == nil || owned.file == nil || ctx.Err() != nil {
		return nil, ErrCredentialSourceLoad
	}
	current, err := os.Lstat(owned.path)
	opened, openedErr := owned.file.Stat()
	if err != nil ||
		openedErr != nil ||
		!safeRuntimeCredentialFile(current) ||
		!safeRuntimeCredentialFile(opened) ||
		!os.SameFile(owned.info, current) ||
		!os.SameFile(owned.info, opened) {
		return nil, ErrCredentialSourceLoad
	}
	if _, err := owned.file.Seek(0, io.SeekStart); err != nil {
		return nil, ErrCredentialSourceLoad
	}
	value, err := io.ReadAll(io.LimitReader(
		owned.file,
		runtimeCredentialSourceMaxBytes+1,
	))
	if err != nil ||
		len(value) == 0 ||
		len(value) > runtimeCredentialSourceMaxBytes ||
		int64(len(value)) != owned.info.Size() ||
		ctx.Err() != nil {
		clear(value)
		return nil, ErrCredentialSourceLoad
	}
	after, err := owned.file.Stat()
	current, currentErr := os.Lstat(owned.path)
	if err != nil ||
		currentErr != nil ||
		!safeRuntimeCredentialFile(after) ||
		!safeRuntimeCredentialFile(current) ||
		after.Size() != int64(len(value)) ||
		!os.SameFile(owned.info, after) ||
		!os.SameFile(owned.info, current) {
		clear(value)
		return nil, ErrCredentialSourceLoad
	}
	return value, nil
}

func (state *runtimeCredentialSourceState) consumeOwned() error {
	if !state.validFilesystemIdentity() {
		return ErrCredentialSourceCleanup
	}
	var outcome error
	for _, owned := range state.files {
		if owned == nil || owned.removed {
			continue
		}
		current, err := os.Lstat(owned.path)
		if err != nil ||
			!safeRuntimeCredentialFile(current) ||
			!os.SameFile(owned.info, current) {
			outcome = errors.Join(outcome, ErrCredentialSourceCleanup)
			continue
		}
		if err := os.Remove(owned.path); err != nil {
			outcome = errors.Join(outcome, ErrCredentialSourceCleanup)
			continue
		}
		if _, err := os.Lstat(owned.path); !errors.Is(err, os.ErrNotExist) {
			outcome = errors.Join(outcome, ErrCredentialSourceCleanup)
			continue
		}
		owned.removed = true
		if err := owned.file.Close(); err != nil {
			outcome = errors.Join(outcome, ErrCredentialSourceCleanup)
		}
		owned.file = nil
	}
	if err := state.directory.Sync(); err != nil {
		outcome = errors.Join(outcome, ErrCredentialSourceCleanup)
	}
	if outcome != nil {
		return outcome
	}
	entries, err := os.ReadDir(state.path)
	if err != nil || len(entries) != 0 || !state.validFilesystemIdentity() {
		return ErrCredentialSourceCleanup
	}
	if err := os.Remove(state.path); err != nil {
		return ErrCredentialSourceCleanup
	}
	if _, err := os.Lstat(state.path); !errors.Is(err, os.ErrNotExist) {
		return ErrCredentialSourceCleanup
	}
	if err := state.parent.Sync(); err != nil {
		return ErrCredentialSourceCleanup
	}
	return errors.Join(state.directory.Close(), state.parent.Close())
}

func (state *runtimeCredentialSourceState) validFilesystemIdentity() bool {
	parent, parentErr := os.Lstat(state.parentPath)
	openedParent, openedParentErr := state.parent.Stat()
	directory, directoryErr := os.Lstat(state.path)
	openedDirectory, openedDirectoryErr := state.directory.Stat()
	return parentErr == nil &&
		openedParentErr == nil &&
		directoryErr == nil &&
		openedDirectoryErr == nil &&
		safeRuntimeCredentialParent(parent) &&
		safeRuntimeCredentialParent(openedParent) &&
		safeRuntimeCredentialDirectory(directory) &&
		safeRuntimeCredentialDirectory(openedDirectory) &&
		os.SameFile(state.parentInfo, parent) &&
		os.SameFile(state.parentInfo, openedParent) &&
		os.SameFile(state.directoryInfo, directory) &&
		os.SameFile(state.directoryInfo, openedDirectory)
}

func (state *runtimeCredentialSourceState) invalidAfter(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return true
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.failed
}

func (state *runtimeCredentialSourceState) closeHandles() {
	for _, owned := range state.files {
		if owned != nil && owned.file != nil {
			_ = owned.file.Close()
		}
	}
	if state.directory != nil {
		_ = state.directory.Close()
	}
	if state.parent != nil {
		_ = state.parent.Close()
	}
}

func exactRuntimeCredentialEntries(entries []os.DirEntry) bool {
	if len(entries) != len(runtimeCredentialSourceNames) {
		return false
	}
	expected := make(map[string]struct{}, len(runtimeCredentialSourceNames))
	for _, name := range runtimeCredentialSourceNames {
		expected[name] = struct{}{}
	}
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok {
			return false
		}
		delete(expected, entry.Name())
	}
	return len(expected) == 0
}

func safeRuntimeCredentialParent(info os.FileInfo) bool {
	return info != nil &&
		info.Mode()&os.ModeSymlink == 0 &&
		info.IsDir() &&
		info.Mode().Perm()&0o022 == 0 &&
		runtimeCredentialOwnedByCurrentUser(info)
}

func safeRuntimeCredentialDirectory(info os.FileInfo) bool {
	return info != nil &&
		info.Mode()&os.ModeSymlink == 0 &&
		info.IsDir() &&
		info.Mode().Perm() == 0o700 &&
		runtimeCredentialOwnedByCurrentUser(info)
}

func safeRuntimeCredentialFile(info os.FileInfo) bool {
	return info != nil &&
		info.Mode()&os.ModeSymlink == 0 &&
		info.Mode().IsRegular() &&
		info.Mode().Perm() == 0o600 &&
		info.Size() > 0 &&
		info.Size() <= runtimeCredentialSourceMaxBytes &&
		runtimeCredentialOwnedByCurrentUser(info) &&
		runtimeCredentialSingleLink(info)
}

func (CredentialSourceConfig) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (RuntimeCredentialSource) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var (
	_ json.Marshaler = CredentialSourceConfig{}
	_ json.Marshaler = RuntimeCredentialSource{}
)
