package canarylauncher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const topologyDirectoryPrefix = "dg-canary-topology-"

type topologyWorkspace struct {
	mu            sync.Mutex
	root          string
	path          string
	composePath   string
	gatewayPath   string
	parent        *os.File
	directory     *os.File
	directoryInfo os.FileInfo
	composeInfo   os.FileInfo
	gatewayInfo   os.FileInfo
	removed       bool
}

func openTopologyWorkspace(
	root string,
	runID string,
	compose []byte,
	gateway []byte,
) (*topologyWorkspace, error) {
	if !runIDPattern.MatchString(runID) ||
		root == "" ||
		!filepath.IsAbs(root) ||
		len(compose) == 0 ||
		len(gateway) == 0 {
		return nil, ErrInvalidConfiguration
	}
	parent, err := os.Open(root)
	if err != nil {
		return nil, ErrLaunch
	}
	parentInfo, err := parent.Stat()
	if err != nil ||
		!parentInfo.IsDir() ||
		parentInfo.Mode().Perm() != 0o700 {
		_ = parent.Close()
		return nil, ErrLaunch
	}
	path := filepath.Join(root, topologyDirectoryPrefix+runID)
	if err := os.Mkdir(path, 0o700); err != nil {
		_ = parent.Close()
		return nil, ErrLaunch
	}
	directory, err := os.Open(path)
	if err != nil {
		_ = os.Remove(path)
		_ = parent.Close()
		return nil, ErrLaunch
	}
	directoryInfo, err := directory.Stat()
	if err != nil ||
		!directoryInfo.IsDir() ||
		directoryInfo.Mode().Perm() != 0o700 {
		_ = directory.Close()
		_ = os.Remove(path)
		_ = parent.Close()
		return nil, ErrLaunch
	}
	workspace := &topologyWorkspace{
		root:          root,
		path:          path,
		composePath:   filepath.Join(path, "docker-compose.yml"),
		gatewayPath:   filepath.Join(path, "gateway.toml"),
		parent:        parent,
		directory:     directory,
		directoryInfo: directoryInfo,
	}
	workspace.composeInfo, err = writeTopologyFile(workspace.composePath, compose)
	if err == nil {
		workspace.gatewayInfo, err = writeTopologyFile(workspace.gatewayPath, gateway)
	}
	if err == nil {
		err = errors.Join(directory.Sync(), parent.Sync())
	}
	if err != nil {
		workspace.removePartial()
		return nil, ErrLaunch
	}
	return workspace, nil
}

func writeTopologyFile(path string, content []byte) (os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm() != 0o600 {
		return nil, ErrLaunch
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	remove = false
	return info, nil
}

func (workspace *topologyWorkspace) ComposePath() string {
	if workspace == nil {
		return ""
	}
	return workspace.composePath
}

func (workspace *topologyWorkspace) Cleanup(ctx context.Context) error {
	if workspace == nil || ctx == nil {
		return ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrLaunch, err)
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.removed {
		return nil
	}
	rootInfo, err := os.Lstat(workspace.root)
	openedRoot, openedRootErr := workspace.parent.Stat()
	if err != nil ||
		openedRootErr != nil ||
		!os.SameFile(rootInfo, openedRoot) ||
		rootInfo.Mode()&os.ModeSymlink != 0 {
		return ErrLaunch
	}
	directoryInfo, err := os.Lstat(workspace.path)
	if err != nil ||
		!os.SameFile(directoryInfo, workspace.directoryInfo) ||
		directoryInfo.Mode()&os.ModeSymlink != 0 ||
		!directoryInfo.IsDir() {
		return ErrLaunch
	}
	for _, file := range []struct {
		path string
		info os.FileInfo
	}{
		{path: workspace.composePath, info: workspace.composeInfo},
		{path: workspace.gatewayPath, info: workspace.gatewayInfo},
	} {
		current, err := os.Lstat(file.path)
		if err != nil ||
			current.Mode()&os.ModeSymlink != 0 ||
			!current.Mode().IsRegular() ||
			!os.SameFile(current, file.info) {
			return ErrLaunch
		}
		if err := os.Remove(file.path); err != nil {
			return ErrLaunch
		}
	}
	if err := workspace.directory.Sync(); err != nil {
		return ErrLaunch
	}
	if err := os.Remove(workspace.path); err != nil {
		return ErrLaunch
	}
	if err := workspace.parent.Sync(); err != nil {
		return ErrLaunch
	}
	workspace.removed = true
	return errors.Join(workspace.directory.Close(), workspace.parent.Close())
}

func (workspace *topologyWorkspace) removePartial() {
	_ = os.Remove(workspace.composePath)
	_ = os.Remove(workspace.gatewayPath)
	_ = workspace.directory.Close()
	_ = os.Remove(workspace.path)
	_ = workspace.parent.Sync()
	_ = workspace.parent.Close()
}

func (topologyWorkspace) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var _ json.Marshaler = topologyWorkspace{}
