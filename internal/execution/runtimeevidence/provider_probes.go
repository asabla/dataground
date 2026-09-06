package runtimeevidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
)

const (
	openShellProbeCommitmentDomain = "dataground.openshell-runtime-provider-probe/v1"
	openShellRuntimeCapability     = "codex.app-server/v1"
)

var (
	ErrOpenShellProbeConfiguration = errors.New("invalid OpenShell runtime probe configuration")
	ErrOpenShellProbeBinding       = errors.New("OpenShell runtime probe binding is invalid")
	ErrOpenShellProbeOrder         = errors.New("OpenShell runtime probe order is invalid")
	ErrOpenShellProbeObservation   = errors.New("OpenShell runtime probe observation failed")
)

type OpenShellProbeStore interface {
	GetGateway(context.Context, string, string) (execution.GatewayRecord, error)
	GetExecution(context.Context, execution.ExecutionRef) (execution.ExecutionRecord, error)
}

type OpenShellProbeProvider interface {
	Observe(context.Context, execution.ExecutionRef) (execution.Observation, error)
	Export(context.Context, execution.ExportRequest) (execution.ExportResult, error)
	Terminate(context.Context, execution.ExecutionRef) error
}

type OpenShellProbeConfig struct {
	diagnosticModel string
	Runtime         CodexProbeProvider
	RunID           string
	ExecutionID     string
	Store           OpenShellProbeStore
	Provider        OpenShellProbeProvider
	Now             func() time.Time
}

type OpenShellProbes struct {
	state *openShellProbeState
}

type openShellProbeState struct {
	runtime   *codexProbeState
	mu        sync.Mutex
	request   ProbeRequest
	store     OpenShellProbeStore
	provider  OpenShellProbeProvider
	execution execution.ExecutionRef
	now       func() time.Time
	next      int
	running   bool
	failed    bool
}

var openShellProbeOrder = [...]CheckName{
	CheckGatewayReady,
	CheckSandboxReady,
	CheckArtifactExport,
	CheckSandboxTeardown,
}

func NewOpenShellProbes(config OpenShellProbeConfig) (*OpenShellProbes, error) {
	if !runIDPattern.MatchString(config.RunID) ||
		config.ExecutionID == "" ||
		isNilHarnessPort(config.Store) ||
		isNilHarnessPort(config.Provider) || isNilHarnessPort(config.Runtime) ||
		(config.diagnosticModel != "" && !diagnosticModelPattern.MatchString(config.diagnosticModel)) {
		return nil, ErrOpenShellProbeConfiguration
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &OpenShellProbes{state: &openShellProbeState{
		runtime: &codexProbeState{
			diagnosticModel: config.diagnosticModel,
			store:           config.Store,
			provider:        config.Runtime,
			execution: execution.ExecutionRef{
				IsolationDomainID: runtimeIsolationDomain(config.RunID),
				ID:                config.ExecutionID,
			},
		},
		request: ProbeRequest{
			RunID:     config.RunID,
			Resources: namesForRun(config.RunID),
		},
		store:    config.Store,
		provider: config.Provider,
		execution: execution.ExecutionRef{
			IsolationDomainID: runtimeIsolationDomain(config.RunID),
			ID:                config.ExecutionID,
		},
		now: now,
	}}, nil
}

func (probes *OpenShellProbes) GatewayReady(
	ctx context.Context,
	request ProbeRequest,
) (ProbeResult, error) {
	state, startedAt, err := probes.begin(ctx, request, CheckGatewayReady)
	if err != nil {
		return ProbeResult{}, err
	}
	record, err := state.store.GetGateway(
		ctx,
		state.execution.IsolationDomainID,
		request.Resources.Gateway,
	)
	finishedAt := state.now().UTC()
	if err != nil ||
		record.Gateway.IsolationDomainID != state.execution.IsolationDomainID ||
		record.Gateway.ID != request.Resources.Gateway ||
		record.Gateway.Driver != driver ||
		record.Gateway.State != execution.GatewayActive ||
		len(record.Gateway.Capabilities) != 1 ||
		record.Gateway.Capabilities[0] != openShellRuntimeCapability ||
		record.Endpoint != gatewayEndpoint {
		state.fail()
		return ProbeResult{}, state.observationError(ctx)
	}
	return state.finish(
		request,
		CheckGatewayReady,
		startedAt,
		finishedAt,
		openShellProbeAssertion(CheckGatewayReady),
		[]byte(record.Gateway.IsolationDomainID),
		[]byte(record.Gateway.ID),
		[]byte(record.Gateway.Driver),
		[]byte(record.Gateway.State),
		[]byte(record.Gateway.Capabilities[0]),
		[]byte(record.Endpoint),
	)
}

func (probes *OpenShellProbes) SandboxReady(
	ctx context.Context,
	request ProbeRequest,
) (ProbeResult, error) {
	state, startedAt, err := probes.begin(ctx, request, CheckSandboxReady)
	if err != nil {
		return ProbeResult{}, err
	}
	record, err := state.store.GetExecution(ctx, state.execution)
	if err != nil || !state.validExecutionRecord(record, request, "ready") {
		state.fail()
		return ProbeResult{}, state.observationError(ctx)
	}
	observation, err := state.provider.Observe(ctx, state.execution)
	finishedAt := state.now().UTC()
	if err != nil ||
		observation.IsolationDomainID != state.execution.IsolationDomainID ||
		observation.ExecutionID != state.execution.ID ||
		observation.State != "ready" ||
		observation.ObservedAt.IsZero() ||
		observation.ObservedAt.Before(startedAt) ||
		observation.ObservedAt.After(finishedAt) {
		state.fail()
		return ProbeResult{}, state.observationError(ctx)
	}
	return state.finish(
		request,
		CheckSandboxReady,
		startedAt,
		finishedAt,
		openShellProbeAssertion(CheckSandboxReady),
		[]byte(record.Execution.ID),
		[]byte(record.PlacementID),
		[]byte(record.OperationID),
		[]byte(record.SandboxName),
		[]byte(observation.State),
		[]byte(observation.ObservedAt.UTC().Format(time.RFC3339Nano)),
	)
}

func (probes *OpenShellProbes) ArtifactExport(
	ctx context.Context,
	request ProbeRequest,
) (ProbeResult, error) {
	state, startedAt, err := probes.begin(ctx, request, CheckArtifactExport)
	if err != nil {
		return ProbeResult{}, err
	}
	record, err := state.store.GetExecution(ctx, state.execution)
	if err != nil || !state.validExecutionRecord(record, request, "ready") {
		state.fail()
		return ProbeResult{}, state.observationError(ctx)
	}
	productionSHA256, err := state.produceArtifact(ctx, request)
	if err != nil {
		state.fail()
		return ProbeResult{}, state.observationError(ctx)
	}
	result, err := state.provider.Export(ctx, execution.ExportRequest{
		IsolationDomainID: state.execution.IsolationDomainID,
		ExecutionID:       state.execution.ID,
		SandboxPath:       runtimeArtifactPath(request.RunID),
	})
	defer clear(result.Content)
	finishedAt := state.now().UTC()
	expected := runtimeArtifactContent(request.RunID)
	if err != nil ||
		result.IsolationDomainID != state.execution.IsolationDomainID ||
		result.ExecutionID != state.execution.ID ||
		!bytes.Equal(result.Content, expected) {
		state.fail()
		return ProbeResult{}, state.observationError(ctx)
	}
	contentSHA256 := sha256.Sum256(result.Content)
	return state.finish(
		request,
		CheckArtifactExport,
		startedAt,
		finishedAt,
		openShellProbeAssertion(CheckArtifactExport),
		[]byte(record.Execution.ID),
		[]byte(record.SandboxName),
		[]byte(runtimeArtifactPath(request.RunID)),
		productionSHA256[:],
		contentSHA256[:],
	)
}

func (probes *OpenShellProbes) SandboxTeardown(
	ctx context.Context,
	request ProbeRequest,
) (ProbeResult, error) {
	state, startedAt, err := probes.begin(ctx, request, CheckSandboxTeardown)
	if err != nil {
		return ProbeResult{}, err
	}
	record, err := state.store.GetExecution(ctx, state.execution)
	if err != nil || !state.validExecutionRecord(record, request, "ready") {
		state.fail()
		return ProbeResult{}, state.observationError(ctx)
	}
	if err := state.provider.Terminate(ctx, state.execution); err != nil {
		state.fail()
		return ProbeResult{}, state.observationError(ctx)
	}
	observation, err := state.provider.Observe(ctx, state.execution)
	terminal, terminalErr := state.store.GetExecution(ctx, state.execution)
	finishedAt := state.now().UTC()
	if err != nil ||
		terminalErr != nil ||
		observation.IsolationDomainID != state.execution.IsolationDomainID ||
		observation.ExecutionID != state.execution.ID ||
		observation.State != "terminated" ||
		observation.ObservedAt.IsZero() ||
		observation.ObservedAt.Before(startedAt) ||
		observation.ObservedAt.After(finishedAt) ||
		!state.validExecutionRecord(terminal, request, "terminated") {
		state.fail()
		return ProbeResult{}, state.observationError(ctx)
	}
	return state.finish(
		request,
		CheckSandboxTeardown,
		startedAt,
		finishedAt,
		openShellProbeAssertion(CheckSandboxTeardown),
		[]byte(record.Execution.ID),
		[]byte(record.SandboxName),
		[]byte(observation.State),
		[]byte(observation.ObservedAt.UTC().Format(time.RFC3339Nano)),
	)
}

func (probes *OpenShellProbes) begin(
	ctx context.Context,
	request ProbeRequest,
	name CheckName,
) (*openShellProbeState, time.Time, error) {
	if probes == nil || probes.state == nil || ctx == nil {
		return nil, time.Time{}, ErrOpenShellProbeConfiguration
	}
	state := probes.state
	state.mu.Lock()
	if state.failed ||
		state.running ||
		state.next >= len(openShellProbeOrder) ||
		name != openShellProbeOrder[state.next] ||
		request != state.request {
		state.failed = true
		state.mu.Unlock()
		return nil, time.Time{}, ErrOpenShellProbeOrder
	}
	state.running = true
	state.next++
	state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		state.fail()
		return nil, time.Time{}, errors.Join(ErrOpenShellProbeObservation, err)
	}
	return state, state.now().UTC(), nil
}

func (state *openShellProbeState) finish(
	request ProbeRequest,
	name CheckName,
	startedAt time.Time,
	finishedAt time.Time,
	assertions Assertions,
	parts ...[]byte,
) (ProbeResult, error) {
	if startedAt.IsZero() || !finishedAt.After(startedAt) {
		state.fail()
		return ProbeResult{}, ErrOpenShellProbeObservation
	}
	state.mu.Lock()
	if state.failed || !state.running {
		state.failed = true
		state.running = false
		state.mu.Unlock()
		return ProbeResult{}, ErrOpenShellProbeOrder
	}
	state.running = false
	state.mu.Unlock()
	return ProbeResult{
		StartedAt:         startedAt,
		FinishedAt:        finishedAt,
		ObservationSHA256: openShellProbeCommitment(request, name, startedAt, finishedAt, parts...),
		Assertions:        assertions,
	}, nil
}

func (state *openShellProbeState) validExecutionRecord(
	record execution.ExecutionRecord,
	request ProbeRequest,
	expectedState string,
) bool {
	return record.Execution.IsolationDomainID == state.execution.IsolationDomainID &&
		record.Execution.ID == state.execution.ID &&
		record.Execution.GatewayID == request.Resources.Gateway &&
		record.Execution.State == expectedState &&
		record.PlacementID != "" &&
		record.OperationID == runtimeOperationID(request.RunID) &&
		record.SandboxName != ""
}

func (state *openShellProbeState) fail() {
	state.mu.Lock()
	state.failed = true
	state.running = false
	state.mu.Unlock()
}

func (state *openShellProbeState) observationError(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return errors.Join(ErrOpenShellProbeObservation, err)
		}
	}
	return ErrOpenShellProbeObservation
}

func runtimeIsolationDomain(runID string) string {
	return identity.Derived("iso", "runtime-conformance:"+runID)
}

func runtimeOperationID(runID string) string {
	return identity.Derived("op", "runtime-conformance:"+runID)
}

func runtimeArtifactPath(runID string) string {
	return "/sandbox/dataground-runtime-conformance-" + runID + ".json"
}

func runtimeArtifactContent(runID string) []byte {
	return []byte(fmt.Sprintf("{\"runId\":%q,\"status\":\"passed\"}\n", runID))
}

func openShellProbeAssertion(name CheckName) Assertions {
	assertions := Assertions{ExposureChecked: true}
	switch name {
	case CheckGatewayReady:
		assertions.GatewayReady = true
	case CheckSandboxReady:
		assertions.SandboxReady = true
	case CheckArtifactExport:
		assertions.ArtifactExported = true
	case CheckSandboxTeardown:
		assertions.SandboxRemoved = true
	}
	return assertions
}

func openShellProbeCommitment(
	request ProbeRequest,
	name CheckName,
	startedAt time.Time,
	finishedAt time.Time,
	parts ...[]byte,
) [sha256.Size]byte {
	digest := sha256.New()
	writeLiveCommitmentPart(digest, []byte(openShellProbeCommitmentDomain))
	writeLiveCommitmentPart(digest, []byte(request.RunID))
	writeLiveCommitmentPart(digest, []byte(name))
	writeLiveCommitmentPart(digest, []byte(request.Resources.Gateway))
	writeLiveCommitmentPart(digest, []byte(request.Resources.Sandbox))
	writeLiveCommitmentPart(digest, []byte(request.Resources.Provider))
	writeLiveCommitmentPart(digest, []byte(request.Resources.Runtime))
	writeLiveCommitmentPart(digest, []byte(request.Resources.Workspace))
	writeLiveCommitmentPart(digest, []byte(startedAt.UTC().Format(time.RFC3339Nano)))
	writeLiveCommitmentPart(digest, []byte(finishedAt.UTC().Format(time.RFC3339Nano)))
	for _, part := range parts {
		writeLiveCommitmentPart(digest, part)
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func (OpenShellProbeConfig) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (OpenShellProbes) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var (
	_ ProviderProbes = (*OpenShellProbes)(nil)
	_ json.Marshaler = OpenShellProbeConfig{}
	_ json.Marshaler = OpenShellProbes{}
)
