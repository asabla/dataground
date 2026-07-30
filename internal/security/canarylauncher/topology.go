package canarylauncher

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const (
	topologyDirectoryPrefix = "dg-canary-topology-"
	gatewayJWTDirectoryName = "gateway-jwt"
)

type topologyWorkspace struct {
	mu            sync.Mutex
	root          string
	path          string
	composePath   string
	gatewayPath   string
	statePath     string
	jwtPath       string
	parent        *os.File
	directory     *os.File
	directoryInfo os.FileInfo
	composeInfo   os.FileInfo
	gatewayInfo   os.FileInfo
	stateInfo     os.FileInfo
	jwtInfo       os.FileInfo
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
		statePath:     filepath.Join(path, "gateway-state"),
		jwtPath:       filepath.Join(path, gatewayJWTDirectoryName),
		parent:        parent,
		directory:     directory,
		directoryInfo: directoryInfo,
	}
	if err := os.Mkdir(workspace.statePath, 0o700); err != nil {
		workspace.removePartial()
		return nil, ErrLaunch
	}
	workspace.stateInfo, err = os.Lstat(workspace.statePath)
	if err != nil ||
		workspace.stateInfo.Mode()&os.ModeSymlink != 0 ||
		!workspace.stateInfo.IsDir() ||
		workspace.stateInfo.Mode().Perm() != 0o700 {
		workspace.removePartial()
		return nil, ErrLaunch
	}
	if err := os.Mkdir(workspace.jwtPath, 0o700); err != nil {
		workspace.removePartial()
		return nil, ErrLaunch
	}
	workspace.jwtInfo, err = os.Lstat(workspace.jwtPath)
	if err != nil ||
		workspace.jwtInfo.Mode()&os.ModeSymlink != 0 ||
		!workspace.jwtInfo.IsDir() ||
		workspace.jwtInfo.Mode().Perm() != 0o700 {
		workspace.removePartial()
		return nil, ErrLaunch
	}
	if err := writeGatewayJWT(workspace.jwtPath); err != nil {
		workspace.removePartial()
		return nil, ErrLaunch
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

func writeGatewayJWT(path string) error {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return err
	}
	var kid [16]byte
	if _, err := rand.Read(kid[:]); err != nil {
		return err
	}
	files := []struct {
		name    string
		content []byte
	}{
		{name: "signing.pem", content: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})},
		{name: "public.pem", content: pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})},
		{name: "kid", content: []byte(hex.EncodeToString(kid[:]) + "\n")},
	}
	for _, file := range files {
		if _, err := writeTopologyFile(filepath.Join(path, file.name), file.content); err != nil {
			return err
		}
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
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

func (workspace *topologyWorkspace) StatePath() string {
	if workspace == nil {
		return ""
	}
	return workspace.statePath
}

func (workspace *topologyWorkspace) JWTPath() string {
	if workspace == nil {
		return ""
	}
	return workspace.jwtPath
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
	stateInfo, err := os.Lstat(workspace.statePath)
	if err != nil ||
		stateInfo.Mode()&os.ModeSymlink != 0 ||
		!stateInfo.IsDir() ||
		!os.SameFile(stateInfo, workspace.stateInfo) {
		return ErrLaunch
	}
	for _, directory := range []struct {
		path string
		info os.FileInfo
	}{
		{path: workspace.jwtPath, info: workspace.jwtInfo},
		{path: workspace.statePath, info: workspace.stateInfo},
	} {
		current, err := os.Lstat(directory.path)
		if err != nil ||
			current.Mode()&os.ModeSymlink != 0 ||
			!current.IsDir() ||
			!os.SameFile(current, directory.info) {
			return ErrLaunch
		}
		if err := os.RemoveAll(directory.path); err != nil {
			return ErrLaunch
		}
		if _, err := os.Lstat(directory.path); !errors.Is(err, os.ErrNotExist) {
			return ErrLaunch
		}
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
	_ = os.RemoveAll(workspace.jwtPath)
	_ = os.RemoveAll(workspace.statePath)
	_ = os.Remove(workspace.composePath)
	_ = os.Remove(workspace.gatewayPath)
	_ = workspace.directory.Close()
	_ = os.Remove(workspace.path)
	_ = workspace.parent.Sync()
	_ = workspace.parent.Close()
}

func (*topologyWorkspace) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var _ json.Marshaler = (*topologyWorkspace)(nil)
