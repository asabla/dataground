package runtimeevidence

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	"github.com/asabla/dataground/internal/security/canarylauncher"
)

const (
	runtimeLauncherOpenShellVersion = "0.0.86"
	candidateRuntimePolicyPath      = "deploy/openshell/codex-compatibility/runtime-policy.yaml"
	runtimeLauncherPolicyPath       = "deploy/openshell/policies/deny-all.yaml"
	runtimeLauncherCleanupTimeout   = 4 * time.Minute
)

var (
	ErrLauncherConfiguration = errors.New("invalid runtime conformance launcher configuration")
	ErrLauncherRun           = errors.New("runtime conformance launcher failed")
	ErrLauncherCleanup       = errors.New("runtime conformance launcher cleanup failed")
)

type LauncherConfig struct {
	diagnosticModel     string
	candidateImage      string
	RepositoryRoot      string
	WorkspaceRoot       string
	CredentialDirectory string
	OpenShellBinary     string
	DockerBinary        string
	Provenance          Provenance
}

type launcherTopology interface {
	Start(context.Context) error
	Cleanup(context.Context) error
}

type launcherCredentialSource interface {
	Cleanup(context.Context) error
}

type launcherProviderBinding interface {
	Provision(context.Context) error
	Cleanup(context.Context, CleanupRequest) error
}

type launcherExecutionCreator interface {
	Create(context.Context) (execution.Execution, error)
	Cleanup(context.Context, CleanupRequest) error
}

type launcherHarness interface {
	Run(context.Context) (Result, error)
}

type launcherPorts interface {
	Check(context.Context) error
	Register(context.Context, string) error
}

type launcherDependencies struct {
	checkCandidate func(context.Context, LauncherConfig) error
	newRunID       func() (string, error)
	readPolicy     func(LauncherConfig) ([]byte, error)
	openWorkspace  func(string, string) (launcherWorkspace, error)
	openPorts      func(string, string, launcherWorkspace) (launcherPorts, error)
	openTopology   func(DockerTopologyConfig) (launcherTopology, error)
	openSource     func(CredentialSourceConfig) (launcherCredentialSource, error)
	newProvider    func(context.Context, string, launcherCredentialSource, launcherPorts) (launcherProviderBinding, error)
	newCreator     func(LauncherConfig, string, []byte, launcherPorts) (launcherExecutionCreator, error)
	newHarness     func(LauncherConfig, string, execution.Execution, launcherPorts, launcherProviderBinding, launcherExecutionCreator, launcherWorkspace) (launcherHarness, error)
}

type launcherState struct {
	runID            string
	resources        Resources
	topology         launcherTopology
	source           launcherCredentialSource
	provider         launcherProviderBinding
	creator          launcherExecutionCreator
	executionCreated bool
	workspace        launcherWorkspace
}

// Launch composes one checked runtime-conformance run and releases its result
// only after every runtime resource and the Docker topology are removed.
func Launch(ctx context.Context, config LauncherConfig) (Result, error) {
	return launch(ctx, config, defaultLauncherDependencies())
}

func launch(
	ctx context.Context,
	config LauncherConfig,
	dependencies launcherDependencies,
) (result Result, outcome error) {
	if ctx == nil || !validLauncherConfig(config) || !validLauncherDependencies(dependencies) {
		return Result{}, ErrLauncherConfiguration
	}
	if err := ctx.Err(); err != nil {
		return Result{}, errors.Join(ErrLauncherRun, err)
	}
	runID, err := dependencies.newRunID()
	if err != nil || !runIDPattern.MatchString(runID) {
		return Result{}, ErrLauncherConfiguration
	}
	state := &launcherState{runID: runID, resources: namesForRun(runID)}
	phase := "policy"
	defer func() {
		if err := state.cleanup(); err != nil {
			result = Result{}
			outcome = launcherFailure(ctx, ErrLauncherCleanup)
			phase = "cleanup"
		}
		if outcome != nil && config.diagnosticModel != "" {
			var failure *LocalDiagnosticError
			if phase == "cleanup" || !errors.As(outcome, &failure) {
				outcome = &LocalDiagnosticError{stage: phase}
			}
		}
	}()

	if config.candidateImage != "" {
		phase = "candidate-credential-check"
		if dependencies.checkCandidate == nil || dependencies.checkCandidate(ctx, config) != nil {
			return Result{}, ErrLauncherRun
		}
	}
	phase = "policy"
	policy, err := dependencies.readPolicy(config)
	if err != nil {
		return Result{}, ErrLauncherConfiguration
	}
	defer clear(policy)

	phase = "workspace"
	workspace, err := dependencies.openWorkspace(config.WorkspaceRoot, runID)
	if err != nil {
		return Result{}, ErrLauncherConfiguration
	}
	state.workspace = workspace
	phase = "provider-preflight"
	ports, err := dependencies.openPorts(config.OpenShellBinary, runID, workspace)
	if err != nil || ports == nil {
		return Result{}, ErrLauncherConfiguration
	}
	if err := ports.Check(ctx); err != nil {
		return Result{}, launcherFailure(ctx, ErrLauncherRun)
	}

	phase = "topology"
	topology, err := dependencies.openTopology(DockerTopologyConfig{
		RunID:          runID,
		RepositoryRoot: config.RepositoryRoot,
		WorkspaceRoot:  config.WorkspaceRoot,
		DockerBinary:   config.DockerBinary,
	})
	if err != nil || topology == nil {
		return Result{}, ErrLauncherConfiguration
	}
	state.topology = topology
	if err := topology.Start(ctx); err != nil {
		return Result{}, launcherFailure(ctx, ErrLauncherRun)
	}
	phase = "gateway-registration"
	if err := ports.Register(ctx, runID); err != nil {
		return Result{}, launcherFailure(ctx, ErrLauncherRun)
	}

	phase = "credential-source"
	source, err := dependencies.openSource(CredentialSourceConfig{
		Directory: config.CredentialDirectory,
	})
	if err != nil || source == nil {
		return Result{}, ErrLauncherConfiguration
	}
	state.source = source
	phase = "provider-binding"
	provider, err := dependencies.newProvider(ctx, runID, source, ports)
	if err != nil || provider == nil {
		return Result{}, launcherFailure(ctx, ErrLauncherRun)
	}
	state.provider = provider
	if err := provider.Provision(ctx); err != nil {
		return Result{}, launcherFailure(ctx, ErrLauncherRun)
	}

	phase = "execution"
	creator, err := dependencies.newCreator(config, runID, policy, ports)
	if err != nil || creator == nil {
		return Result{}, launcherFailure(ctx, ErrLauncherRun)
	}
	state.creator = creator
	executionValue, err := creator.Create(ctx)
	if err != nil {
		return Result{}, launcherFailure(ctx, ErrLauncherRun)
	}
	state.executionCreated = true

	phase = "harness"
	harness, err := dependencies.newHarness(
		config,
		runID,
		executionValue,
		ports,
		provider,
		creator,
		workspace,
	)
	if err != nil || harness == nil {
		return Result{}, launcherFailure(ctx, ErrLauncherRun)
	}
	phase = "live-cases"
	result, err = harness.Run(ctx)
	if err != nil {
		var failure *LocalDiagnosticError
		if config.diagnosticModel != "" && errors.As(err, &failure) {
			return Result{}, failure
		}
		return Result{}, launcherFailure(ctx, ErrLauncherRun)
	}
	return result, nil
}

func defaultLauncherDependencies() launcherDependencies {
	return launcherDependencies{
		checkCandidate: func(ctx context.Context, config LauncherConfig) error {
			return canarylauncher.CheckCandidate(ctx, canarylauncher.Config{
				RepositoryRoot: config.RepositoryRoot, WorkspaceRoot: config.WorkspaceRoot,
				OpenShellBinary: config.OpenShellBinary, DockerBinary: config.DockerBinary,
			}, config.candidateImage)
		},
		newRunID:      newRuntimeLauncherRunID,
		readPolicy:    readRuntimeLauncherPolicy,
		openWorkspace: newRuntimeLauncherWorkspace,
		openPorts:     newRuntimeLauncherPorts,
		openTopology: func(config DockerTopologyConfig) (launcherTopology, error) {
			return NewDockerTopology(config)
		},
		openSource: func(config CredentialSourceConfig) (launcherCredentialSource, error) {
			return NewRuntimeCredentialSource(config)
		},
		newProvider: newRuntimeLauncherProvider,
		newCreator:  newRuntimeLauncherCreator,
		newHarness:  newRuntimeLauncherHarness,
	}
}

func validLauncherConfig(config LauncherConfig) bool {
	return validCandidateSelection(config.candidateImage, config.diagnosticModel) && config.RepositoryRoot != "" &&
		config.WorkspaceRoot != "" &&
		config.CredentialDirectory != "" &&
		validRunProvenance(config.Provenance, config.diagnosticModel)
}

func validLauncherDependencies(dependencies launcherDependencies) bool {
	return dependencies.newRunID != nil &&
		dependencies.readPolicy != nil &&
		dependencies.openWorkspace != nil &&
		dependencies.openPorts != nil &&
		dependencies.openTopology != nil &&
		dependencies.openSource != nil &&
		dependencies.newProvider != nil &&
		dependencies.newCreator != nil &&
		dependencies.newHarness != nil
}

func newRuntimeLauncherRunID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		clear(value)
		return "", ErrLauncherConfiguration
	}
	runID := hex.EncodeToString(value)
	clear(value)
	return runID, nil
}

func readRuntimeLauncherPolicy(config LauncherConfig) ([]byte, error) {
	root, err := resolveRuntimeTopologyDirectory(config.RepositoryRoot, false)
	if err != nil {
		return nil, ErrLauncherConfiguration
	}
	path, digest := runtimeLauncherPolicyPath, runtimePolicySHA256
	if config.candidateImage != "" {
		path, digest = candidateRuntimePolicyPath, candidateRuntimePolicySHA256
	}
	return readRuntimeTopologyFile(filepath.Join(root, filepath.FromSlash(path)), digest)
}

type runtimeLauncherPorts struct {
	store    execution.StateStore
	provider *openshell.Provider
}

func newRuntimeLauncherPorts(
	binary string,
	runID string,
	workspace launcherWorkspace,
) (launcherPorts, error) {
	owned, ok := workspace.(*runtimeLauncherWorkspace)
	if !ok || owned == nil || !runIDPattern.MatchString(runID) {
		return nil, ErrLauncherConfiguration
	}
	if binary == "" {
		binary = "openshell"
	}
	resolved, err := resolveRuntimeTopologyBinary(binary)
	if err != nil {
		return nil, ErrLauncherConfiguration
	}
	resources := namesForRun(runID)
	profiles, err := execution.NewProviderProfileRegistry([]string{resources.Provider})
	if err != nil {
		return nil, ErrLauncherConfiguration
	}
	store := execution.NewMemoryStateStore()
	provider := openshell.New(openshell.Config{
		Binary:           resolved,
		ExpectedVersion:  runtimeLauncherOpenShellVersion,
		PolicyWorkspace:  owned.policy,
		ExportWorkspace:  owned.export,
		StateStore:       store,
		ProviderProfiles: profiles,
	}, openshell.ExecRunner{Environment: runtimeLauncherOpenShellEnvironment(owned.path)})
	return &runtimeLauncherPorts{store: store, provider: provider}, nil
}

func (ports *runtimeLauncherPorts) Check(ctx context.Context) error {
	if ports == nil || ports.provider == nil || ctx == nil {
		return ErrLauncherConfiguration
	}
	if err := ports.provider.Check(ctx); err != nil {
		return ErrLauncherRun
	}
	return nil
}

func (ports *runtimeLauncherPorts) Register(ctx context.Context, runID string) error {
	if ports == nil || ports.provider == nil || ports.store == nil || ctx == nil ||
		!runIDPattern.MatchString(runID) {
		return ErrLauncherConfiguration
	}
	resources := namesForRun(runID)
	isolationID := runtimeIsolationDomain(runID)
	value, err := ports.provider.RegisterGateway(ctx, execution.GatewayRegistration{
		IsolationDomainID: isolationID,
		ID:                resources.Gateway,
		Endpoint:          gatewayEndpoint,
		Driver:            driver,
		Capabilities:      []string{openShellRuntimeCapability},
	})
	if err != nil || !validRuntimeLauncherGateway(value, isolationID, resources.Gateway) {
		return ErrLauncherRun
	}
	record, err := ports.store.GetGateway(ctx, isolationID, resources.Gateway)
	if err != nil || record.Endpoint != gatewayEndpoint ||
		!validRuntimeLauncherGateway(record.Gateway, isolationID, resources.Gateway) {
		return ErrLauncherRun
	}
	return nil
}

func validRuntimeLauncherGateway(value execution.Gateway, isolationID string, gatewayID string) bool {
	return value.IsolationDomainID == isolationID &&
		value.ID == gatewayID &&
		value.Driver == driver &&
		value.State == execution.GatewayActive &&
		len(value.Capabilities) == 1 &&
		value.Capabilities[0] == openShellRuntimeCapability
}

func newRuntimeLauncherProvider(
	ctx context.Context,
	runID string,
	source launcherCredentialSource,
	ports launcherPorts,
) (launcherProviderBinding, error) {
	credentialSource, sourceOK := source.(*RuntimeCredentialSource)
	runtimePorts, portsOK := ports.(*runtimeLauncherPorts)
	if !sourceOK || !portsOK || runtimePorts.provider == nil {
		return nil, ErrLauncherConfiguration
	}
	return NewRuntimeProviderFromCredentialSource(ctx, RuntimeProviderSourceConfig{
		RunID:    runID,
		Source:   credentialSource,
		Provider: runtimePorts.provider,
	})
}

func newRuntimeLauncherCreator(
	config LauncherConfig,
	runID string,
	policy []byte,
	ports launcherPorts,
) (launcherExecutionCreator, error) {
	runtimePorts, ok := ports.(*runtimeLauncherPorts)
	if !ok || runtimePorts.provider == nil || runtimePorts.store == nil {
		return nil, ErrLauncherConfiguration
	}
	return NewExecutionCreator(ExecutionCreationConfig{
		diagnosticImage: config.candidateImage,
		RunID:           runID,
		Policy:          policy,
		Store:           runtimePorts.store,
		Provider:        runtimePorts.provider,
	})
}

func newRuntimeLauncherHarness(
	config LauncherConfig,
	runID string,
	executionValue execution.Execution,
	ports launcherPorts,
	provider launcherProviderBinding,
	creator launcherExecutionCreator,
	workspace launcherWorkspace,
) (launcherHarness, error) {
	runtimePorts, portsOK := ports.(*runtimeLauncherPorts)
	runtimeProvider, providerOK := provider.(*RuntimeProvider)
	runtimeCreator, creatorOK := creator.(*ExecutionCreator)
	if !portsOK || !providerOK || !creatorOK || runtimePorts.provider == nil ||
		runtimePorts.store == nil || workspace == nil {
		return nil, ErrLauncherConfiguration
	}
	return NewHarness(HarnessConfig{
		diagnosticModel: config.diagnosticModel,
		candidateImage:  config.candidateImage,
		RunID:           runID,
		Provenance:      config.Provenance,
		ExecutionID:     executionValue.ID,
		Store:           runtimePorts.store,
		Provider:        runtimePorts.provider,
		Cleanup: Cleanup{
			Sandbox:         runtimeCreator.Cleanup,
			ProviderBinding: runtimeProvider.Cleanup,
			Workspace:       workspace.Cleanup,
		},
	})
}

func (state *launcherState) cleanup() error {
	if state == nil {
		return ErrLauncherCleanup
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeLauncherCleanupTimeout)
	defer cancel()
	var outcome error
	if state.creator != nil && state.executionCreated {
		if err := state.creator.Cleanup(ctx, CleanupRequest{
			RunID:        state.runID,
			ResourceKind: "sandbox",
			ResourceName: state.resources.Sandbox,
		}); err != nil {
			outcome = errors.Join(outcome, ErrLauncherCleanup)
		}
	}
	if state.provider != nil {
		if err := state.provider.Cleanup(ctx, CleanupRequest{
			RunID:        state.runID,
			ResourceKind: "provider",
			ResourceName: state.resources.Provider,
		}); err != nil {
			outcome = errors.Join(outcome, ErrLauncherCleanup)
		}
	}
	if state.source != nil {
		if err := state.source.Cleanup(ctx); err != nil {
			outcome = errors.Join(outcome, ErrLauncherCleanup)
		}
	}
	if state.workspace != nil {
		if err := state.workspace.Cleanup(ctx, CleanupRequest{
			RunID:        state.runID,
			ResourceKind: "workspace",
			ResourceName: state.resources.Workspace,
		}); err != nil {
			outcome = errors.Join(outcome, ErrLauncherCleanup)
		}
	}
	if state.topology != nil {
		if err := state.topology.Cleanup(ctx); err != nil {
			outcome = errors.Join(outcome, ErrLauncherCleanup)
		}
	}
	return outcome
}

func runtimeLauncherOpenShellEnvironment(home string) []string {
	return []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + home,
	}
}

func launcherFailure(ctx context.Context, sentinel error) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return errors.Join(sentinel, err)
		}
	}
	return sentinel
}

func (LauncherConfig) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var _ json.Marshaler = LauncherConfig{}
