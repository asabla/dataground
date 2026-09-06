package canarylauncher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	"github.com/asabla/dataground/internal/runtime/codex"
	"github.com/asabla/dataground/internal/security/canarycollect"
	"github.com/asabla/dataground/internal/security/canarydocker"
	"github.com/asabla/dataground/internal/security/canaryevidence"
	"github.com/asabla/dataground/internal/security/canaryharness"
	"github.com/asabla/dataground/internal/security/canaryprofile"
	"github.com/asabla/dataground/internal/security/canaryprovider"
	"github.com/asabla/dataground/internal/security/canaryruntime"
	"github.com/asabla/dataground/internal/security/canarysandbox"
	"github.com/asabla/dataground/internal/security/canaryworkspace"
)

const (
	runtimeCapability = "codex.app-server"
	readyTimeout      = 5 * time.Minute
	readyPollInterval = time.Second
)

type FailureStage string

const (
	FailureStageConfiguration                     FailureStage = "configuration"
	FailureStageGateway                           FailureStage = "gateway"
	FailureStageProviderCheck                     FailureStage = "provider-check"
	FailureStageProviderRegistration              FailureStage = "provider-registration"
	FailureStageProviderSettingsObservation       FailureStage = "provider-settings-observation"
	FailureStageProviderSettingsMutation          FailureStage = "provider-settings-mutation"
	FailureStageProviderSettingsVerification      FailureStage = "provider-settings-verification"
	FailureStageProviderProvision                 FailureStage = "provider-provision"
	FailureStageProviderBinding                   FailureStage = "provider-binding"
	FailureStagePlacement                         FailureStage = "placement"
	FailureStageSandboxCreate                     FailureStage = "sandbox-create"
	FailureStageSandboxCreatePermission           FailureStage = "sandbox-create-permission"
	FailureStageSandboxCreateMissing              FailureStage = "sandbox-create-missing-path"
	FailureStageSandboxCreateImage                FailureStage = "sandbox-create-image"
	FailureStageSandboxCreateSupervisor           FailureStage = "sandbox-create-supervisor"
	FailureStageSandboxCreateProvider             FailureStage = "sandbox-create-provider"
	FailureStageSandboxCreateObservation          FailureStage = "sandbox-create-observation"
	FailureStageSandboxCreatePolicy               FailureStage = "sandbox-create-policy"
	FailureStageSandboxCreateNetwork              FailureStage = "sandbox-create-network"
	FailureStageSandboxCreateTimeout              FailureStage = "sandbox-create-timeout"
	FailureStageSandboxCreateOverflow             FailureStage = "sandbox-create-diagnostic-overflow"
	FailureStageSandboxCreateArgument             FailureStage = "sandbox-create-argument"
	FailureStageSandboxCreateArgumentGateway      FailureStage = "sandbox-create-argument-gateway"
	FailureStageSandboxCreateArgumentName         FailureStage = "sandbox-create-argument-name"
	FailureStageSandboxCreateArgumentImage        FailureStage = "sandbox-create-argument-image"
	FailureStageSandboxCreateArgumentPolicy       FailureStage = "sandbox-create-argument-policy"
	FailureStageSandboxCreateArgumentAutoProvider FailureStage = "sandbox-create-argument-auto-provider"
	FailureStageSandboxCreateArgumentApproval     FailureStage = "sandbox-create-argument-approval"
	FailureStageSandboxCreateArgumentLabel        FailureStage = "sandbox-create-argument-label"
	FailureStageSandboxCreateArgumentProvider     FailureStage = "sandbox-create-argument-provider"
	FailureStageSandboxCreateArgumentCommand      FailureStage = "sandbox-create-argument-command"
	FailureStageSandboxCreateAuth                 FailureStage = "sandbox-create-authentication"
	FailureStageSandboxCreateConflict             FailureStage = "sandbox-create-conflict"
	FailureStageSandboxCreateStorage              FailureStage = "sandbox-create-storage"
	FailureStageSandboxCreateDriver               FailureStage = "sandbox-create-compute-driver"
	FailureStageSandboxReady                      FailureStage = "sandbox-readiness"
	FailureStageSandboxReadyError                 FailureStage = "sandbox-readiness-error"
	FailureStageSandboxReadyTerminal              FailureStage = "sandbox-readiness-terminal"
	FailureStageSandboxReadyTimeout               FailureStage = "sandbox-readiness-timeout"
	FailureStageSourceBinding                     FailureStage = "source-binding"
	FailureStageRuntime                           FailureStage = "runtime"
	FailureStageDockerSourceBinding               FailureStage = "collection-docker-source-binding"
	FailureStageHarnessComposition                FailureStage = "collection-harness-composition"
	FailureStageHarnessRun                        FailureStage = "collection-harness-run"
	FailureStageHarnessCleanup                    FailureStage = "collection-cleanup"
	FailureStageSandboxProcess                    FailureStage = "collection-sandbox-process"
	FailureStageSandboxEnvironment                FailureStage = "collection-sandbox-environment"
	FailureStageSandboxFilesystem                 FailureStage = "collection-sandbox-filesystem"
	FailureStageProviderArguments                 FailureStage = "collection-provider-arguments"
	FailureStageGatewayLogs                       FailureStage = "collection-gateway-logs"
	FailureStageSandboxLogs                       FailureStage = "collection-sandbox-logs"
	FailureStageRuntimeErrors                     FailureStage = "collection-runtime-errors"
	FailureStageCleanup                           FailureStage = "cleanup"
	FailureStageSerialization                     FailureStage = "serialization"
)

var (
	ErrInvalidConfiguration = errors.New("invalid credential evidence launcher configuration")
	ErrTopologyDrift        = errors.New("credential evidence topology does not match the checked profile")
	ErrLaunch               = errors.New("credential evidence launch failed")
	ErrSerialization        = errors.New("credential evidence launcher cannot be serialized")
	runIDPattern            = regexp.MustCompile("^[a-f0-9]{32}$")
)

type Config struct {
	RepositoryRoot  string
	WorkspaceRoot   string
	OpenShellBinary string
	DockerBinary    string
}

// Run starts one isolated Docker topology, creates the provider-bound sandbox,
// wraps the exact Codex session, and releases evidence only after every owned
// resource and the run-scoped gateway volume have been removed.
func Run(ctx context.Context, config Config) (canaryevidence.Result, error) {
	return run(ctx, config, "")
}

func run(ctx context.Context, config Config, diagnosticImage string) (canaryevidence.Result, error) {
	if ctx == nil || (diagnosticImage != "" && !candidateImagePattern.MatchString(diagnosticImage)) {
		return canaryevidence.Result{}, ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return canaryevidence.Result{}, errors.Join(ErrLaunch, err)
	}
	resolved, err := resolveConfig(config)
	if err != nil {
		return canaryevidence.Result{}, err
	}

	composeContent, err := readVerifiedFile(resolved.composeFile, canaryprofile.ComposeSHA256)
	if err != nil {
		return canaryevidence.Result{}, err
	}
	defer clear(composeContent)
	gatewayContent, err := readVerifiedFile(
		resolved.gatewayConfig,
		canaryprofile.GatewayConfigSHA256,
	)
	if err != nil {
		return canaryevidence.Result{}, err
	}
	defer clear(gatewayContent)
	policy, err := readVerifiedFile(resolved.policyFile, canaryprofile.PolicySHA256)
	if err != nil {
		return canaryevidence.Result{}, err
	}
	defer clear(policy)

	runID, err := newRunID()
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	names, err := canaryharness.NamesForRun(runID)
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}

	workspace, err := canaryworkspace.Open(canaryworkspace.Config{
		Root:  resolved.workspaceRoot,
		RunID: runID,
	})
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	state := cleanupState{
		runID:     runID,
		names:     names,
		workspace: workspace,
	}
	defer func() {
		_ = state.cleanup()
	}()

	topology, err := openTopologyWorkspace(
		resolved.workspaceRoot,
		runID,
		composeContent,
		gatewayContent,
	)
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	state.topology = topology

	policyWorkspace, err := openshell.OpenPolicyWorkspace(
		filepath.Join(resolved.workspaceRoot, ".policy"),
	)
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	state.policyWorkspace = policyWorkspace

	userID, groupID, dockerGroupID, err := dockerProcessIdentity()
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageGateway)
	}
	host, err := newComposeHost(
		runID,
		names,
		resolved.dockerBinary,
		topology.ComposePath(),
		topology.StatePath(),
		topology.JWTPath(),
		userID,
		groupID,
		dockerGroupID,
		execCommandRunner{},
	)
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	state.host = host
	containerID, err := host.Start(ctx)
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageGateway)
	}

	profiles, err := execution.NewProviderProfileRegistry([]string{names.Provider})
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	store := execution.NewMemoryStateStore()
	provider := openshell.New(openshell.Config{
		Binary:           resolved.openShellBinary,
		ExpectedVersion:  canaryprofile.OpenShellVersion,
		PolicyWorkspace:  policyWorkspace,
		StateStore:       store,
		ProviderProfiles: profiles,
	}, openshell.ExecRunner{Environment: openShellEnvironment()})
	if err := provider.Check(ctx); err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageProviderCheck)
	}

	isolationDomainID := "dg-canary-domain-" + runID
	operationID := "dg-canary-operation-" + runID
	if _, err := provider.RegisterGateway(ctx, execution.GatewayRegistration{
		IsolationDomainID: isolationDomainID,
		ID:                names.Gateway,
		Endpoint:          canaryprofile.GatewayEndpoint,
		Driver:            canaryprofile.Driver,
		Capabilities:      []string{runtimeCapability},
	}); err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageProviderRegistration)
	}
	if err := provider.EnableProviderProfiles(
		ctx,
		isolationDomainID,
		names.Gateway,
	); err != nil {
		return canaryevidence.Result{}, launchError(ctx, providerSettingsStage(err))
	}

	provisioned, err := canaryprovider.Provision(
		ctx,
		canaryprovider.ProvisionConfig{
			RunID:             runID,
			IsolationDomainID: isolationDomainID,
			GatewayID:         names.Gateway,
		},
		provider,
	)
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageProviderProvision)
	}
	providerCleanup, err := canaryprovider.New(canaryprovider.Config{
		RunID:   runID,
		Binding: provisioned.Binding(),
	}, provider)
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageProviderBinding)
	}
	state.provider = providerCleanup

	placement, err := provider.SelectGateway(ctx, execution.PlacementRequest{
		IsolationDomainID:    isolationDomainID,
		OperationID:          operationID,
		RequiredCapabilities: []string{runtimeCapability},
	})
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStagePlacement)
	}
	sandboxImage := canaryprofile.SandboxImage
	if diagnosticImage != "" {
		sandboxImage = diagnosticImage
	}
	create := provider.Create
	if diagnosticImage != "" {
		create = provider.CreateLocalDiagnostic
	}
	executionValue, err := create(ctx, execution.CreateRequest{
		Placement:         placement,
		IsolationDomainID: isolationDomainID,
		OperationID:       operationID,
		Image:             sandboxImage,
		Policy:            policy,
		PolicyDigest:      "sha256:" + canaryprofile.PolicySHA256,
		ProviderProfiles:  []string{names.Provider},
	})
	if err != nil {
		recoveryExecution, recoveryErr := store.GetExecutionByOperation(
			context.Background(),
			isolationDomainID,
			operationID,
		)
		if recoveryErr == nil {
			if cleanup, cleanupErr := canarysandbox.New(canarysandbox.Config{
				RunID:     runID,
				Execution: recoveryExecution,
			}, provider); cleanupErr == nil {
				state.sandbox = cleanup
				state.sandboxName = recoveryExecution.ID
			}
		}
		return canaryevidence.Result{}, launchError(ctx, sandboxCreateStage(err))
	}
	sandboxCleanup, err := canarysandbox.New(canarysandbox.Config{
		RunID:     runID,
		Execution: executionValue,
	}, provider)
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageSandboxCreate)
	}
	state.sandbox = sandboxCleanup
	state.sandboxName = executionValue.ID

	executionValue, err = waitForReady(ctx, provider, executionValue)
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx, sandboxReadinessStage(err))
	}
	openShellSources, err := provider.NewCredentialEvidenceSources(
		ctx,
		openshell.CredentialEvidenceSourceConfig{
			RunID: runID,
			Execution: execution.ExecutionRef{
				IsolationDomainID: executionValue.IsolationDomainID,
				ID:                executionValue.ID,
			},
		},
	)
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageSourceBinding)
	}

	session, err := provider.StartRuntime(ctx, execution.ExecutionRef{
		IsolationDomainID: executionValue.IsolationDomainID,
		ID:                executionValue.ID,
	})
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageRuntime)
	}
	runtimeSources, err := canaryruntime.New(canaryruntime.Config{
		RunID:       runID,
		RuntimeName: names.Runtime,
		Session:     session,
	})
	if err != nil {
		_ = session.Close()
		return canaryevidence.Result{}, launchError(ctx, FailureStageRuntime)
	}
	state.runtime = runtimeSources
	if _, err := codex.New(runtimeSources); err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageRuntime)
	}

	dockerSources, err := canarydocker.New(ctx, canarydocker.Config{
		RunID:          runID,
		GatewayName:    names.Gateway,
		ProviderName:   names.Provider,
		ContainerID:    containerID,
		GatewayImage:   canaryprofile.GatewayImage,
		ComposeProject: host.project,
		DockerBinary:   resolved.dockerBinary,
	})
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageDockerSourceBinding)
	}
	harness, err := canaryharness.New(canaryharness.Config{
		DiagnosticImage: diagnosticImage,
		RunID:           runID,
		Provider:        provider,
		Provisioned:     provisioned,
		Execution:       executionValue,
		Workspace:       workspace,
		OpenShell:       openShellSources,
		Docker:          dockerSources,
		Runtime:         runtimeSources,
	})
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageHarnessComposition)
	}
	result, err := harness.Run(ctx)
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx, harnessRunStage(err))
	}
	if err := state.cleanup(); err != nil {
		return canaryevidence.Result{}, launchError(ctx, FailureStageCleanup)
	}
	return result, nil
}

type resolvedConfig struct {
	workspaceRoot   string
	composeFile     string
	gatewayConfig   string
	policyFile      string
	openShellBinary string
	dockerBinary    string
}

func resolveConfig(config Config) (resolvedConfig, error) {
	if config.RepositoryRoot == "" || config.WorkspaceRoot == "" {
		return resolvedConfig{}, ErrInvalidConfiguration
	}
	repositoryRoot, err := filepath.Abs(config.RepositoryRoot)
	if err != nil {
		return resolvedConfig{}, ErrInvalidConfiguration
	}
	repositoryRoot, err = filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return resolvedConfig{}, ErrInvalidConfiguration
	}
	workspaceRoot, err := filepath.Abs(config.WorkspaceRoot)
	if err != nil {
		return resolvedConfig{}, ErrInvalidConfiguration
	}
	workspaceRoot, err = filepath.EvalSymlinks(workspaceRoot)
	if err != nil || pathsOverlap(repositoryRoot, workspaceRoot) {
		return resolvedConfig{}, ErrInvalidConfiguration
	}
	openShellBinary := config.OpenShellBinary
	if openShellBinary == "" {
		openShellBinary = "openshell"
	}
	openShellBinary, err = resolveBinary(openShellBinary)
	if err != nil {
		return resolvedConfig{}, err
	}
	dockerBinary := config.DockerBinary
	if dockerBinary == "" {
		dockerBinary = "docker"
	}
	dockerBinary, err = resolveBinary(dockerBinary)
	if err != nil {
		return resolvedConfig{}, err
	}
	return resolvedConfig{
		workspaceRoot:   filepath.Clean(workspaceRoot),
		composeFile:     filepath.Join(repositoryRoot, filepath.FromSlash(canaryprofile.ComposePath)),
		gatewayConfig:   filepath.Join(repositoryRoot, filepath.FromSlash(canaryprofile.GatewayConfigPath)),
		policyFile:      filepath.Join(repositoryRoot, filepath.FromSlash(canaryprofile.PolicyPath)),
		openShellBinary: openShellBinary,
		dockerBinary:    dockerBinary,
	}, nil
}

func harnessRunStage(err error) FailureStage {
	if errors.Is(err, canaryevidence.ErrCleanup) {
		return FailureStageHarnessCleanup
	}
	surface := canarycollect.FailureSurfaceOf(err)
	stage := FailureStageHarnessRun
	switch surface {
	case "sandbox-process":
		stage = FailureStageSandboxProcess
	case "sandbox-environment":
		stage = FailureStageSandboxEnvironment
	case "sandbox-filesystem":
		stage = FailureStageSandboxFilesystem
	case "provider-arguments":
		stage = FailureStageProviderArguments
	case "gateway-logs":
		stage = FailureStageGatewayLogs
	case "sandbox-logs":
		stage = FailureStageSandboxLogs
	case "runtime-errors":
		stage = FailureStageRuntimeErrors
	default:
		stage = FailureStageHarnessRun
	}
	return collectionFailureStage(stage, err)
}

func collectionFailureStage(stage FailureStage, err error) FailureStage {
	if stage == FailureStageHarnessRun {
		return stage
	}
	switch {
	case errors.Is(err, canarycollect.ErrCanaryDetected):
		return FailureStage(string(stage) + "-canary-detected")
	case errors.Is(err, canarycollect.ErrScanInputLimit):
		return FailureStage(string(stage) + "-input-limit")
	case errors.Is(err, canarycollect.ErrSourceClose):
		return FailureStage(string(stage) + "-source-close")
	default:
		return stage
	}
}

func providerSettingsStage(err error) FailureStage {
	switch {
	case errors.Is(err, openshell.ErrProviderSettingsMutation):
		return FailureStageProviderSettingsMutation
	case errors.Is(err, openshell.ErrProviderSettingsVerification):
		return FailureStageProviderSettingsVerification
	default:
		return FailureStageProviderSettingsObservation
	}
}

func sandboxCreateStage(err error) FailureStage {
	if errors.Is(err, execution.ErrStateConflict) {
		return FailureStageSandboxCreateConflict
	}
	if errors.Is(err, openshell.ErrProviderObservation) {
		return FailureStageSandboxCreateObservation
	}
	if errors.Is(err, openshell.ErrPolicyWorkspaceUnavailable) ||
		errors.Is(err, openshell.ErrPolicyWorkspaceBusy) ||
		errors.Is(err, openshell.ErrPolicyWorkspaceUnsafe) ||
		errors.Is(err, openshell.ErrPolicyWorkspaceFailure) {
		return FailureStageSandboxCreatePolicy
	}
	class := openshell.NativeFailureClassOf(err)
	if class == openshell.NativeFailureUnknown &&
		errors.Is(err, openshell.ErrProviderFailure) &&
		!openshell.IsNativeCommandFailure(err) {
		return FailureStageSandboxCreateProvider
	}
	switch class {
	case openshell.NativeFailurePermission:
		return FailureStageSandboxCreatePermission
	case openshell.NativeFailureMissing:
		return FailureStageSandboxCreateMissing
	case openshell.NativeFailureImage:
		return FailureStageSandboxCreateImage
	case openshell.NativeFailureSupervisor:
		return FailureStageSandboxCreateSupervisor
	case openshell.NativeFailureProvider:
		return FailureStageSandboxCreateProvider
	case openshell.NativeFailurePolicy:
		return FailureStageSandboxCreatePolicy
	case openshell.NativeFailureNetwork:
		return FailureStageSandboxCreateNetwork
	case openshell.NativeFailureTimeout:
		return FailureStageSandboxCreateTimeout
	case openshell.NativeFailureOverflow:
		return FailureStageSandboxCreateOverflow
	case openshell.NativeFailureArgument:
		return FailureStageSandboxCreateArgument
	case openshell.NativeFailureArgumentGateway:
		return FailureStageSandboxCreateArgumentGateway
	case openshell.NativeFailureArgumentName:
		return FailureStageSandboxCreateArgumentName
	case openshell.NativeFailureArgumentImage:
		return FailureStageSandboxCreateArgumentImage
	case openshell.NativeFailureArgumentPolicy:
		return FailureStageSandboxCreateArgumentPolicy
	case openshell.NativeFailureArgumentAutoProvider:
		return FailureStageSandboxCreateArgumentAutoProvider
	case openshell.NativeFailureArgumentApproval:
		return FailureStageSandboxCreateArgumentApproval
	case openshell.NativeFailureArgumentLabel:
		return FailureStageSandboxCreateArgumentLabel
	case openshell.NativeFailureArgumentProvider:
		return FailureStageSandboxCreateArgumentProvider
	case openshell.NativeFailureArgumentCommand:
		return FailureStageSandboxCreateArgumentCommand
	case openshell.NativeFailureAuth:
		return FailureStageSandboxCreateAuth
	case openshell.NativeFailureConflict:
		return FailureStageSandboxCreateConflict
	case openshell.NativeFailureStorage:
		return FailureStageSandboxCreateStorage
	case openshell.NativeFailureDriver:
		return FailureStageSandboxCreateDriver
	default:
		return FailureStageSandboxCreate
	}
}

type sandboxReadinessError struct {
	state string
}

func (err *sandboxReadinessError) Error() string {
	return ErrLaunch.Error()
}

func sandboxReadinessStage(err error) FailureStage {
	var readiness *sandboxReadinessError
	if !errors.As(err, &readiness) {
		return FailureStageSandboxReady
	}
	switch readiness.state {
	case "error":
		return FailureStageSandboxReadyError
	case "deleting", "failed", "terminated":
		return FailureStageSandboxReadyTerminal
	default:
		return FailureStageSandboxReadyTimeout
	}
}

func waitForReady(
	ctx context.Context,
	provider *openshell.Provider,
	value execution.Execution,
) (execution.Execution, error) {
	if value.State == "ready" {
		return value, nil
	}
	readyCtx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	ticker := time.NewTicker(readyPollInterval)
	defer ticker.Stop()
	ref := execution.ExecutionRef{
		IsolationDomainID: value.IsolationDomainID,
		ID:                value.ID,
	}
	lastState := value.State
	for {
		observation, err := provider.Observe(readyCtx, ref)
		if err != nil {
			return execution.Execution{}, ErrLaunch
		}
		lastState = observation.State
		switch observation.State {
		case "ready":
			value.State = "ready"
			return value, nil
		case "error", "deleting", "failed", "terminated", "unknown":
			return execution.Execution{}, &sandboxReadinessError{state: observation.State}
		}
		select {
		case <-readyCtx.Done():
			return execution.Execution{}, &sandboxReadinessError{state: lastState}
		case <-ticker.C:
		}
	}
}

func newRunID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		clear(random)
		return "", ErrLaunch
	}
	runID := hex.EncodeToString(random)
	clear(random)
	return runID, nil
}

type cleanupState struct {
	runID           string
	names           canaryharness.ResourceNames
	runtime         *canaryruntime.Sources
	sandboxName     string
	sandbox         *canarysandbox.Adapter
	provider        *canaryprovider.Adapter
	workspace       *canaryworkspace.Workspace
	policyWorkspace *openshell.PolicyWorkspace
	host            *composeHost
	topology        *topologyWorkspace
}

func (state *cleanupState) cleanup() error {
	if state == nil {
		return ErrLaunch
	}
	ctx, cancel := cleanupContext()
	defer cancel()
	var outcome error
	if state.runtime != nil {
		state.runtime.Discard()
		if err := state.runtime.Close(); err != nil {
			outcome = errors.Join(outcome, ErrLaunch)
		}
	}
	if state.sandbox != nil {
		if err := state.sandbox.Cleanup(ctx, canaryevidence.CleanupRequest{
			RunID:        state.runID,
			ResourceKind: "sandbox",
			ResourceName: state.sandboxName,
		}); err != nil {
			outcome = errors.Join(outcome, ErrLaunch)
		}
	}
	if state.provider != nil {
		if err := state.provider.Cleanup(ctx, canaryevidence.CleanupRequest{
			RunID:        state.runID,
			ResourceKind: "provider",
			ResourceName: state.names.Provider,
		}); err != nil {
			outcome = errors.Join(outcome, ErrLaunch)
		}
	}
	if state.workspace != nil {
		if err := state.workspace.Cleanup(ctx, canaryevidence.CleanupRequest{
			RunID:        state.runID,
			ResourceKind: "workspace",
			ResourceName: state.names.Workspace,
		}); err != nil {
			outcome = errors.Join(outcome, ErrLaunch)
		}
	}
	if state.policyWorkspace != nil {
		if err := state.policyWorkspace.Close(); err != nil {
			outcome = errors.Join(outcome, ErrLaunch)
		}
	}
	hostRemoved := true
	if state.host != nil {
		if err := state.host.Stop(ctx); err != nil {
			hostRemoved = false
			outcome = errors.Join(outcome, ErrLaunch)
		}
	}
	if state.topology != nil && hostRemoved {
		if err := state.topology.Cleanup(ctx); err != nil {
			outcome = errors.Join(outcome, ErrLaunch)
		}
	}
	return outcome
}

type stagedLaunchError struct {
	stage FailureStage
	cause error
}

func (err *stagedLaunchError) Error() string {
	return ErrLaunch.Error()
}

func (err *stagedLaunchError) Unwrap() []error {
	return []error{ErrLaunch, err.cause}
}

func launchError(ctx context.Context, stage FailureStage) error {
	cause := ErrLaunch
	if err := ctx.Err(); err != nil {
		cause = err
	}
	return &stagedLaunchError{stage: stage, cause: cause}
}

func StageOf(err error) FailureStage {
	var staged *stagedLaunchError
	if errors.As(err, &staged) {
		return staged.stage
	}
	if errors.Is(err, ErrSerialization) {
		return FailureStageSerialization
	}
	return FailureStageConfiguration
}

func (Config) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var _ json.Marshaler = Config{}
