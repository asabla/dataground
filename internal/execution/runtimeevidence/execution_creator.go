package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
)

const (
	runtimePolicySHA256            = "a193c3421b98a1640aa099d91b528beaee91af2a14980ba423ac3050c40649a9"
	runtimeExecutionReadyTimeout   = 5 * time.Minute
	runtimeExecutionPollInterval   = time.Second
	runtimeExecutionCleanupTimeout = time.Minute
)

var (
	ErrExecutionCreationConfiguration = errors.New("invalid runtime conformance execution creation configuration")
	ErrExecutionCreation              = errors.New("runtime conformance execution creation failed")
	ErrExecutionCreationOrder         = errors.New("runtime conformance execution creation order is invalid")
	ErrExecutionCreationCleanup       = errors.New("runtime conformance execution cleanup failed")
)

type ExecutionCreationProvider interface {
	HarnessProvider
	RegisterGateway(context.Context, execution.GatewayRegistration) (execution.Gateway, error)
	EnableProviderProfiles(context.Context, string, string) error
	SelectGateway(context.Context, execution.PlacementRequest) (execution.Placement, error)
	Create(context.Context, execution.CreateRequest) (execution.Execution, error)
}

type ExecutionCreationConfig struct {
	diagnosticImage string
	RunID           string
	Policy          []byte
	Store           HarnessStore
	Provider        ExecutionCreationProvider
}

type ExecutionCreator struct {
	state *executionCreationState
}

type executionCreationState struct {
	image     string
	create    func(context.Context, execution.CreateRequest) (execution.Execution, error)
	mu        sync.Mutex
	runID     string
	resources Resources
	store     HarnessStore
	provider  ExecutionCreationProvider
	policy    []byte
	ref       execution.ExecutionRef
	poll      func(context.Context) error
	started   bool
	created   bool
	cleaning  bool
	cleaned   bool
	failed    bool
}

// NewExecutionCreator owns the immutable OpenShell execution inputs used by
// runtime conformance. Docker topology and provider credential provisioning
// remain launcher responsibilities.
func NewExecutionCreator(config ExecutionCreationConfig) (*ExecutionCreator, error) {
	return newExecutionCreator(config, pollRuntimeExecution)
}

func newExecutionCreator(
	config ExecutionCreationConfig,
	poll func(context.Context) error,
) (*ExecutionCreator, error) {
	if !runIDPattern.MatchString(config.RunID) ||
		len(config.Policy) == 0 ||
		isNilHarnessPort(config.Store) ||
		isNilHarnessPort(config.Provider) ||
		poll == nil ||
		execution.VerifyEnforcementPolicy(
			config.Policy,
			"sha256:"+runtimePolicySHA256,
		) != nil {
		return nil, ErrExecutionCreationConfiguration
	}
	image := sandboxImage
	create := config.Provider.Create
	if config.diagnosticImage != "" {
		provider, ok := config.Provider.(interface {
			CreateLocalDiagnostic(context.Context, execution.CreateRequest) (execution.Execution, error)
		})
		if !ok || !commitmentPattern.MatchString(config.diagnosticImage) {
			return nil, ErrExecutionCreationConfiguration
		}
		image, create = config.diagnosticImage, provider.CreateLocalDiagnostic
	}
	isolationDomainID := runtimeIsolationDomain(config.RunID)
	return &ExecutionCreator{state: &executionCreationState{
		image: image, create: create,
		runID:     config.RunID,
		resources: namesForRun(config.RunID),
		store:     config.Store,
		provider:  config.Provider,
		policy:    slices.Clone(config.Policy),
		ref: execution.ExecutionRef{
			IsolationDomainID: isolationDomainID,
			ID:                runtimeExecutionID(config.RunID),
		},
		poll: poll,
	}}, nil
}

func (creator *ExecutionCreator) Create(ctx context.Context) (execution.Execution, error) {
	if creator == nil || creator.state == nil || ctx == nil {
		return execution.Execution{}, ErrExecutionCreationConfiguration
	}
	state := creator.state
	state.mu.Lock()
	if state.started || state.failed || state.cleaned {
		state.failed = true
		state.mu.Unlock()
		return execution.Execution{}, errors.Join(ErrExecutionCreation, ErrExecutionCreationOrder)
	}
	state.started = true
	policy := state.policy
	state.policy = nil
	state.mu.Unlock()
	defer clear(policy)

	if err := ctx.Err(); err != nil {
		state.fail()
		return execution.Execution{}, errors.Join(ErrExecutionCreation, err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, runtimeExecutionReadyTimeout)
	defer cancel()

	gateway, err := state.provider.RegisterGateway(readyCtx, execution.GatewayRegistration{
		IsolationDomainID: state.ref.IsolationDomainID,
		ID:                state.resources.Gateway,
		Endpoint:          gatewayEndpoint,
		Driver:            driver,
		Capabilities:      []string{openShellRuntimeCapability},
	})
	if err != nil || !validCreatedGateway(gateway, state) || state.invalidAfter(readyCtx) {
		state.fail()
		return execution.Execution{}, state.creationError(readyCtx, false)
	}
	gatewayRecord, err := state.store.GetGateway(
		readyCtx,
		state.ref.IsolationDomainID,
		state.resources.Gateway,
	)
	if err != nil ||
		!validCreatedGateway(gatewayRecord.Gateway, state) ||
		gatewayRecord.Endpoint != gatewayEndpoint ||
		state.invalidAfter(readyCtx) {
		state.fail()
		return execution.Execution{}, state.creationError(readyCtx, false)
	}
	if err := state.provider.EnableProviderProfiles(
		readyCtx,
		state.ref.IsolationDomainID,
		state.resources.Gateway,
	); err != nil || state.invalidAfter(readyCtx) {
		state.fail()
		return execution.Execution{}, state.creationError(readyCtx, false)
	}
	placement, err := state.provider.SelectGateway(readyCtx, execution.PlacementRequest{
		IsolationDomainID:    state.ref.IsolationDomainID,
		OperationID:          runtimeOperationID(state.runID),
		RequiredCapabilities: []string{openShellRuntimeCapability},
	})
	if err != nil || !validCreatedPlacement(placement, state) || state.invalidAfter(readyCtx) {
		state.fail()
		return execution.Execution{}, state.creationError(readyCtx, false)
	}

	requestPolicy := slices.Clone(policy)
	creationStartedAt := time.Now().UTC()
	value, createErr := state.create(readyCtx, execution.CreateRequest{
		Placement:         placement,
		IsolationDomainID: state.ref.IsolationDomainID,
		OperationID:       runtimeOperationID(state.runID),
		Image:             state.image,
		Policy:            requestPolicy,
		PolicyDigest:      "sha256:" + runtimePolicySHA256,
		ProviderProfiles:  []string{state.resources.Provider},
	})
	clear(requestPolicy)
	if createErr != nil ||
		!validCreatedExecution(value, state) ||
		state.invalidAfter(readyCtx) {
		state.fail()
		return execution.Execution{}, state.creationError(readyCtx, true)
	}

	ready, err := state.waitForReady(readyCtx, creationStartedAt)
	if err != nil {
		state.fail()
		return execution.Execution{}, state.creationError(readyCtx, true)
	}
	state.mu.Lock()
	if state.failed || state.cleaned {
		state.failed = true
		state.mu.Unlock()
		cleanupErr := state.cleanupAfterFailure()
		return execution.Execution{}, errors.Join(
			ErrExecutionCreation,
			ErrExecutionCreationOrder,
			cleanupErr,
		)
	}
	state.created = true
	state.mu.Unlock()
	return ready, nil
}

func (creator *ExecutionCreator) Cleanup(
	ctx context.Context,
	request CleanupRequest,
) error {
	if creator == nil || creator.state == nil || ctx == nil {
		return ErrExecutionCreationConfiguration
	}
	state := creator.state
	state.mu.Lock()
	if request.RunID != state.runID ||
		request.ResourceKind != "sandbox" ||
		request.ResourceName != state.resources.Sandbox ||
		!state.started ||
		!state.created ||
		state.cleaning {
		state.failed = true
		state.mu.Unlock()
		return ErrExecutionCreationCleanup
	}
	if state.cleaned {
		state.mu.Unlock()
		return nil
	}
	state.cleaning = true
	state.mu.Unlock()

	cleanupCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		runtimeExecutionCleanupTimeout,
	)
	defer cancel()
	err := state.cleanupPersisted(cleanupCtx)
	state.mu.Lock()
	state.cleaning = false
	if err != nil {
		state.failed = true
		state.mu.Unlock()
		return ErrExecutionCreationCleanup
	}
	state.cleaned = true
	state.mu.Unlock()
	return nil
}

func (state *executionCreationState) waitForReady(
	ctx context.Context,
	startedAt time.Time,
) (execution.Execution, error) {
	for {
		observation, err := state.provider.Observe(ctx, state.ref)
		finishedAt := time.Now().UTC()
		if err != nil ||
			state.invalidAfter(ctx) ||
			observation.IsolationDomainID != state.ref.IsolationDomainID ||
			observation.ExecutionID != state.ref.ID ||
			observation.ObservedAt.IsZero() ||
			observation.ObservedAt.Before(startedAt) ||
			observation.ObservedAt.After(finishedAt) {
			return execution.Execution{}, ErrExecutionCreation
		}
		switch observation.State {
		case "ready":
			record, err := state.store.GetExecution(ctx, state.ref)
			if err != nil || !validCreatedExecutionRecord(record, state, "ready") {
				return execution.Execution{}, ErrExecutionCreation
			}
			return record.Execution, nil
		case "provisioning", "pending":
			if err := state.poll(ctx); err != nil {
				return execution.Execution{}, ErrExecutionCreation
			}
		default:
			return execution.Execution{}, ErrExecutionCreation
		}
	}
}

func (state *executionCreationState) creationError(
	ctx context.Context,
	mayHaveCreated bool,
) error {
	var outcome error
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			outcome = errors.Join(outcome, err)
		}
	}
	if mayHaveCreated {
		outcome = errors.Join(outcome, state.cleanupAfterFailure())
	}
	return errors.Join(ErrExecutionCreation, outcome)
}

func (state *executionCreationState) cleanupAfterFailure() error {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		runtimeExecutionCleanupTimeout,
	)
	defer cancel()
	if err := state.cleanupPersisted(ctx); err != nil {
		return ErrExecutionCreationCleanup
	}
	return nil
}

func (state *executionCreationState) cleanupPersisted(ctx context.Context) error {
	record, err := state.store.GetExecution(ctx, state.ref)
	if errors.Is(err, execution.ErrExecutionMissing) {
		return nil
	}
	if err != nil ||
		!validCleanupExecutionState(record.Execution.State) ||
		!validCreatedExecutionRecord(record, state, record.Execution.State) {
		return ErrExecutionCreationCleanup
	}
	startedAt := time.Now().UTC()
	terminateErr := state.provider.Terminate(ctx, state.ref)
	if ctx.Err() != nil {
		return ErrExecutionCreationCleanup
	}
	observation, err := state.provider.Observe(ctx, state.ref)
	finishedAt := time.Now().UTC()
	if err != nil ||
		ctx.Err() != nil ||
		(terminateErr != nil && observation.State != "terminated") ||
		observation.IsolationDomainID != state.ref.IsolationDomainID ||
		observation.ExecutionID != state.ref.ID ||
		observation.State != "terminated" ||
		observation.ObservedAt.IsZero() ||
		observation.ObservedAt.Before(startedAt) ||
		observation.ObservedAt.After(finishedAt) {
		return ErrExecutionCreationCleanup
	}
	terminal, err := state.store.GetExecution(ctx, state.ref)
	if err != nil || !validCreatedExecutionRecord(terminal, state, "terminated") {
		return ErrExecutionCreationCleanup
	}
	return nil
}

func validCreatedGateway(
	gateway execution.Gateway,
	state *executionCreationState,
) bool {
	return gateway.IsolationDomainID == state.ref.IsolationDomainID &&
		gateway.ID == state.resources.Gateway &&
		gateway.Driver == driver &&
		gateway.State == execution.GatewayActive &&
		len(gateway.Capabilities) == 1 &&
		gateway.Capabilities[0] == openShellRuntimeCapability
}

func validCreatedPlacement(
	placement execution.Placement,
	state *executionCreationState,
) bool {
	return placement.IsolationDomainID == state.ref.IsolationDomainID &&
		placement.ID != "" &&
		placement.GatewayID == state.resources.Gateway
}

func validCreatedExecution(
	value execution.Execution,
	state *executionCreationState,
) bool {
	return value.IsolationDomainID == state.ref.IsolationDomainID &&
		value.ID == state.ref.ID &&
		value.GatewayID == state.resources.Gateway &&
		(value.State == "provisioning" || value.State == "pending" || value.State == "ready")
}

func validCreatedExecutionRecord(
	record execution.ExecutionRecord,
	state *executionCreationState,
	expectedState string,
) bool {
	return validCreatedExecutionIdentity(record.Execution, state) &&
		record.Execution.State == expectedState &&
		record.PlacementID != "" &&
		record.OperationID == runtimeOperationID(state.runID) &&
		record.SandboxName != ""
}

func validCreatedExecutionIdentity(
	value execution.Execution,
	state *executionCreationState,
) bool {
	return value.IsolationDomainID == state.ref.IsolationDomainID &&
		value.ID == state.ref.ID &&
		value.GatewayID == state.resources.Gateway
}

func validCleanupExecutionState(state string) bool {
	switch state {
	case "pending", "provisioning", "ready", "error", "deleting", "failed", "terminated", "unknown":
		return true
	default:
		return false
	}
}

func runtimeExecutionID(runID string) string {
	return identity.Derived(
		"exe",
		runtimeIsolationDomain(runID)+":"+runtimeOperationID(runID),
	)
}

func pollRuntimeExecution(ctx context.Context) error {
	timer := time.NewTimer(runtimeExecutionPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (state *executionCreationState) invalidAfter(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return true
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.failed
}

func (state *executionCreationState) fail() {
	state.mu.Lock()
	state.failed = true
	state.mu.Unlock()
}

func (ExecutionCreationConfig) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (ExecutionCreator) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var (
	_ json.Marshaler = ExecutionCreationConfig{}
	_ json.Marshaler = ExecutionCreator{}
)
