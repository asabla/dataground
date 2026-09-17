package runtimeevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/asabla/dataground/internal/execution/openshell"
)

// ObservedDockerTopologyConfig pins an existing deployment independently of the
// observation. It does not grant authority to start, restart or remove it.
type ObservedDockerTopologyConfig struct {
	RunID                  string
	ContainerID            string
	StartedAt              string
	WorkspaceRoot          string
	DockerBinary           string
	SupervisorLocalImageID string
	GatewayConfigSHA256    string
}

type ObservedDockerTopology struct{ state *dockerTopologyState }

type observedTopologyDependencies struct {
	dockerTopologyDependencies
	checkLoadedConfiguration func(context.Context, int, *runtimeTopologyWorkspace, string, time.Time) error
}

func NewObservedDockerTopology(config ObservedDockerTopologyConfig) (*ObservedDockerTopology, error) {
	if runtime.GOOS != "linux" {
		return nil, ErrDockerTopologyConfiguration
	}
	return newObservedDockerTopology(config, observedTopologyDependencies{
		dockerTopologyDependencies: dockerTopologyDependencies{
			runner: localTopologyDockerRunner{}, resolveBinary: resolveRuntimeTopologyBinary,
			processIdentity: runtimeDockerProcessIdentity, checkListeners: checkGatewayProcessListeners,
		},
		checkLoadedConfiguration: checkLoadedGatewayConfiguration,
	})
}

func newObservedDockerTopology(config ObservedDockerTopologyConfig, dependencies observedTopologyDependencies) (*ObservedDockerTopology, error) {
	started, err := time.Parse(time.RFC3339Nano, config.StartedAt)
	if err != nil || started.IsZero() || started.After(time.Now()) || !runIDPattern.MatchString(config.RunID) || !runtimeContainerIDPattern.MatchString(config.ContainerID) || !commitmentPattern.MatchString("sha256:"+config.GatewayConfigSHA256) || dependencies.runner == nil || dependencies.resolveBinary == nil || dependencies.processIdentity == nil || dependencies.checkListeners == nil || dependencies.checkLoadedConfiguration == nil {
		return nil, ErrDockerTopologyConfiguration
	}
	if config.SupervisorLocalImageID == "" {
		if config.GatewayConfigSHA256 != runtimeGatewayConfigSHA256 {
			return nil, ErrDockerTopologyConfiguration
		}
	} else if !commitmentPattern.MatchString(config.SupervisorLocalImageID) {
		return nil, ErrDockerTopologyConfiguration
	}
	root, err := resolveRuntimeTopologyDirectory(config.WorkspaceRoot, true)
	if err != nil {
		return nil, ErrDockerTopologyConfiguration
	}
	binary, err := dependencies.resolveBinary(config.DockerBinary)
	if err != nil || !filepath.IsAbs(binary) {
		return nil, ErrDockerTopologyConfiguration
	}
	uid, gid, dockerGID, err := dependencies.processIdentity()
	if err != nil || uid < 0 || gid < 0 || dockerGID < 0 {
		return nil, ErrDockerTopologyConfiguration
	}
	workspace, err := openObservedTopologyWorkspace(root, config.RunID)
	if err != nil {
		return nil, err
	}
	resources := namesForRun(config.RunID)
	environment := []string{"PATH=" + filepath.Dir(binary) + ":/usr/bin:/bin"}
	for _, entry := range runtimeTopologyEnvironment(config.RunID, resources, workspace.statePath, workspace.jwtPath, uid, gid, dockerGID) {
		if strings.HasPrefix(entry, "DATAGROUND_RUNTIME_CONFORMANCE_") {
			environment = append(environment, entry)
		}
	}
	state := &dockerTopologyState{
		containerID: config.ContainerID, startedAt: config.StartedAt,
		runID: config.RunID, resources: resources, project: "dg_runtime_" + config.RunID,
		runner: dependencies.runner, binary: binary, environment: environment,
		workspace: workspace, checkListeners: dependencies.checkListeners,
		started: true, active: true,
	}
	if config.SupervisorLocalImageID != "" {
		state.candidate = candidateTopologyBinding{image: config.SupervisorLocalImageID, gatewaySHA256: config.GatewayConfigSHA256}
	}
	state.checkLoadedConfiguration = func(ctx context.Context, pid int) error {
		return dependencies.checkLoadedConfiguration(ctx, pid, workspace, config.GatewayConfigSHA256, started)
	}
	observer := &ObservedDockerTopology{state: state}
	content, contentErr := readRuntimeTopologyFile(workspace.gatewayPath, config.GatewayConfigSHA256)
	defer clear(content)
	bindingValid := config.SupervisorLocalImageID == "" || bytes.Count(content, []byte("\nsupervisor_image = \""+config.SupervisorLocalImageID+"\"\n")) == 1
	if contentErr != nil || !bindingValid || state.verifyFrozenTopology() != nil {
		_ = observer.Close()
		return nil, ErrDockerTopologyDrift
	}
	return observer, nil
}

func openObservedTopologyWorkspace(root, runID string) (*runtimeTopologyWorkspace, error) {
	path := filepath.Join(root, runtimeTopologyDirectoryPrefix+runID)
	workspace := &runtimeTopologyWorkspace{root: root, path: path, composePath: filepath.Join(path, "docker-compose.yml"), gatewayPath: filepath.Join(path, "gateway.toml"), statePath: filepath.Join(path, "gateway-state"), jwtPath: filepath.Join(path, runtimeTopologyJWTDirectory)}
	var err error
	workspace.parent, err = os.Open(root)
	if err != nil {
		return nil, ErrDockerTopologyConfiguration
	}
	fail := func() (*runtimeTopologyWorkspace, error) {
		if workspace.directory != nil {
			_ = workspace.directory.Close()
		}
		_ = workspace.parent.Close()
		return nil, ErrDockerTopologyConfiguration
	}
	workspace.directory, err = os.Open(path)
	if err != nil {
		return fail()
	}
	for _, entry := range []struct {
		path      string
		target    *os.FileInfo
		directory bool
	}{
		{path, &workspace.directoryInfo, true}, {workspace.statePath, &workspace.stateInfo, true}, {workspace.jwtPath, &workspace.jwtInfo, true},
		{workspace.composePath, &workspace.composeInfo, false}, {workspace.gatewayPath, &workspace.gatewayInfo, false},
	} {
		info, err := os.Lstat(entry.path)
		if err != nil || (entry.directory && !safeRuntimeTopologyDirectory(info)) || (!entry.directory && !safeRuntimeTopologyFile(info)) {
			return fail()
		}
		*entry.target = info
	}
	opened, err := workspace.directory.Stat()
	if err != nil || !os.SameFile(opened, workspace.directoryInfo) {
		return fail()
	}
	return workspace, nil
}

func (observer *ObservedDockerTopology) Check(ctx context.Context) error {
	if observer == nil || observer.state == nil || ctx == nil {
		return ErrDockerTopologyConfiguration
	}
	state := observer.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.failed || state.removed {
		return ErrDockerTopologyDrift
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fail := func() error { state.failed = true; return ErrDockerTopologyDrift }
	if state.verifyFrozenTopology() != nil || state.verifyContainer(ctx, state.containerID) != nil {
		return fail()
	}
	if state.candidate.image != "" {
		output, err := state.runner.Run(ctx, state.environment, state.binary, "image", "inspect", state.candidate.image, "--format", openshell.SupervisorCandidateInspectionFormat)
		valid := err == nil && len(output) <= 4096 && openshell.VerifySupervisorCandidateInspection(output, state.candidate.image)
		clear(output)
		if !valid {
			return fail()
		}
	}
	if _, err := state.verifyRunningConfiguration(ctx, state.containerID); err != nil {
		return fail()
	}
	return nil
}

// Close releases observation handles only. The deployment remains owned by its
// operator, including after drift or a worker restart.
func (observer *ObservedDockerTopology) Close() error {
	if observer == nil || observer.state == nil {
		return nil
	}
	state := observer.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.removed {
		return nil
	}
	state.removed = true
	if state.workspace != nil {
		_ = state.workspace.directory.Close()
		_ = state.workspace.parent.Close()
	}
	return nil
}

type localTopologyDockerRunner struct{}

func (localTopologyDockerRunner) Run(ctx context.Context, environment []string, binary string, arguments ...string) ([]byte, error) {
	return (dockerTopologyExecRunner{}).Run(ctx, environment, binary, append([]string{"--host", "unix:///var/run/docker.sock"}, arguments...)...)
}
func (ObservedDockerTopologyConfig) MarshalJSON() ([]byte, error) {
	return nil, ErrDockerTopologyConfiguration
}
func (ObservedDockerTopology) MarshalJSON() ([]byte, error) {
	return nil, ErrDockerTopologyConfiguration
}

var _ json.Marshaler = ObservedDockerTopologyConfig{}
