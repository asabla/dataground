package runtimeevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const developmentGatewayContract = "dataground.dev.gateway-deployment/v1"

var (
	ErrDevelopmentGateway        = errors.New("development gateway operation unavailable; retain the workspace and retry the exact request")
	ErrDevelopmentGatewayRetired = errors.New("development gateway is retired; a new deployment requires a new run identifier")
	developmentScopePattern      = regexp.MustCompile(`^(iso|svc|rev)_[0-9a-z]{20,32}$`)
)

// DevelopmentGatewayConfig is an operator-owned local deployment request. It
// grants no service execution, provider credential, or certification authority.
type DevelopmentGatewayConfig struct {
	IsolationDomainID      string `json:"isolationDomainId"`
	ServiceID              string `json:"serviceId"`
	RevisionID             string `json:"revisionId"`
	RunID                  string `json:"runId"`
	RepositoryRoot         string `json:"repositoryRoot"`
	WorkspaceRoot          string `json:"workspaceRoot"`
	DockerBinary           string `json:"dockerBinary"`
	SupervisorLocalImageID string `json:"supervisorLocalImageId"`
	GatewayConfigSHA256    string `json:"gatewayConfigSHA256"`
}

type DevelopmentGatewayReceipt struct {
	Contract               string `json:"contract"`
	IsolationDomainID      string `json:"isolationDomainId"`
	ServiceID              string `json:"serviceId"`
	RevisionID             string `json:"revisionId"`
	RunID                  string `json:"runId"`
	ContainerID            string `json:"containerId"`
	StartedAt              string `json:"startedAt"`
	SupervisorLocalImageID string `json:"supervisorLocalImageId"`
	GatewayConfigSHA256    string `json:"gatewayConfigSHA256"`
	State                  string `json:"state"`
}

type developmentGatewayDependencies struct {
	observedTopologyDependencies
	stageWait func(context.Context) error
}

// RunDevelopmentGateway keeps the gateway alive after the command exits. An
// ambiguous up is observed on replay, never repeated. Stop retains all data.
func RunDevelopmentGateway(ctx context.Context, action string, config DevelopmentGatewayConfig) (DevelopmentGatewayReceipt, error) {
	if runtime.GOOS != "linux" || (config.SupervisorLocalImageID != "" && runtime.GOARCH != "arm64") {
		return DevelopmentGatewayReceipt{}, ErrDockerTopologyConfiguration
	}
	return runDevelopmentGateway(ctx, action, config, developmentGatewayDependencies{
		observedTopologyDependencies: observedTopologyDependencies{
			dockerTopologyDependencies: dockerTopologyDependencies{runner: localTopologyDockerRunner{}, resolveBinary: resolveRuntimeTopologyBinary, processIdentity: runtimeDockerProcessIdentity, checkListeners: checkGatewayProcessListeners, wait: waitForRuntimeTopology},
			checkLoadedConfiguration:   checkLoadedGatewayConfiguration,
		},
		stageWait: func(ctx context.Context) error {
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	})
}

func runDevelopmentGateway(ctx context.Context, action string, config DevelopmentGatewayConfig, deps developmentGatewayDependencies) (DevelopmentGatewayReceipt, error) {
	empty := DevelopmentGatewayReceipt{}
	if ctx == nil || (action != "up" && action != "inspect" && action != "stop") || !config.valid() || deps.runner == nil || deps.resolveBinary == nil || deps.processIdentity == nil || deps.checkListeners == nil || deps.checkLoadedConfiguration == nil || deps.wait == nil || deps.stageWait == nil {
		return empty, ErrDockerTopologyConfiguration
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	root, err := resolveRuntimeTopologyDirectory(config.WorkspaceRoot, true)
	if err != nil || root != config.WorkspaceRoot {
		return empty, ErrDockerTopologyConfiguration
	}
	lock, err := lockDevelopmentGateway(root, config.RunID)
	if err != nil {
		return empty, ErrDevelopmentGateway
	}
	defer closeDevelopmentGatewayLock(lock)
	if ctx.Err() != nil {
		return empty, ErrDevelopmentGateway
	}
	prefix := filepath.Join(root, "dg-runtime-deployment-"+config.RunID)
	request, _ := json.Marshal(config)
	if err := immutableDevelopmentRecord(prefix+".request.json", request, action == "up"); err != nil {
		return empty, ErrDevelopmentGateway
	}
	retired, err := readDevelopmentRecord(prefix+".stop", []byte("stop\n"))
	if err != nil {
		return empty, ErrDevelopmentGateway
	}
	if retired && action != "stop" {
		return empty, ErrDevelopmentGatewayRetired
	}
	intent, err := readDevelopmentRecord(prefix+".start", []byte("start\n"))
	if err != nil {
		return empty, ErrDevelopmentGateway
	}
	state, err := openDevelopmentGatewayState(config, deps, action == "up" && !intent)
	if err != nil {
		return empty, ErrDevelopmentGateway
	}
	defer func() { _ = state.workspace.directory.Close(); _ = state.workspace.parent.Close() }()
	var receipt DevelopmentGatewayReceipt
	content, err := readDevelopmentBytes(prefix + ".receipt.json")
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&receipt) != nil || decoder.Decode(new(any)) != io.EOF || !receipt.matches(config) {
			return empty, ErrDevelopmentGateway
		}
		canonical, _ := json.Marshal(receipt)
		if !intent || !bytes.Equal(content, canonical) {
			return empty, ErrDevelopmentGateway
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return empty, ErrDevelopmentGateway
	}
	if action == "stop" {
		if receipt.ContainerID == "" {
			return empty, ErrDevelopmentGateway
		}
		if immutableDevelopmentRecord(prefix+".stop", []byte("stop\n"), true) != nil {
			return empty, ErrDevelopmentGateway
		}
		if stopDevelopmentGateway(ctx, state, receipt) != nil {
			return empty, ErrDevelopmentGateway
		}
		receipt.State = "stopped"
		return receipt, nil
	}
	if receipt.ContainerID == "" {
		if action != "up" {
			return empty, ErrDevelopmentGateway
		}
		if !intent {
			ids, err := developmentGatewayContainers(ctx, state)
			if err != nil || len(ids) != 0 {
				return empty, ErrDevelopmentGateway
			}
			if deps.stageWait(ctx) != nil || state.verifyFrozenTopology() != nil {
				return empty, ErrDevelopmentGateway
			}
			if immutableDevelopmentRecord(prefix+".start", []byte("start\n"), true) != nil {
				return empty, ErrDevelopmentGateway
			}
			// No second up is allowed after this durable intent, including when
			// cancellation or a lost acknowledgement hides the native outcome.
			output, _ := state.runner.Run(ctx, state.environment, state.binary, "compose", "--project-name", state.project, "--file", state.workspace.composePath, "up", "--detach", "--no-recreate", "--no-build", "--pull", "never", "gateway")
			clear(output)
		}
		ids, err := developmentGatewayContainers(ctx, state)
		if err != nil || len(ids) != 1 || state.verifyContainer(ctx, ids[0]) != nil || deps.wait(ctx) != nil {
			return empty, ErrDevelopmentGateway
		}
		started, err := state.verifyRunningConfiguration(ctx, ids[0])
		if err != nil {
			return empty, ErrDevelopmentGateway
		}
		receipt = DevelopmentGatewayReceipt{Contract: developmentGatewayContract, IsolationDomainID: config.IsolationDomainID, ServiceID: config.ServiceID, RevisionID: config.RevisionID, RunID: config.RunID, ContainerID: ids[0], StartedAt: started, SupervisorLocalImageID: config.SupervisorLocalImageID, GatewayConfigSHA256: config.GatewayConfigSHA256, State: "running"}
	}
	observer, err := newObservedDockerTopology(ObservedDockerTopologyConfig{RunID: config.RunID, ContainerID: receipt.ContainerID, StartedAt: receipt.StartedAt, WorkspaceRoot: config.WorkspaceRoot, DockerBinary: config.DockerBinary, SupervisorLocalImageID: config.SupervisorLocalImageID, GatewayConfigSHA256: config.GatewayConfigSHA256}, deps.observedTopologyDependencies)
	if err != nil {
		return empty, ErrDevelopmentGateway
	}
	defer observer.Close()
	if observer.Check(ctx) != nil || ctx.Err() != nil {
		return empty, ErrDevelopmentGateway
	}
	content, _ = json.Marshal(receipt)
	if immutableDevelopmentRecord(prefix+".receipt.json", content, action == "up") != nil {
		return empty, ErrDevelopmentGateway
	}
	return receipt, nil
}

func (config DevelopmentGatewayConfig) valid() bool {
	for _, scope := range []struct{ value, prefix string }{{config.IsolationDomainID, "iso_"}, {config.ServiceID, "svc_"}, {config.RevisionID, "rev_"}} {
		if !developmentScopePattern.MatchString(scope.value) || !strings.HasPrefix(scope.value, scope.prefix) {
			return false
		}
	}
	if !runIDPattern.MatchString(config.RunID) || !commitmentPattern.MatchString("sha256:"+config.GatewayConfigSHA256) || (config.SupervisorLocalImageID == "" && config.GatewayConfigSHA256 != runtimeGatewayConfigSHA256) || (config.SupervisorLocalImageID != "" && !commitmentPattern.MatchString(config.SupervisorLocalImageID)) {
		return false
	}
	for _, path := range []string{config.RepositoryRoot, config.WorkspaceRoot, config.DockerBinary} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.ContainsRune(path, 0) {
			return false
		}
	}
	return !strings.ContainsRune(config.DockerBinary, os.PathListSeparator) && !runtimeTopologyPathsOverlap(config.RepositoryRoot, config.WorkspaceRoot)
}

func (receipt DevelopmentGatewayReceipt) matches(config DevelopmentGatewayConfig) bool {
	started, err := time.Parse(time.RFC3339Nano, receipt.StartedAt)
	return err == nil && !started.IsZero() && !started.After(time.Now()) && runtimeContainerIDPattern.MatchString(receipt.ContainerID) && receipt.Contract == developmentGatewayContract && receipt.IsolationDomainID == config.IsolationDomainID && receipt.ServiceID == config.ServiceID && receipt.RevisionID == config.RevisionID && receipt.RunID == config.RunID && receipt.SupervisorLocalImageID == config.SupervisorLocalImageID && receipt.GatewayConfigSHA256 == config.GatewayConfigSHA256 && receipt.State == "running"
}

func openDevelopmentGatewayState(config DevelopmentGatewayConfig, deps developmentGatewayDependencies, create bool) (*dockerTopologyState, error) {
	binary, err := deps.resolveBinary(config.DockerBinary)
	if err != nil || !filepath.IsAbs(binary) {
		return nil, ErrDevelopmentGateway
	}
	uid, gid, dockerGID, err := deps.processIdentity()
	if err != nil || uid < 0 || gid < 0 || dockerGID < 0 {
		return nil, ErrDevelopmentGateway
	}
	workspace, err := openObservedTopologyWorkspace(config.WorkspaceRoot, config.RunID)
	if err != nil {
		_, statErr := os.Lstat(filepath.Join(config.WorkspaceRoot, runtimeTopologyDirectoryPrefix+config.RunID))
		if !create || !errors.Is(statErr, os.ErrNotExist) {
			return nil, ErrDevelopmentGateway
		}
		compose, err := readRuntimeTopologyFile(filepath.Join(config.RepositoryRoot, runtimeTopologyComposePath), runtimeComposeSHA256)
		if err != nil {
			return nil, err
		}
		defer clear(compose)
		gateway, err := readRuntimeTopologyFile(filepath.Join(config.RepositoryRoot, runtimeTopologyGatewayPath), runtimeGatewayConfigSHA256)
		if err != nil {
			return nil, err
		}
		defer clear(gateway)
		if config.SupervisorLocalImageID != "" {
			// Candidate inspection gets the same local daemon and isolated
			// environment as all subsequent deployment operations.
			gateway, err = selectRuntimeSupervisorCandidate(DockerTopologyConfig{supervisorCandidateImage: config.SupervisorLocalImageID}, developmentGatewayRunner{deps.runner}, binary, gateway)
			if err != nil {
				return nil, err
			}
			defer clear(gateway)
		}
		if runtimeTopologySHA256(gateway) != config.GatewayConfigSHA256 {
			return nil, ErrDevelopmentGateway
		}
		workspace, err = openRuntimeTopologyWorkspace(config.WorkspaceRoot, config.RunID, compose, gateway)
		if err != nil {
			return nil, err
		}
	}
	resources := namesForRun(config.RunID)
	environment := developmentGatewayEnvironment(binary, runtimeTopologyEnvironment(config.RunID, resources, workspace.statePath, workspace.jwtPath, uid, gid, dockerGID))
	state := &dockerTopologyState{runID: config.RunID, resources: resources, project: "dg_runtime_" + config.RunID, runner: deps.runner, binary: binary, environment: environment, workspace: workspace, checkListeners: deps.checkListeners}
	if config.SupervisorLocalImageID != "" {
		state.candidate = candidateTopologyBinding{image: config.SupervisorLocalImageID, gatewaySHA256: config.GatewayConfigSHA256}
	}
	if state.verifyFrozenTopology() != nil {
		_ = workspace.directory.Close()
		_ = workspace.parent.Close()
		return nil, ErrDevelopmentGateway
	}
	return state, nil
}

type developmentGatewayRunner struct{ dockerTopologyRunner }

func (runner developmentGatewayRunner) Run(ctx context.Context, _ []string, binary string, args ...string) ([]byte, error) {
	return runner.dockerTopologyRunner.Run(ctx, developmentGatewayEnvironment(binary, nil), binary, args...)
}
func developmentGatewayEnvironment(binary string, values []string) []string {
	result := []string{"PATH=" + filepath.Dir(binary) + ":/usr/bin:/bin"}
	for _, value := range values {
		if strings.HasPrefix(value, "DATAGROUND_RUNTIME_CONFORMANCE_") {
			result = append(result, value)
		}
	}
	return result
}

func developmentGatewayContainers(ctx context.Context, state *dockerTopologyState) ([]string, error) {
	output, err := state.runner.Run(ctx, state.environment, state.binary, "ps", "--all", "--no-trunc", "--filter", "label=com.docker.compose.project="+state.project, "--format", "{{.ID}}")
	defer clear(output)
	if err != nil || len(output) > 1024 {
		return nil, ErrDevelopmentGateway
	}
	ids := strings.Fields(string(output))
	if len(ids) > 1 || (len(ids) == 1 && !runtimeContainerIDPattern.MatchString(ids[0])) {
		return nil, ErrDevelopmentGateway
	}
	return ids, nil
}

func stopDevelopmentGateway(ctx context.Context, state *dockerTopologyState, receipt DevelopmentGatewayReceipt) error {
	ids, err := developmentGatewayContainers(ctx, state)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if ids[0] != receipt.ContainerID || state.verifyContainer(ctx, receipt.ContainerID) != nil {
		return ErrDevelopmentGateway
	}
	inspect := func() (bool, error) {
		output, err := state.runner.Run(ctx, state.environment, state.binary, "inspect", "--format", `{{.State.Running}} {{.State.StartedAt}}`, receipt.ContainerID)
		defer clear(output)
		values := strings.Fields(string(output))
		if err != nil || len(values) != 2 || values[1] != receipt.StartedAt || (values[0] != "true" && values[0] != "false") {
			return false, ErrDevelopmentGateway
		}
		return values[0] == "true", nil
	}
	running, err := inspect()
	if err != nil || !running {
		return err
	}
	output, _ := state.runner.Run(ctx, state.environment, state.binary, "stop", "--time", "10", receipt.ContainerID)
	clear(output)
	running, err = inspect()
	if err != nil || running {
		return ErrDevelopmentGateway
	}
	return nil
}
