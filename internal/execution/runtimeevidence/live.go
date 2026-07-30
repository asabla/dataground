package runtimeevidence

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"sync"
	"time"
)

const liveObservationCommitmentDomain = "dataground.openshell-runtime-live-observation/v1"

var (
	ErrLiveCaseConfiguration = errors.New("invalid live runtime case configuration")
	ErrLiveCaseBinding       = errors.New("live runtime case binding is invalid")
	ErrLiveCaseOrder         = errors.New("live runtime case order is invalid")
	ErrLiveCaseBackend       = errors.New("live runtime case backend failed")
	ErrLiveCaseReceipt       = errors.New("live runtime case receipt is invalid")
	ErrLiveCaseReplay        = errors.New("live runtime case receipt was replayed")
)

// LiveBinding contains only portable evidence identities. Native OpenShell and
// Codex identifiers remain private to the scenario implementation.
type LiveBinding struct {
	RunID     string
	Resources Resources
}

// LiveReceipt commits to one completed live observation without returning its
// transcript, native identifiers, endpoints, prompts, or provider responses.
type LiveReceipt struct {
	StartedAt               time.Time
	FinishedAt              time.Time
	ObservationSHA256       [sha256.Size]byte
	ExposureChecked         bool
	NativeProtocolExposed   bool
	UpstreamEndpointExposed bool
}

// LiveScenario owns the native operations for each required case. Separate
// methods prevent a launcher from routing a check name through a generic,
// caller-selected operation.
type LiveScenario interface {
	ValidateBinding(LiveBinding) error
	GatewayReady(context.Context, LiveBinding) (LiveReceipt, error)
	SandboxReady(context.Context, LiveBinding) (LiveReceipt, error)
	Initialize(context.Context, LiveBinding) (LiveReceipt, error)
	TurnSuccess(context.Context, LiveBinding) (LiveReceipt, error)
	TurnFailure(context.Context, LiveBinding) (LiveReceipt, error)
	EventNormalization(context.Context, LiveBinding) (LiveReceipt, error)
	Interrupt(context.Context, LiveBinding) (LiveReceipt, error)
	Cancellation(context.Context, LiveBinding) (LiveReceipt, error)
	CommandApproval(context.Context, LiveBinding) (LiveReceipt, error)
	FileChangeApproval(context.Context, LiveBinding) (LiveReceipt, error)
	ArtifactExport(context.Context, LiveBinding) (LiveReceipt, error)
	SandboxTeardown(context.Context, LiveBinding) (LiveReceipt, error)
}

// LiveCases is the repository-owned CaseRunner for live conformance. It owns
// binding, dispatch, order, replay prevention, receipt validation, exposure
// classification, and observation commitment construction.
type LiveCases struct {
	state *liveCaseState
}

type liveCaseState struct {
	mu       sync.Mutex
	binding  LiveBinding
	scenario LiveScenario
	next     int
	running  bool
	failed   bool
	sealed   bool
	proofs   map[[sha256.Size]byte]struct{}
}

func NewLiveCases(runID string, scenario LiveScenario) (*LiveCases, error) {
	if !runIDPattern.MatchString(runID) || scenario == nil {
		return nil, ErrLiveCaseConfiguration
	}
	binding := LiveBinding{RunID: runID, Resources: namesForRun(runID)}
	if err := scenario.ValidateBinding(binding); err != nil {
		return nil, ErrLiveCaseConfiguration
	}
	return &LiveCases{state: &liveCaseState{
		binding:  binding,
		scenario: scenario,
		proofs:   make(map[[sha256.Size]byte]struct{}, len(requiredChecks)),
	}}, nil
}

func (cases *LiveCases) ValidateBinding(runID string, resources Resources) error {
	if cases == nil || cases.state == nil {
		return ErrLiveCaseBinding
	}
	cases.state.mu.Lock()
	defer cases.state.mu.Unlock()
	if cases.state.failed ||
		cases.state.running ||
		cases.state.sealed ||
		runID != cases.state.binding.RunID ||
		resources != cases.state.binding.Resources {
		return ErrLiveCaseBinding
	}
	return nil
}

func (cases *LiveCases) Run(ctx context.Context, request CheckRequest) (Observation, error) {
	if cases == nil || cases.state == nil || ctx == nil {
		return Observation{}, ErrLiveCaseConfiguration
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, errors.Join(ErrLiveCaseBackend, err)
	}

	state := cases.state
	state.mu.Lock()
	if state.sealed {
		state.mu.Unlock()
		return Observation{}, ErrLiveCaseOrder
	}
	if state.failed ||
		state.running ||
		state.next >= len(requiredChecks) ||
		request.RunID != state.binding.RunID ||
		request.Resources != state.binding.Resources ||
		request.Name != requiredChecks[state.next] {
		state.failed = true
		state.mu.Unlock()
		return Observation{}, ErrLiveCaseOrder
	}
	state.running = true
	state.next++
	binding := state.binding
	scenario := state.scenario
	state.mu.Unlock()

	receipt, err := dispatchLiveCase(ctx, scenario, binding, request.Name)
	if err != nil {
		state.fail()
		if contextErr := ctx.Err(); contextErr != nil {
			return Observation{}, errors.Join(ErrLiveCaseBackend, contextErr)
		}
		return Observation{}, ErrLiveCaseBackend
	}
	if err := ctx.Err(); err != nil {
		state.fail()
		return Observation{}, errors.Join(ErrLiveCaseBackend, err)
	}
	if !validLiveReceipt(receipt) {
		state.fail()
		return Observation{}, ErrLiveCaseReceipt
	}

	state.mu.Lock()
	if state.failed || !state.running {
		state.failed = true
		state.running = false
		state.mu.Unlock()
		return Observation{}, ErrLiveCaseOrder
	}
	if _, exists := state.proofs[receipt.ObservationSHA256]; exists {
		state.failed = true
		state.running = false
		state.mu.Unlock()
		return Observation{}, ErrLiveCaseReplay
	}
	state.proofs[receipt.ObservationSHA256] = struct{}{}
	state.running = false
	state.mu.Unlock()

	return Observation{
		StartedAt:               receipt.StartedAt.UTC(),
		FinishedAt:              receipt.FinishedAt.UTC(),
		Commitment:              liveObservationCommitment(binding, request.Name, receipt),
		NativeProtocolExposed:   false,
		UpstreamEndpointExposed: false,
	}, nil
}

func (cases *LiveCases) FinalizeBinding(runID string, resources Resources) error {
	if cases == nil || cases.state == nil {
		return ErrLiveCaseBinding
	}
	state := cases.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.sealed {
		return ErrLiveCaseBinding
	}
	if state.failed ||
		state.running ||
		state.next != len(requiredChecks) ||
		runID != state.binding.RunID ||
		resources != state.binding.Resources {
		state.failed = true
		return ErrLiveCaseBinding
	}
	state.sealed = true
	return nil
}

func (LiveCases) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (state *liveCaseState) fail() {
	state.mu.Lock()
	state.failed = true
	state.running = false
	state.mu.Unlock()
}

func dispatchLiveCase(
	ctx context.Context,
	scenario LiveScenario,
	binding LiveBinding,
	name CheckName,
) (LiveReceipt, error) {
	switch name {
	case CheckGatewayReady:
		return scenario.GatewayReady(ctx, binding)
	case CheckSandboxReady:
		return scenario.SandboxReady(ctx, binding)
	case CheckInitialize:
		return scenario.Initialize(ctx, binding)
	case CheckTurnSuccess:
		return scenario.TurnSuccess(ctx, binding)
	case CheckTurnFailure:
		return scenario.TurnFailure(ctx, binding)
	case CheckEventNormalization:
		return scenario.EventNormalization(ctx, binding)
	case CheckInterrupt:
		return scenario.Interrupt(ctx, binding)
	case CheckCancellation:
		return scenario.Cancellation(ctx, binding)
	case CheckCommandApproval:
		return scenario.CommandApproval(ctx, binding)
	case CheckFileChangeApproval:
		return scenario.FileChangeApproval(ctx, binding)
	case CheckArtifactExport:
		return scenario.ArtifactExport(ctx, binding)
	case CheckSandboxTeardown:
		return scenario.SandboxTeardown(ctx, binding)
	default:
		return LiveReceipt{}, ErrLiveCaseOrder
	}
}

func validLiveReceipt(receipt LiveReceipt) bool {
	empty := [sha256.Size]byte{}
	return !receipt.StartedAt.IsZero() &&
		!receipt.FinishedAt.IsZero() &&
		receipt.FinishedAt.After(receipt.StartedAt) &&
		receipt.ObservationSHA256 != empty &&
		receipt.ExposureChecked &&
		!receipt.NativeProtocolExposed &&
		!receipt.UpstreamEndpointExposed
}

func liveObservationCommitment(
	binding LiveBinding,
	name CheckName,
	receipt LiveReceipt,
) string {
	digest := sha256.New()
	writeLiveCommitmentPart(digest, []byte(liveObservationCommitmentDomain))
	writeLiveCommitmentPart(digest, []byte(binding.RunID))
	writeLiveCommitmentPart(digest, []byte(name))
	writeLiveCommitmentPart(digest, []byte(binding.Resources.Gateway))
	writeLiveCommitmentPart(digest, []byte(binding.Resources.Sandbox))
	writeLiveCommitmentPart(digest, []byte(binding.Resources.Provider))
	writeLiveCommitmentPart(digest, []byte(binding.Resources.Runtime))
	writeLiveCommitmentPart(digest, []byte(binding.Resources.Workspace))
	writeLiveCommitmentPart(digest, []byte(receipt.StartedAt.UTC().Format(time.RFC3339Nano)))
	writeLiveCommitmentPart(digest, []byte(receipt.FinishedAt.UTC().Format(time.RFC3339Nano)))
	writeLiveCommitmentPart(digest, receipt.ObservationSHA256[:])
	writeLiveCommitmentPart(digest, []byte("exposure-checked"))
	writeLiveCommitmentPart(digest, []byte("native-protocol-not-exposed"))
	writeLiveCommitmentPart(digest, []byte("upstream-endpoint-not-exposed"))
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func writeLiveCommitmentPart(digest hash.Hash, value []byte) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write(value)
}

var (
	_ CaseRunner     = (*LiveCases)(nil)
	_ json.Marshaler = LiveCases{}
)
