package runtimeevidence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

var (
	ErrScenarioConfiguration = errors.New("invalid runtime scenario configuration")
	ErrScenarioBinding       = errors.New("runtime scenario binding is invalid")
	ErrScenarioOrder         = errors.New("runtime scenario order is invalid")
	ErrScenarioProbe         = errors.New("runtime scenario probe failed")
	ErrScenarioAssertion     = errors.New("runtime scenario assertion is invalid")
	ErrScenarioReplay        = errors.New("runtime scenario observation was replayed")
)

type ProbeRequest struct {
	RunID     string
	Resources Resources
}

type ProbeResult struct {
	StartedAt         time.Time
	FinishedAt        time.Time
	ObservationSHA256 [sha256.Size]byte
	Assertions        Assertions
}

type Assertions struct {
	GatewayReady            bool
	SandboxReady            bool
	Initialized             bool
	TurnCompleted           bool
	DeterministicFailure    bool
	EventsNormalized        bool
	Interrupted             bool
	Cancelled               bool
	CommandApprovalHandled  bool
	FileApprovalHandled     bool
	ArtifactExported        bool
	SandboxRemoved          bool
	ExposureChecked         bool
	NativeProtocolExposed   bool
	UpstreamEndpointExposed bool
}

type ProviderProbes interface {
	GatewayReady(context.Context, ProbeRequest) (ProbeResult, error)
	SandboxReady(context.Context, ProbeRequest) (ProbeResult, error)
	ArtifactExport(context.Context, ProbeRequest) (ProbeResult, error)
	SandboxTeardown(context.Context, ProbeRequest) (ProbeResult, error)
}

type RuntimeProbes interface {
	Initialize(context.Context, ProbeRequest) (ProbeResult, error)
	TurnSuccess(context.Context, ProbeRequest) (ProbeResult, error)
	TurnFailure(context.Context, ProbeRequest) (ProbeResult, error)
	EventNormalization(context.Context, ProbeRequest) (ProbeResult, error)
	Interrupt(context.Context, ProbeRequest) (ProbeResult, error)
	Cancellation(context.Context, ProbeRequest) (ProbeResult, error)
	CommandApproval(context.Context, ProbeRequest) (ProbeResult, error)
	FileChangeApproval(context.Context, ProbeRequest) (ProbeResult, error)
}

type ScenarioConfig struct {
	RunID    string
	Provider ProviderProbes
	Runtime  RuntimeProbes
}

type ConcreteScenario struct {
	state *scenarioState
}

type scenarioState struct {
	mu       sync.Mutex
	request  ProbeRequest
	provider ProviderProbes
	runtime  RuntimeProbes
	next     int
	running  bool
	failed   bool
	proofs   map[[sha256.Size]byte]struct{}
}

func NewConcreteScenario(config ScenarioConfig) (*ConcreteScenario, error) {
	if !runIDPattern.MatchString(config.RunID) || config.Provider == nil || config.Runtime == nil {
		return nil, ErrScenarioConfiguration
	}
	return &ConcreteScenario{state: &scenarioState{
		request:  ProbeRequest{RunID: config.RunID, Resources: namesForRun(config.RunID)},
		provider: config.Provider,
		runtime:  config.Runtime,
		proofs:   make(map[[sha256.Size]byte]struct{}, len(requiredChecks)),
	}}, nil
}

func (scenario *ConcreteScenario) ValidateBinding(binding LiveBinding) error {
	if scenario == nil || scenario.state == nil {
		return ErrScenarioBinding
	}
	state := scenario.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.failed || state.running || binding.RunID != state.request.RunID ||
		binding.Resources != state.request.Resources {
		state.failed = true
		return ErrScenarioBinding
	}
	return nil
}

func (scenario *ConcreteScenario) GatewayReady(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckGatewayReady, func(ctx context.Context, state *scenarioState) (ProbeResult, error) {
		return state.provider.GatewayReady(ctx, state.request)
	})
}

func (scenario *ConcreteScenario) SandboxReady(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckSandboxReady, func(ctx context.Context, state *scenarioState) (ProbeResult, error) {
		return state.provider.SandboxReady(ctx, state.request)
	})
}

func (scenario *ConcreteScenario) Initialize(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckInitialize, func(ctx context.Context, state *scenarioState) (ProbeResult, error) {
		return state.runtime.Initialize(ctx, state.request)
	})
}

func (scenario *ConcreteScenario) TurnSuccess(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckTurnSuccess, func(ctx context.Context, state *scenarioState) (ProbeResult, error) {
		return state.runtime.TurnSuccess(ctx, state.request)
	})
}

func (scenario *ConcreteScenario) TurnFailure(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckTurnFailure, func(ctx context.Context, state *scenarioState) (ProbeResult, error) {
		return state.runtime.TurnFailure(ctx, state.request)
	})
}

func (scenario *ConcreteScenario) EventNormalization(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckEventNormalization, func(ctx context.Context, state *scenarioState) (ProbeResult, error) {
		return state.runtime.EventNormalization(ctx, state.request)
	})
}

func (scenario *ConcreteScenario) Interrupt(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckInterrupt, func(ctx context.Context, state *scenarioState) (ProbeResult, error) {
		return state.runtime.Interrupt(ctx, state.request)
	})
}

func (scenario *ConcreteScenario) Cancellation(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckCancellation, func(ctx context.Context, state *scenarioState) (ProbeResult, error) {
		return state.runtime.Cancellation(ctx, state.request)
	})
}

func (scenario *ConcreteScenario) CommandApproval(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckCommandApproval, func(ctx context.Context, state *scenarioState) (ProbeResult, error) {
		return state.runtime.CommandApproval(ctx, state.request)
	})
}

func (scenario *ConcreteScenario) FileChangeApproval(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckFileChangeApproval, func(ctx context.Context, state *scenarioState) (ProbeResult, error) {
		return state.runtime.FileChangeApproval(ctx, state.request)
	})
}

func (scenario *ConcreteScenario) ArtifactExport(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckArtifactExport, func(ctx context.Context, state *scenarioState) (ProbeResult, error) {
		return state.provider.ArtifactExport(ctx, state.request)
	})
}

func (scenario *ConcreteScenario) SandboxTeardown(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckSandboxTeardown, func(ctx context.Context, state *scenarioState) (ProbeResult, error) {
		return state.provider.SandboxTeardown(ctx, state.request)
	})
}

type probeCall func(context.Context, *scenarioState) (ProbeResult, error)

func (scenario *ConcreteScenario) run(
	ctx context.Context,
	binding LiveBinding,
	name CheckName,
	probe probeCall,
) (LiveReceipt, error) {
	if scenario == nil || scenario.state == nil || ctx == nil || probe == nil {
		return LiveReceipt{}, ErrScenarioConfiguration
	}
	state := scenario.state
	state.mu.Lock()
	if state.failed || state.running || state.next >= len(requiredChecks) ||
		name != requiredChecks[state.next] || binding.RunID != state.request.RunID ||
		binding.Resources != state.request.Resources {
		state.failed = true
		state.mu.Unlock()
		return LiveReceipt{}, ErrScenarioOrder
	}
	state.running = true
	state.next++
	state.mu.Unlock()

	if err := ctx.Err(); err != nil {
		state.fail()
		return LiveReceipt{}, errors.Join(ErrScenarioProbe, err)
	}
	result, err := probe(ctx, state)
	if err != nil {
		state.fail()
		if contextErr := ctx.Err(); contextErr != nil {
			return LiveReceipt{}, errors.Join(ErrScenarioProbe, contextErr)
		}
		return LiveReceipt{}, ErrScenarioProbe
	}
	if err := ctx.Err(); err != nil {
		state.fail()
		return LiveReceipt{}, errors.Join(ErrScenarioProbe, err)
	}
	if !validProbeResult(name, result) {
		state.fail()
		return LiveReceipt{}, ErrScenarioAssertion
	}

	state.mu.Lock()
	if state.failed || !state.running {
		state.failed = true
		state.running = false
		state.mu.Unlock()
		return LiveReceipt{}, ErrScenarioOrder
	}
	if _, exists := state.proofs[result.ObservationSHA256]; exists {
		state.failed = true
		state.running = false
		state.mu.Unlock()
		return LiveReceipt{}, ErrScenarioReplay
	}
	state.proofs[result.ObservationSHA256] = struct{}{}
	state.running = false
	state.mu.Unlock()

	return LiveReceipt{
		StartedAt:               result.StartedAt.UTC(),
		FinishedAt:              result.FinishedAt.UTC(),
		ObservationSHA256:       result.ObservationSHA256,
		ExposureChecked:         true,
		NativeProtocolExposed:   false,
		UpstreamEndpointExposed: false,
	}, nil
}

func validProbeResult(name CheckName, result ProbeResult) bool {
	empty := [sha256.Size]byte{}
	if result.StartedAt.IsZero() || result.FinishedAt.IsZero() ||
		!result.FinishedAt.After(result.StartedAt) ||
		result.ObservationSHA256 == empty ||
		!result.Assertions.ExposureChecked ||
		result.Assertions.NativeProtocolExposed ||
		result.Assertions.UpstreamEndpointExposed {
		return false
	}
	expected := Assertions{ExposureChecked: true}
	switch name {
	case CheckGatewayReady:
		expected.GatewayReady = true
	case CheckSandboxReady:
		expected.SandboxReady = true
	case CheckInitialize:
		expected.Initialized = true
	case CheckTurnSuccess:
		expected.TurnCompleted = true
	case CheckTurnFailure:
		expected.DeterministicFailure = true
	case CheckEventNormalization:
		expected.EventsNormalized = true
	case CheckInterrupt:
		expected.Interrupted = true
	case CheckCancellation:
		expected.Cancelled = true
	case CheckCommandApproval:
		expected.CommandApprovalHandled = true
	case CheckFileChangeApproval:
		expected.FileApprovalHandled = true
	case CheckArtifactExport:
		expected.ArtifactExported = true
	case CheckSandboxTeardown:
		expected.SandboxRemoved = true
	default:
		return false
	}
	return result.Assertions == expected
}

func (state *scenarioState) fail() {
	state.mu.Lock()
	state.failed = true
	state.running = false
	state.mu.Unlock()
}

func (ConcreteScenario) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var (
	_ LiveScenario   = (*ConcreteScenario)(nil)
	_ json.Marshaler = ConcreteScenario{}
)
