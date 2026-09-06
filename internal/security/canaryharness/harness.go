package canaryharness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sync"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	"github.com/asabla/dataground/internal/security/canarydocker"
	"github.com/asabla/dataground/internal/security/canaryevidence"
	"github.com/asabla/dataground/internal/security/canaryprovider"
	"github.com/asabla/dataground/internal/security/canaryruntime"
	"github.com/asabla/dataground/internal/security/canarysandbox"
	"github.com/asabla/dataground/internal/security/canarysource"
	"github.com/asabla/dataground/internal/security/canaryworkspace"
)

const (
	gatewayNamePrefix   = "dg-canary-gateway-"
	providerNamePrefix  = "dg-canary-provider-"
	runtimeNamePrefix   = "dg-canary-runtime-"
	workspaceNamePrefix = "dg-canary-"
)

var (
	ErrInvalidConfiguration = errors.New("invalid credential evidence harness configuration")
	ErrAlreadyStarted       = errors.New("credential evidence harness already started")
	ErrSerialization        = errors.New("credential evidence harness cannot be serialized")

	runIDPattern      = regexp.MustCompile(`^[a-f0-9]{32}$`)
	resourcePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	commitmentPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// ResourceNames are the portable identities derived for one evidence run.
// Native gateway, sandbox, provider, and runtime identities remain private.
type ResourceNames struct {
	Gateway   string
	Provider  string
	Runtime   string
	Workspace string
}

// NamesForRun returns the only portable names accepted by the closed harness.
func NamesForRun(runID string) (ResourceNames, error) {
	if !runIDPattern.MatchString(runID) {
		return ResourceNames{}, ErrInvalidConfiguration
	}
	return ResourceNames{
		Gateway:   gatewayNamePrefix + runID,
		Provider:  providerNamePrefix + runID,
		Runtime:   runtimeNamePrefix + runID,
		Workspace: workspaceNamePrefix + runID,
	}, nil
}

// Config contains only repository-owned live adapters and the exact resources
// they already created. New derives every portable name from RunID and refuses
// to compose identities from different runs or gateways.
type Config struct {
	DiagnosticImage string
	RunID           string
	Provider        *openshell.Provider
	Provisioned     *canaryprovider.Provisioned
	Execution       execution.Execution
	Workspace       *canaryworkspace.Workspace
	OpenShell       *openshell.CredentialEvidenceSources
	Docker          *canarydocker.Sources
	Runtime         *canaryruntime.Sources
}

// Harness binds one complete evidence plan. Copies share the same irreversible
// single-use state so a live acquisition cannot be replayed through value copies.
type Harness struct {
	state *state
}

type state struct {
	mu      sync.Mutex
	config  canaryevidence.Config
	runtime *canaryruntime.Sources
	started bool
}

type runtimeBoundary struct {
	once    sync.Once
	runtime *canaryruntime.Sources
	err     error
}

func (boundary *runtimeBoundary) OpenRuntimeErrors(
	ctx context.Context,
	request canarysource.Request,
) (io.ReadCloser, error) {
	if boundary == nil || boundary.runtime == nil || ctx == nil {
		return nil, canaryruntime.ErrCredentialSource
	}
	boundary.once.Do(func() {
		boundary.err = boundary.runtime.Close()
	})
	if boundary.err != nil {
		boundary.runtime.Discard()
		return nil, canaryruntime.ErrCredentialSource
	}
	return boundary.runtime.OpenRuntimeErrors(ctx, request)
}

// New closes the composition boundary around the provisioned provider,
// ready execution, verifier workspace, seven source backends, and cleanup ports.
func New(config Config) (*Harness, error) {
	discardRuntime := config.Runtime != nil
	defer func() {
		if discardRuntime {
			config.Runtime.Discard()
		}
	}()

	names, err := NamesForRun(config.RunID)
	if err != nil ||
		config.Provider == nil ||
		config.Provisioned == nil ||
		config.Workspace == nil ||
		config.OpenShell == nil ||
		config.Docker == nil ||
		config.Runtime == nil {
		return nil, ErrInvalidConfiguration
	}

	binding := config.Provisioned.Binding()
	commitment := config.Provisioned.Commitment()
	if binding.IsolationDomainID == "" ||
		binding.GatewayID == "" ||
		binding.ID == "" ||
		binding.Name != names.Provider ||
		binding.ResourceVersion == 0 ||
		!commitmentPattern.MatchString(commitment) ||
		config.Execution.IsolationDomainID != binding.IsolationDomainID ||
		config.Execution.GatewayID != binding.GatewayID ||
		!resourcePattern.MatchString(config.Execution.ID) ||
		config.Execution.State != "ready" ||
		config.Workspace.Name() != names.Workspace {
		return nil, ErrInvalidConfiguration
	}

	sandboxCleanup, err := canarysandbox.New(canarysandbox.Config{
		RunID: config.RunID, Execution: config.Execution,
	}, config.Provider)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	providerCleanup, err := canaryprovider.New(canaryprovider.Config{
		RunID: config.RunID, Binding: binding,
	}, config.Provider)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	sources, err := canarysource.New(canarysource.Config{
		RunID:            config.RunID,
		CanaryCommitment: commitment,
		Resources: canarysource.ResourceNames{
			Gateway:  names.Gateway,
			Sandbox:  config.Execution.ID,
			Provider: names.Provider,
			Runtime:  names.Runtime,
		},
		OpenShell: config.OpenShell,
		Docker:    config.Docker,
		Runtime:   &runtimeBoundary{runtime: config.Runtime},
	})
	if err != nil {
		return nil, ErrInvalidConfiguration
	}

	harness := &Harness{state: &state{
		config: canaryevidence.Config{
			DiagnosticImage:  config.DiagnosticImage,
			RunID:            config.RunID,
			CanaryCommitment: commitment,
			Resources: canaryevidence.Resources{
				Gateway:   names.Gateway,
				Sandbox:   config.Execution.ID,
				Provider:  names.Provider,
				Runtime:   names.Runtime,
				Workspace: names.Workspace,
			},
			Sources: sources,
			Cleanup: canaryevidence.Cleanup{
				Sandbox:         sandboxCleanup.Cleanup,
				ProviderBinding: providerCleanup.Cleanup,
				Workspace:       config.Workspace.Cleanup,
			},
		},
		runtime: config.Runtime,
	}}
	discardRuntime = false
	return harness, nil
}

// Run performs the single live collection and always discards any runtime
// capture that was not consumed because an earlier surface failed.
func (harness *Harness) Run(ctx context.Context) (canaryevidence.Result, error) {
	return harness.runWith(ctx, canaryevidence.Run)
}

type evidenceRunner func(context.Context, canaryevidence.Config) (canaryevidence.Result, error)

func (harness *Harness) runWith(
	ctx context.Context,
	run evidenceRunner,
) (canaryevidence.Result, error) {
	if harness == nil || harness.state == nil || ctx == nil || run == nil {
		return canaryevidence.Result{}, ErrInvalidConfiguration
	}
	if err := ctx.Err(); err != nil {
		return canaryevidence.Result{}, errors.Join(ErrInvalidConfiguration, err)
	}

	state := harness.state
	state.mu.Lock()
	if state.started {
		state.mu.Unlock()
		return canaryevidence.Result{}, ErrAlreadyStarted
	}
	state.started = true
	state.mu.Unlock()

	defer state.runtime.Discard()
	return run(ctx, state.config)
}

func (Config) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (Harness) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var (
	_ json.Marshaler              = Config{}
	_ json.Marshaler              = Harness{}
	_ canarysource.RuntimeSources = (*runtimeBoundary)(nil)
)
