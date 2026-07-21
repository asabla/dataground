package pgrouteproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const (
	routeStateVersion  = 1
	maxRouteStateBytes = 512
)

var errRouteStateOutcomeUnknown = errors.New("PostgreSQL route state outcome is unknown")

type persistedRouteState struct {
	Version             int    `json:"version"`
	PrimaryTarget       string `json:"primaryTarget"`
	PromotedTarget      string `json:"promotedTarget"`
	Route               Route  `json:"route"`
	PromotionGeneration uint64 `json:"promotionGeneration"`
}

type routeStateStore struct {
	path     string
	lockFile *os.File
}

func openRouteStateStore(path string, controlSocket string) (*routeStateStore, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("invalid PostgreSQL route state path")
	}
	if path == controlSocket {
		return nil, errors.New("PostgreSQL route state and control socket paths must be distinct")
	}
	directory := filepath.Dir(path)
	if directory != filepath.Dir(controlSocket) {
		return nil, errors.New("PostgreSQL route state and control socket must share a private directory")
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 ||
		directoryInfo.Mode().Perm() != 0o700 || !routePathOwnedByCurrentUser(directoryInfo) {
		return nil, errors.New("invalid PostgreSQL route state directory")
	}

	lockPath := path + ".lock"
	preexisting, statErr := os.Lstat(lockPath)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, errors.New("inspect PostgreSQL route state lock")
	}
	if statErr == nil && !validPrivateRouteFile(preexisting) {
		return nil, errors.New("invalid PostgreSQL route state lock")
	}
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.New("open PostgreSQL route state lock")
	}
	lockInfo, err := lockFile.Stat()
	if err != nil || !validPrivateRouteFile(lockInfo) ||
		(statErr == nil && !os.SameFile(preexisting, lockInfo)) {
		_ = lockFile.Close()
		return nil, errors.New("validate PostgreSQL route state lock")
	}
	if err := lockRouteState(lockFile); err != nil {
		_ = lockFile.Close()
		return nil, errors.New("lock PostgreSQL route state")
	}
	return &routeStateStore{path: path, lockFile: lockFile}, nil
}

func (store *routeStateStore) close() error {
	if store == nil || store.lockFile == nil {
		return nil
	}
	unlockErr := unlockRouteState(store.lockFile)
	closeErr := store.lockFile.Close()
	store.lockFile = nil
	if unlockErr != nil || closeErr != nil {
		return errors.New("close PostgreSQL route state lock")
	}
	return nil
}

func (store *routeStateStore) exists() (bool, error) {
	info, err := os.Lstat(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("inspect PostgreSQL route state")
	}
	if !validPrivateRouteFile(info) {
		return false, errors.New("invalid PostgreSQL route state file")
	}
	return true, nil
}

func (store *routeStateStore) read(primaryTarget string, promotedTarget string) (persistedRouteState, error) {
	exists, err := store.exists()
	if err != nil {
		return persistedRouteState{}, err
	}
	if !exists {
		return persistedRouteState{}, errors.New("PostgreSQL route state is unavailable")
	}
	file, err := os.Open(store.path)
	if err != nil {
		return persistedRouteState{}, errors.New("open PostgreSQL route state")
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxRouteStateBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(content) > maxRouteStateBytes {
		return persistedRouteState{}, errors.New("read PostgreSQL route state")
	}
	var state persistedRouteState
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return persistedRouteState{}, errors.New("decode PostgreSQL route state")
	}
	if err := requireJSONEnd(decoder); err != nil {
		return persistedRouteState{}, errors.New("decode PostgreSQL route state")
	}
	canonical, err := encodeRouteState(state)
	if err != nil || !bytes.Equal(content, canonical) ||
		state.Version != routeStateVersion || !validRoute(state.Route) ||
		state.PromotionGeneration == 0 || state.PrimaryTarget != primaryTarget ||
		state.PromotedTarget != promotedTarget {
		return persistedRouteState{}, errors.New("invalid PostgreSQL route state")
	}
	return state, nil
}

func (store *routeStateStore) write(state persistedRouteState, replacing bool) error {
	content, err := encodeRouteState(state)
	if err != nil || len(content) > maxRouteStateBytes {
		return errors.New("encode PostgreSQL route state")
	}
	exists, inspectErr := store.exists()
	if inspectErr != nil {
		return inspectErr
	}
	if exists != replacing {
		return errors.New("PostgreSQL route state changed before replacement")
	}

	directory := filepath.Dir(store.path)
	temporary, err := os.CreateTemp(directory, ".route-state-*")
	if err != nil {
		return errors.New("create PostgreSQL route state replacement")
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return errors.New("protect PostgreSQL route state replacement")
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return errors.New("write PostgreSQL route state replacement")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("sync PostgreSQL route state replacement")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close PostgreSQL route state replacement")
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return errors.New("replace PostgreSQL route state")
	}
	removeTemporary = false
	directoryFile, err := os.Open(directory)
	if err != nil {
		return errRouteStateOutcomeUnknown
	}
	syncErr := directoryFile.Sync()
	closeErr := directoryFile.Close()
	if syncErr != nil || closeErr != nil {
		return errRouteStateOutcomeUnknown
	}
	return nil
}

func encodeRouteState(state persistedRouteState) ([]byte, error) {
	content, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return errors.New("trailing PostgreSQL route state")
}

func validPrivateRouteFile(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm() == 0o600 &&
		routePathOwnedByCurrentUser(info) && routePathSingleLink(info)
}
