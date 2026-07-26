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
	readyTimeout = 5 * time.Minute
	readyPollInterval = time.Second
)

var (
	ErrInvalidConfiguration = errors.New("invalid credential evidence launcher configuration")
	ErrTopologyDrift = errors.New("credential evidence topology does not match the checked profile")
	ErrLaunch = errors.New("credential evidence launch failed")
	ErrSerialization = errors.New("credential evidence launcher cannot be serialized")
	runIDPattern = regexp.MustCompile("^[a-f0-9]{32}$")
)

type Config struct {
	RepositoryRoot string
	WorkspaceRoot string
	OpenShellBinary string
	DockerBinary string
}

// Run starts one isolated Docker topology, creates the provider-bound sandbox,
// wraps the exact Codex session, and releases evidence only after every owned
// resource and the run-scoped gateway volume have been removed.
func Run(ctx context.Context, config Config) (canaryevidence.Result, error) {
	if ctx == nil {
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
	clear(composeContent)
	gatewayContent, err := readVerifiedFile(
		resolved.gatewayConfig,
		canaryprofile.GatewayConfigSHA256,
	)
	if err != nil {
		return canaryevidence.Result{}, err
	}
	clear(gatewayContent)
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
		Root: resolved.workspaceRoot,
		RunID: runID,
	})
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	state := cleanupState{
		runID: runID,
		names: names,
		workspace: workspace,
	}
	defer func() {
		_ = state.cleanup()
	}()

	policyWorkspace, err := openshell.OpenPolicyWorkspace(
		filepath.Join(resolved.workspaceRoot, ".policy"),
	)
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	state.policyWorkspace = policyWorkspace

	host, err := newComposeHost(
		runID,
		names,
		resolved.dockerBinary,
		resolved.composeFile,
		execCommandRunner{},
	)
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	state.host = host
	containerID, err := host.Start(ctx)
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx)
	}

	profiles, err := execution.NewProviderProfileRegistry([]string{names.Provider})
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	store := execution.NewMemoryStateStore()
	provider := openshell.New(openshell.Config{
		Binary: resolved.openShellBinary,
		ExpectedVersion: canaryprofile.OpenShellVersion,
		PolicyWorkspace: policyWorkspace,
		StateStore: store,
		ProviderProfiles: profiles,
	}, openshell.ExecRunner{Environment: openShellEnvironment()})
	if err := provider.Check(ctx); err != nil {
		return canaryevidence.Result{}, launchError(ctx)
	}

	isolationDomainID := "dg-canary-domain-" + runID
	operationID := "dg-canary-operation-" + runID
	if _, err := provider.RegisterGateway(ctx, execution.GatewayRegistration{
		IsolationDomainID: isolationDomainID,
		ID: names.Gateway,
		Endpoint: canaryprofile.GatewayEndpoint,
		Driver: canaryprofile.Driver,
		Capabilities: []string{runtimeCapability},
	}); err != nil {
		return canaryevidence.Result{}, launchError(ctx)
	}

	provisioned, err := canaryprovider.Provision(
		ctx,
		canaryprovider.ProvisionConfig{
			RunID: runID,
			IsolationDomainID: isolationDomainID,
			GatewayID: names.Gateway,
		},
		provider,
	)
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx)
	}
	providerCleanup, err := canaryprovider.New(canaryprovider.Config{
		RunID: runID,
		Binding: provisioned.Binding(),
	}, provider)
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	state.provider = providerCleanup

	placement, err := provider.SelectGateway(ctx, execution.PlacementRequest{
		IsolationDomainID: isolationDomainID,
		OperationID: operationID,
		RequiredCapabilities: []string{runtimeCapability},
	})
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx)
	}
	executionValue, err := provider.Create(ctx, execution.CreateRequest{
		Placement: placement,
		IsolationDomainID: isolationDomainID,
		OperationID: operationID,
		Image: canaryprofile.SandboxImage,
		Policy: policy,
		PolicyDigest: "sha256:" + canaryprofile.PolicySHA256,
		ProviderProfiles: []string{names.Provider},
	})
	if err != nil {
		recoveryExecution, recoveryErr := store.GetExecutionByOperation(
			context.Background(),
			isolationDomainID,
			operationID,
		)
		if recoveryErr == nil {
			if cleanup, cleanupErr := canarysandbox.New(canarysandbox.Config{
				RunID: runID,
				Execution: recoveryExecution,
			}, provider); cleanupErr == nil {
				state.sandbox = cleanup
				state.sandboxName = recoveryExecution.ID
			}
		}
		return canaryevidence.Result{}, launchError(ctx)
	}
	sandboxCleanup, err := canarysandbox.New(canarysandbox.Config{
		RunID: runID,
		Execution: executionValue,
	}, provider)
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	state.sandbox = sandboxCleanup
	state.sandboxName = executionValue.ID

	executionValue, err = waitForReady(ctx, provider, executionValue)
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx)
	}
	openShellSources, err := provider.NewCredentialEvidenceSources(
		ctx,
		openshell.CredentialEvidenceSourceConfig{
			RunID: runID,
			Execution: execution.ExecutionRef{
				IsolationDomainID: executionValue.IsolationDomainID,
				ID: executionValue.ID,
			},
		},
	)
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx)
	}

	session, err := provider.StartRuntime(ctx, execution.ExecutionRef{
		IsolationDomainID: executionValue.IsolationDomainID,
		ID: executionValue.ID,
	})
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx)
	}
	runtimeSources, err := canaryruntime.New(canaryruntime.Config{
		RunID: runID,
		RuntimeName: names.Runtime,
		Session: session,
	})
	if err != nil {
		_ = session.Close()
		return canaryevidence.Result{}, ErrLaunch
	}
	state.runtime = runtimeSources
	if _, err := codex.New(runtimeSources); err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}

	dockerSources, err := canarydocker.New(ctx, canarydocker.Config{
		RunID: runID,
		GatewayName: names.Gateway,
		ProviderName: names.Provider,
		ContainerID: containerID,
		GatewayImage: canaryprofile.GatewayImage,
		ComposeProject: host.project,
		DockerBinary: resolved.dockerBinary,
	})
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx)
	}
	harness, err := canaryharness.New(canaryharness.Config{
		RunID: runID,
		Provider: provider,
		Provisioned: provisioned,
		Execution: executionValue,
		Workspace: workspace,
		OpenShell: openShellSources,
		Docker: dockerSources,
		Runtime: runtimeSources,
	})
	if err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	result, err := harness.Run(ctx)
	if err != nil {
		return canaryevidence.Result{}, launchError(ctx)
	}
	if err := state.cleanup(); err != nil {
		return canaryevidence.Result{}, ErrLaunch
	}
	return result, nil
}

type resolvedConfig struct {
	workspaceRoot string
	composeFile string
	gatewayConfig string
	policyFile string
	openShellBinary string
	dockerBinary string
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
	if err != nil {
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
		workspaceRoot: filepath.Clean(workspaceRoot),
		composeFile: filepath.Join(repositoryRoot, filepath.FromSlash(canaryprofile.ComposePath)),
		gatewayConfig: filepath.Join(repositoryRoot, filepath.FromSlash(canaryprofile.GatewayConfigPath)),
		policyFile: filepath.Join(repositoryRoot, filepath.FromSlash(canaryprofile.PolicyPath)),
		openShellBinary: openShellBinary,
		dockerBinary: dockerBinary,
	}, nil
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
		ID: value.ID,
	}
	for {
		observation, err := provider.Observe(readyCtx, ref)
		if err != nil {
			return execution.Execution{}, ErrLaunch
		}
		switch observation.State {
		case "ready":
			value.State = "ready"
			return value, nil
		case "failed", "terminated":
			return execution.Execution{}, ErrLaunch
		}
		select {
		case <-readyCtx.Done():
			return execution.Execution{}, ErrLaunch
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
	runID string
	names canaryharness.ResourceNames
	runtime *canaryruntime.Sources
	sandboxName string
	sandbox *canarysandbox.Adapter
	provider *canaryprovider.Adapter
	workspace *canaryworkspace.Workspace
	policyWorkspace *openshell.PolicyWorkspace
	host *composeHost
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
			RunID: state.runID,
			ResourceKind: "sandbox",
			ResourceName: state.sandboxName,
		}); err != nil {
			outcome = errors.Join(outcome, ErrLaunch)
		}
	}
	if state.provider != nil {
		if err := state.provider.Cleanup(ctx, canaryevidence.CleanupRequest{
			RunID: state.runID,
			ResourceKind: "provider",
			ResourceName: state.names.Provider,
		}); err != nil {
			outcome = errors.Join(outcome, ErrLaunch)
		}
	}
	if state.workspace != nil {
		if err := state.workspace.Cleanup(ctx, canaryevidence.CleanupRequest{
			RunID: state.runID,
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
	if state.host != nil {
		if err := state.host.Stop(ctx); err != nil {
			outcome = errors.Join(outcome, ErrLaunch)
		}
	}
	return outcome
}

func launchError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrLaunch, err)
	}
	return ErrLaunch
}

func (Config) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var _ json.Marshaler = Config{}
