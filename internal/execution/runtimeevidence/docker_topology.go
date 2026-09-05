package runtimeevidence

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	runtimeTopologyComposePath     = "deploy/openshell/runtime-conformance/docker-compose.yml"
	runtimeTopologyGatewayPath     = "deploy/openshell/runtime-conformance/gateway.toml"
	runtimeTopologyDirectoryPrefix = "dg-runtime-topology-"
	runtimeTopologyJWTDirectory    = "gateway-jwt"
	runtimeTopologyMaxFileBytes    = 1 << 20
	runtimeTopologyMaxOutputBytes  = 64 << 10
	runtimeTopologyReadyTimeout    = 2 * time.Minute
	runtimeTopologyPollInterval    = 500 * time.Millisecond
	runtimeTopologyCleanupTimeout  = time.Minute
	runtimeTopologyHealthEndpoint  = "http://127.0.0.1:8081/health"
)

var (
	ErrDockerTopologyConfiguration = errors.New("invalid runtime conformance Docker topology configuration")
	ErrDockerTopologyDrift         = errors.New("runtime conformance Docker topology does not match the checked profile")
	ErrDockerTopologyStart         = errors.New("runtime conformance Docker topology start failed")
	ErrDockerTopologyOrder         = errors.New("runtime conformance Docker topology order is invalid")
	ErrDockerTopologyCleanup       = errors.New("runtime conformance Docker topology cleanup failed")
	runtimeContainerIDPattern      = regexp.MustCompile("^[a-f0-9]{64}$")
)

type DockerTopologyConfig struct {
	RunID          string
	RepositoryRoot string
	WorkspaceRoot  string
	DockerBinary   string
}

type DockerTopology struct {
	state *dockerTopologyState
}

type dockerTopologyState struct {
	mu          sync.Mutex
	runID       string
	resources   Resources
	runner      dockerTopologyRunner
	binary      string
	project     string
	environment []string
	wait        func(context.Context) error
	workspace   *runtimeTopologyWorkspace
	started     bool
	starting    bool
	active      bool
	cleaning    bool
	removed     bool
	failed      bool
}

type dockerTopologyDependencies struct {
	runner          dockerTopologyRunner
	wait            func(context.Context) error
	resolveBinary   func(string) (string, error)
	processIdentity func() (int, int, int, error)
}

type dockerTopologyRunner interface {
	Run(context.Context, []string, string, ...string) ([]byte, error)
}

type dockerTopologyExecRunner struct{}

// NewDockerTopology owns the exact Docker-hosted gateway used by one runtime
// conformance run. It freezes checked topology bytes before native mutation and
// retains cleanup authority after an ambiguous start.
func NewDockerTopology(config DockerTopologyConfig) (*DockerTopology, error) {
	return newDockerTopology(config, dockerTopologyDependencies{
		runner:          dockerTopologyExecRunner{},
		wait:            waitForRuntimeTopology,
		resolveBinary:   resolveRuntimeTopologyBinary,
		processIdentity: runtimeDockerProcessIdentity,
	})
}

func newDockerTopology(
	config DockerTopologyConfig,
	dependencies dockerTopologyDependencies,
) (*DockerTopology, error) {
	if !runIDPattern.MatchString(config.RunID) ||
		config.RepositoryRoot == "" ||
		config.WorkspaceRoot == "" ||
		dependencies.runner == nil ||
		dependencies.wait == nil ||
		dependencies.resolveBinary == nil ||
		dependencies.processIdentity == nil {
		return nil, ErrDockerTopologyConfiguration
	}
	repositoryRoot, err := resolveRuntimeTopologyDirectory(config.RepositoryRoot, false)
	if err != nil {
		return nil, err
	}
	workspaceRoot, err := resolveRuntimeTopologyDirectory(config.WorkspaceRoot, true)
	if err != nil || runtimeTopologyPathsOverlap(repositoryRoot, workspaceRoot) {
		return nil, ErrDockerTopologyConfiguration
	}
	binaryName := config.DockerBinary
	if binaryName == "" {
		binaryName = "docker"
	}
	binary, err := dependencies.resolveBinary(binaryName)
	if err != nil {
		return nil, ErrDockerTopologyConfiguration
	}
	compose, err := readRuntimeTopologyFile(
		filepath.Join(repositoryRoot, filepath.FromSlash(runtimeTopologyComposePath)),
		runtimeComposeSHA256,
	)
	if err != nil {
		return nil, err
	}
	defer clear(compose)
	gateway, err := readRuntimeTopologyFile(
		filepath.Join(repositoryRoot, filepath.FromSlash(runtimeTopologyGatewayPath)),
		runtimeGatewayConfigSHA256,
	)
	if err != nil {
		return nil, err
	}
	defer clear(gateway)
	workspace, err := openRuntimeTopologyWorkspace(
		workspaceRoot,
		config.RunID,
		compose,
		gateway,
	)
	if err != nil {
		return nil, err
	}
	userID, groupID, dockerGroupID, err := dependencies.processIdentity()
	if err != nil || userID < 0 || groupID < 0 || dockerGroupID < 0 {
		_ = workspace.Cleanup(context.Background())
		return nil, ErrDockerTopologyConfiguration
	}
	resources := namesForRun(config.RunID)
	return &DockerTopology{state: &dockerTopologyState{
		runID:     config.RunID,
		resources: resources,
		runner:    dependencies.runner,
		binary:    binary,
		project:   "dg_runtime_" + config.RunID,
		environment: runtimeTopologyEnvironment(
			config.RunID,
			resources,
			workspace.statePath,
			workspace.jwtPath,
			userID,
			groupID,
			dockerGroupID,
		),
		wait:      dependencies.wait,
		workspace: workspace,
	}}, nil
}

func (topology *DockerTopology) Start(ctx context.Context) error {
	if topology == nil || topology.state == nil || ctx == nil {
		return ErrDockerTopologyConfiguration
	}
	state := topology.state
	state.mu.Lock()
	if state.started || state.starting || state.removed || state.failed {
		state.failed = true
		state.mu.Unlock()
		return errors.Join(ErrDockerTopologyStart, ErrDockerTopologyOrder)
	}
	state.started = true
	state.starting = true
	state.active = true
	state.mu.Unlock()

	if err := ctx.Err(); err != nil {
		state.failStart()
		return errors.Join(ErrDockerTopologyStart, err)
	}
	if _, err := state.runner.Run(
		ctx,
		state.environment,
		state.binary,
		"compose",
		"--project-name",
		state.project,
		"--file",
		state.workspace.composePath,
		"up",
		"--detach",
		"--remove-orphans",
	); err != nil {
		state.failStart()
		return topologyStartError(ctx)
	}
	if err := state.wait(ctx); err != nil {
		state.failStart()
		return topologyStartError(ctx)
	}
	containerID, err := state.observeContainer(ctx)
	if err != nil {
		state.failStart()
		return topologyStartError(ctx)
	}
	if err := state.continuationError(ctx); err != nil {
		state.failStart()
		return err
	}
	if err := state.verifyContainer(ctx, containerID); err != nil {
		state.failStart()
		return topologyStartError(ctx)
	}
	state.mu.Lock()
	if err := ctx.Err(); err != nil {
		state.failed = true
		state.starting = false
		state.mu.Unlock()
		return errors.Join(ErrDockerTopologyStart, err)
	}
	if state.failed || state.removed {
		state.failed = true
		state.starting = false
		state.mu.Unlock()
		return errors.Join(ErrDockerTopologyStart, ErrDockerTopologyOrder)
	}
	state.starting = false
	state.mu.Unlock()
	return nil
}

// Cleanup tears down the exact run-derived project and frozen workspace under
// a cancellation-independent bound. It is safe after failed or ambiguous start.
func (topology *DockerTopology) Cleanup(ctx context.Context) error {
	if topology == nil || topology.state == nil || ctx == nil {
		return ErrDockerTopologyConfiguration
	}
	state := topology.state
	state.mu.Lock()
	if state.cleaning || state.starting {
		if state.starting {
			state.failed = true
		}
		state.mu.Unlock()
		return ErrDockerTopologyCleanup
	}
	if state.removed {
		state.mu.Unlock()
		return nil
	}
	state.cleaning = true
	active := state.active
	state.mu.Unlock()

	cleanupCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		runtimeTopologyCleanupTimeout,
	)
	defer cancel()
	var outcome error
	if active {
		_, _ = state.runner.Run(
			cleanupCtx,
			state.environment,
			state.binary,
			"compose",
			"--project-name",
			state.project,
			"--file",
			state.workspace.composePath,
			"down",
			"--volumes",
			"--remove-orphans",
		)
		if err := state.observeRemoval(cleanupCtx); err != nil {
			outcome = errors.Join(outcome, ErrDockerTopologyCleanup)
		}
	}
	if outcome == nil {
		if err := state.workspace.Cleanup(cleanupCtx); err != nil {
			outcome = errors.Join(outcome, ErrDockerTopologyCleanup)
		}
	}
	state.mu.Lock()
	state.cleaning = false
	if outcome != nil {
		state.failed = true
		state.mu.Unlock()
		return outcome
	}
	state.active = false
	state.removed = true
	state.mu.Unlock()
	return nil
}

func (state *dockerTopologyState) observeContainer(ctx context.Context) (string, error) {
	output, err := state.runner.Run(
		ctx,
		state.environment,
		state.binary,
		"compose",
		"--project-name",
		state.project,
		"--file",
		state.workspace.composePath,
		"ps",
		"--quiet",
		"gateway",
	)
	if err != nil {
		return "", ErrDockerTopologyStart
	}
	containerID := strings.TrimSpace(string(output))
	clear(output)
	if !runtimeContainerIDPattern.MatchString(containerID) {
		return "", ErrDockerTopologyStart
	}
	return containerID, nil
}

func (state *dockerTopologyState) verifyContainer(
	ctx context.Context,
	containerID string,
) error {
	format := strings.Join([]string{
		"{{.Id}}",
		"{{.Config.Image}}",
		"{{index .Config.Labels \"com.docker.compose.project\"}}",
		"{{index .Config.Labels \"dataground.dev/runtime-conformance-run\"}}",
		"{{index .Config.Labels \"dataground.dev/runtime-conformance-gateway\"}}",
		"{{index .Config.Labels \"dataground.dev/runtime-conformance-provider\"}}",
	}, "\n")
	output, err := state.runner.Run(
		ctx,
		state.environment,
		state.binary,
		"inspect",
		"--format",
		format,
		containerID,
	)
	if err != nil {
		return ErrDockerTopologyStart
	}
	values := strings.Split(strings.TrimSpace(string(output)), "\n")
	clear(output)
	if len(values) != 6 ||
		values[0] != containerID ||
		values[1] != gatewayImage ||
		values[2] != state.project ||
		values[3] != state.runID ||
		values[4] != state.resources.Gateway ||
		values[5] != state.resources.Provider {
		return ErrDockerTopologyStart
	}
	return nil
}

func (state *dockerTopologyState) observeRemoval(ctx context.Context) error {
	containers, containerErr := state.runner.Run(
		ctx,
		state.environment,
		state.binary,
		"compose",
		"--project-name",
		state.project,
		"--file",
		state.workspace.composePath,
		"ps",
		"--all",
		"--quiet",
	)
	volumes, volumeErr := state.runner.Run(
		ctx,
		state.environment,
		state.binary,
		"volume",
		"ls",
		"--filter",
		"label=com.docker.compose.project="+state.project,
		"--quiet",
	)
	containersRemain := strings.TrimSpace(string(containers)) != ""
	volumesRemain := strings.TrimSpace(string(volumes)) != ""
	clear(containers)
	clear(volumes)
	if containerErr != nil || volumeErr != nil || containersRemain || volumesRemain {
		return ErrDockerTopologyCleanup
	}
	return nil
}

func (state *dockerTopologyState) continuationError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrDockerTopologyStart, err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.failed || state.removed {
		return errors.Join(ErrDockerTopologyStart, ErrDockerTopologyOrder)
	}
	return nil
}

func (state *dockerTopologyState) failStart() {
	state.mu.Lock()
	state.starting = false
	state.failed = true
	state.mu.Unlock()
}

func (dockerTopologyExecRunner) Run(
	ctx context.Context,
	environment []string,
	binary string,
	args ...string,
) ([]byte, error) {
	if ctx == nil || binary == "" {
		return nil, ErrDockerTopologyConfiguration
	}
	var output runtimeTopologyBoundedBuffer
	output.remaining = runtimeTopologyMaxOutputBytes
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append([]string(nil), environment...)
	command.Stdout = &output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		clear(output.buffer.Bytes())
		return nil, ErrDockerTopologyStart
	}
	value := append([]byte(nil), output.buffer.Bytes()...)
	clear(output.buffer.Bytes())
	return value, nil
}

func waitForRuntimeTopology(ctx context.Context) error {
	readyCtx, cancel := context.WithTimeout(ctx, runtimeTopologyReadyTimeout)
	defer cancel()
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrDockerTopologyStart
		},
	}
	ticker := time.NewTicker(runtimeTopologyPollInterval)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(
			readyCtx,
			http.MethodGet,
			runtimeTopologyHealthEndpoint,
			nil,
		)
		if err != nil {
			return ErrDockerTopologyStart
		}
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
			closeErr := response.Body.Close()
			if response.StatusCode == http.StatusOK && closeErr == nil {
				return nil
			}
		}
		select {
		case <-readyCtx.Done():
			return ErrDockerTopologyStart
		case <-ticker.C:
		}
	}
}

func resolveRuntimeTopologyDirectory(value string, private bool) (string, error) {
	path, err := filepath.Abs(value)
	if err != nil {
		return "", ErrDockerTopologyConfiguration
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", ErrDockerTopologyConfiguration
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", ErrDockerTopologyConfiguration
	}
	if private && info.Mode().Perm() != 0o700 {
		return "", ErrDockerTopologyConfiguration
	}
	return filepath.Clean(path), nil
}

func resolveRuntimeTopologyBinary(value string) (string, error) {
	resolved, err := exec.LookPath(value)
	if err != nil {
		return "", ErrDockerTopologyConfiguration
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", ErrDockerTopologyConfiguration
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return "", ErrDockerTopologyConfiguration
	}
	return filepath.Clean(resolved), nil
}

func runtimeDockerProcessIdentity() (int, int, int, error) {
	info, err := os.Stat("/var/run/docker.sock")
	stat, ok := runtimeTopologySystemStat(info)
	if err != nil || !ok || info.Mode()&os.ModeSocket == 0 {
		return 0, 0, 0, ErrDockerTopologyConfiguration
	}
	return os.Getuid(), os.Getgid(), int(stat.Gid), nil
}

func runtimeTopologySystemStat(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

func runtimeTopologyEnvironment(
	runID string,
	resources Resources,
	statePath string,
	jwtPath string,
	userID int,
	groupID int,
	dockerGroupID int,
) []string {
	keys := [...]string{
		"DOCKER_CERT_PATH",
		"DOCKER_CONFIG",
		"DOCKER_CONTEXT",
		"DOCKER_HOST",
		"DOCKER_TLS_VERIFY",
		"HOME",
		"PATH",
		"XDG_CONFIG_HOME",
	}
	environment := make([]string, 0, len(keys)+8)
	for _, key := range keys {
		if value, exists := os.LookupEnv(key); exists {
			environment = append(environment, key+"="+value)
		}
	}
	return append(
		environment,
		"DATAGROUND_RUNTIME_CONFORMANCE_RUN_ID="+runID,
		"DATAGROUND_RUNTIME_CONFORMANCE_GATEWAY="+resources.Gateway,
		"DATAGROUND_RUNTIME_CONFORMANCE_PROVIDER="+resources.Provider,
		"DATAGROUND_RUNTIME_CONFORMANCE_STATE_PATH="+statePath,
		"DATAGROUND_RUNTIME_CONFORMANCE_JWT_PATH="+jwtPath,
		"DATAGROUND_RUNTIME_CONFORMANCE_UID="+strconv.Itoa(userID),
		"DATAGROUND_RUNTIME_CONFORMANCE_GID="+strconv.Itoa(groupID),
		"DATAGROUND_RUNTIME_CONFORMANCE_DOCKER_GID="+strconv.Itoa(dockerGroupID),
	)
}

func runtimeTopologyPathsOverlap(left string, right string) bool {
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil &&
			(relative == "." ||
				(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
			return true
		}
	}
	return false
}

func readRuntimeTopologyFile(path string, expectedDigest string) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || len(expectedDigest) != 64 {
		return nil, ErrDockerTopologyConfiguration
	}
	info, err := os.Lstat(path)
	if err != nil ||
		info.Mode()&os.ModeSymlink != 0 ||
		!info.Mode().IsRegular() ||
		info.Size() < 0 ||
		info.Size() > runtimeTopologyMaxFileBytes {
		return nil, ErrDockerTopologyDrift
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrDockerTopologyDrift
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, runtimeTopologyMaxFileBytes+1))
	if err != nil || len(content) > runtimeTopologyMaxFileBytes {
		clear(content)
		return nil, ErrDockerTopologyDrift
	}
	opened, openedErr := file.Stat()
	current, currentErr := os.Lstat(path)
	if openedErr != nil ||
		currentErr != nil ||
		!os.SameFile(info, opened) ||
		!os.SameFile(info, current) ||
		!runtimeTopologyDigestEqual(runtimeTopologySHA256(content), expectedDigest) {
		clear(content)
		return nil, ErrDockerTopologyDrift
	}
	return content, nil
}

func runtimeTopologySHA256(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func runtimeTopologyDigestEqual(left string, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func topologyStartError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrDockerTopologyStart, err)
	}
	return ErrDockerTopologyStart
}

type runtimeTopologyBoundedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (buffer *runtimeTopologyBoundedBuffer) Write(value []byte) (int, error) {
	if len(value) > buffer.remaining {
		return 0, ErrDockerTopologyStart
	}
	count, err := buffer.buffer.Write(value)
	buffer.remaining -= count
	return count, err
}

type runtimeTopologyWorkspace struct {
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

func openRuntimeTopologyWorkspace(
	root string,
	runID string,
	compose []byte,
	gateway []byte,
) (*runtimeTopologyWorkspace, error) {
	if !runIDPattern.MatchString(runID) ||
		root == "" ||
		!filepath.IsAbs(root) ||
		len(compose) == 0 ||
		len(gateway) == 0 {
		return nil, ErrDockerTopologyConfiguration
	}
	parent, err := os.Open(root)
	if err != nil {
		return nil, ErrDockerTopologyConfiguration
	}
	currentParent, currentParentErr := os.Lstat(root)
	parentInfo, err := parent.Stat()
	if err != nil ||
		currentParentErr != nil ||
		!safeRuntimeTopologyDirectory(parentInfo) ||
		!safeRuntimeTopologyDirectory(currentParent) ||
		!os.SameFile(parentInfo, currentParent) {
		_ = parent.Close()
		return nil, ErrDockerTopologyConfiguration
	}
	path := filepath.Join(root, runtimeTopologyDirectoryPrefix+runID)
	if err := os.Mkdir(path, 0o700); err != nil {
		_ = parent.Close()
		return nil, ErrDockerTopologyStart
	}
	directory, err := os.Open(path)
	if err != nil {
		_ = os.Remove(path)
		_ = parent.Close()
		return nil, ErrDockerTopologyStart
	}
	directoryInfo, err := directory.Stat()
	currentDirectory, currentDirectoryErr := os.Lstat(path)
	if err != nil ||
		currentDirectoryErr != nil ||
		!safeRuntimeTopologyDirectory(directoryInfo) ||
		!safeRuntimeTopologyDirectory(currentDirectory) ||
		!os.SameFile(directoryInfo, currentDirectory) {
		_ = directory.Close()
		_ = os.Remove(path)
		_ = parent.Close()
		return nil, ErrDockerTopologyStart
	}
	workspace := &runtimeTopologyWorkspace{
		root:          root,
		path:          path,
		composePath:   filepath.Join(path, "docker-compose.yml"),
		gatewayPath:   filepath.Join(path, "gateway.toml"),
		statePath:     filepath.Join(path, "gateway-state"),
		jwtPath:       filepath.Join(path, runtimeTopologyJWTDirectory),
		parent:        parent,
		directory:     directory,
		directoryInfo: directoryInfo,
	}
	if err := os.Mkdir(workspace.statePath, 0o700); err == nil {
		workspace.stateInfo, err = os.Lstat(workspace.statePath)
	}
	if err == nil {
		err = os.Mkdir(workspace.jwtPath, 0o700)
	}
	if err == nil {
		workspace.jwtInfo, err = os.Lstat(workspace.jwtPath)
	}
	if err == nil {
		err = writeRuntimeTopologyJWT(workspace.jwtPath)
	}
	if err == nil {
		workspace.composeInfo, err = writeRuntimeTopologyFile(workspace.composePath, compose)
	}
	if err == nil {
		workspace.gatewayInfo, err = writeRuntimeTopologyFile(workspace.gatewayPath, gateway)
	}
	if err == nil {
		err = errors.Join(directory.Sync(), parent.Sync())
	}
	if err != nil ||
		!safeRuntimeTopologyDirectory(workspace.stateInfo) ||
		!safeRuntimeTopologyDirectory(workspace.jwtInfo) {
		workspace.removePartial()
		return nil, ErrDockerTopologyStart
	}
	return workspace, nil
}

func writeRuntimeTopologyJWT(path string) error {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	defer clear(privateKey)
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return err
	}
	defer clear(privateDER)
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
		if _, err := writeRuntimeTopologyFile(filepath.Join(path, file.name), file.content); err != nil {
			for _, candidate := range files {
				clear(candidate.content)
			}
			return err
		}
	}
	for _, file := range files {
		clear(file.content)
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func writeRuntimeTopologyFile(path string, content []byte) (os.FileInfo, error) {
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
	if err != nil || !safeRuntimeTopologyFile(info) {
		return nil, ErrDockerTopologyStart
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	remove = false
	return info, nil
}

func (workspace *runtimeTopologyWorkspace) Cleanup(ctx context.Context) error {
	if workspace == nil || ctx == nil {
		return ErrDockerTopologyConfiguration
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrDockerTopologyCleanup, err)
	}
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.removed {
		return nil
	}
	rootInfo, rootErr := os.Lstat(workspace.root)
	openedRoot, openedRootErr := workspace.parent.Stat()
	directoryInfo, directoryErr := os.Lstat(workspace.path)
	if rootErr != nil ||
		openedRootErr != nil ||
		directoryErr != nil ||
		rootInfo.Mode()&os.ModeSymlink != 0 ||
		directoryInfo.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(rootInfo, openedRoot) ||
		!os.SameFile(directoryInfo, workspace.directoryInfo) {
		return ErrDockerTopologyCleanup
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
			!safeRuntimeTopologyDirectory(current) ||
			!os.SameFile(current, directory.info) {
			return ErrDockerTopologyCleanup
		}
		if err := os.RemoveAll(directory.path); err != nil {
			return ErrDockerTopologyCleanup
		}
		if _, err := os.Lstat(directory.path); !errors.Is(err, os.ErrNotExist) {
			return ErrDockerTopologyCleanup
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
			return ErrDockerTopologyCleanup
		}
		if err := os.Remove(file.path); err != nil {
			return ErrDockerTopologyCleanup
		}
	}
	if err := workspace.directory.Sync(); err != nil {
		return ErrDockerTopologyCleanup
	}
	if err := os.Remove(workspace.path); err != nil {
		return ErrDockerTopologyCleanup
	}
	if err := workspace.parent.Sync(); err != nil {
		return ErrDockerTopologyCleanup
	}
	workspace.removed = true
	return errors.Join(workspace.directory.Close(), workspace.parent.Close())
}

func safeRuntimeTopologyDirectory(info os.FileInfo) bool {
	return info != nil &&
		info.Mode()&os.ModeSymlink == 0 &&
		info.IsDir() &&
		info.Mode().Perm() == 0o700 &&
		runtimeCredentialOwnedByCurrentUser(info)
}

func safeRuntimeTopologyFile(info os.FileInfo) bool {
	return info != nil &&
		info.Mode()&os.ModeSymlink == 0 &&
		info.Mode().IsRegular() &&
		info.Mode().Perm() == 0o600 &&
		runtimeCredentialOwnedByCurrentUser(info) &&
		runtimeCredentialSingleLink(info)
}

func (workspace *runtimeTopologyWorkspace) removePartial() {
	_ = os.RemoveAll(workspace.jwtPath)
	_ = os.RemoveAll(workspace.statePath)
	_ = os.Remove(workspace.composePath)
	_ = os.Remove(workspace.gatewayPath)
	_ = workspace.directory.Close()
	_ = os.Remove(workspace.path)
	_ = workspace.parent.Sync()
	_ = workspace.parent.Close()
}

func (DockerTopologyConfig) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (DockerTopology) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (*runtimeTopologyWorkspace) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var (
	_ dockerTopologyRunner = dockerTopologyExecRunner{}
	_ json.Marshaler       = DockerTopologyConfig{}
	_ json.Marshaler       = DockerTopology{}
	_ json.Marshaler       = (*runtimeTopologyWorkspace)(nil)
)
