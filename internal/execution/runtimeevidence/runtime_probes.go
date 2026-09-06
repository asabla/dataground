package runtimeevidence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/execution"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
)

const codexProbeCommitmentDomain = "dataground.openshell-runtime-codex-probe/v1"

const (
	maxCodexProbeEvents     = 256
	maxCodexProbeEventBytes = 4 << 20
)

var (
	ErrCodexProbeConfiguration = errors.New("invalid Codex runtime probe configuration")
	ErrCodexProbeOrder         = errors.New("Codex runtime probe order is invalid")
	ErrCodexProbeObservation   = errors.New("Codex runtime probe observation failed")
)

type CodexProbeStore interface {
	GetExecution(context.Context, execution.ExecutionRef) (execution.ExecutionRecord, error)
}

type CodexProbeProvider interface {
	StartRuntime(context.Context, execution.ExecutionRef) (execution.RuntimeSession, error)
}

type CodexProbeConfig struct {
	diagnosticModel string
	RunID           string
	ExecutionID     string
	Store           CodexProbeStore
	Provider        CodexProbeProvider
	Now             func() time.Time
}

type CodexProbes struct {
	state *codexProbeState
}

type codexProbeState struct {
	diagnosticModel string
	mu              sync.Mutex
	request         ProbeRequest
	store           CodexProbeStore
	provider        CodexProbeProvider
	execution       execution.ExecutionRef
	now             func() time.Time
	next            int
	running         bool
	failed          bool
}

type codexProbeObservation struct {
	events  []dgruntime.Event
	outcome string
}

type trackedCodexProbeSession struct {
	execution.RuntimeSession
	mu       sync.Mutex
	closed   bool
	closeErr error
}

var codexProbeOrder = [...]CheckName{
	CheckInitialize,
	CheckTurnSuccess,
	CheckTurnFailure,
	CheckEventNormalization,
	CheckInterrupt,
	CheckCancellation,
	CheckCommandApproval,
	CheckFileChangeApproval,
}

func NewCodexProbes(config CodexProbeConfig) (*CodexProbes, error) {
	if !runIDPattern.MatchString(config.RunID) ||
		config.ExecutionID == "" ||
		config.Store == nil ||
		config.Provider == nil ||
		(config.diagnosticModel != "" && !diagnosticModelPattern.MatchString(config.diagnosticModel)) {
		return nil, ErrCodexProbeConfiguration
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &CodexProbes{state: &codexProbeState{
		diagnosticModel: config.diagnosticModel,
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

func (probes *CodexProbes) Initialize(
	ctx context.Context,
	request ProbeRequest,
) (ProbeResult, error) {
	return probes.run(ctx, request, CheckInitialize, func(
		ctx context.Context,
		state *codexProbeState,
	) (codexProbeObservation, error) {
		turn, closeTurn, err := state.start(ctx, request, lockedRequest(
			"Initialize the runtime and wait for interruption.",
		))
		if err != nil {
			return codexProbeObservation{}, err
		}
		defer func() { _ = closeTurn() }()
		interrupted := false
		events, err := observeTurn(ctx, turn, func(event dgruntime.Event) (bool, error) {
			if event.Type != "lifecycle.started" || interrupted {
				return false, nil
			}
			interrupted = true
			if err := turn.Interrupt(ctx); err != nil {
				return false, err
			}
			return false, nil
		}, "lifecycle.cancelled")
		if err != nil || !interrupted || turn.Wait(ctx) != nil || closeTurn() != nil {
			return codexProbeObservation{}, ErrCodexProbeObservation
		}
		return codexProbeObservation{events: events, outcome: "initialized-and-interrupted"}, nil
	})
}

func (probes *CodexProbes) TurnSuccess(
	ctx context.Context,
	request ProbeRequest,
) (ProbeResult, error) {
	return probes.run(ctx, request, CheckTurnSuccess, func(
		ctx context.Context,
		state *codexProbeState,
	) (codexProbeObservation, error) {
		marker := "dg-runtime-success-" + request.RunID
		turn, closeTurn, err := state.start(ctx, request, lockedRequest(
			"Reply with exactly "+marker+" and no other text.",
		))
		if err != nil {
			return codexProbeObservation{}, err
		}
		defer func() { _ = closeTurn() }()
		events, err := observeTurn(ctx, turn, nil, "lifecycle.succeeded")
		if err != nil || turn.Wait(ctx) != nil || textOutput(events) != marker || closeTurn() != nil {
			return codexProbeObservation{}, ErrCodexProbeObservation
		}
		return codexProbeObservation{events: events, outcome: "completed"}, nil
	})
}

func (probes *CodexProbes) TurnFailure(
	ctx context.Context,
	request ProbeRequest,
) (ProbeResult, error) {
	return probes.run(ctx, request, CheckTurnFailure, func(
		ctx context.Context,
		state *codexProbeState,
	) (codexProbeObservation, error) {
		// A prompt asking the model to fail can itself complete successfully.
		// Require an actual rejection of a run-derived unavailable model; a
		// successful response or rejection before turn startup cannot certify it.
		startRequest := lockedRequest("Exercise the controlled unavailable-model failure case.")
		startRequest.Model = runtimeFailureModel(request.RunID)
		turn, closeTurn, err := state.start(ctx, request, startRequest)
		if err != nil {
			return codexProbeObservation{}, err
		}
		defer func() { _ = closeTurn() }()
		events, err := observeTurn(ctx, turn, nil, "lifecycle.failed")
		if err != nil || !errors.Is(turn.Wait(ctx), dgruntime.ErrTurnFailed) || closeTurn() != nil {
			return codexProbeObservation{}, ErrCodexProbeObservation
		}
		return codexProbeObservation{events: events, outcome: "failed"}, nil
	})
}

func (probes *CodexProbes) EventNormalization(
	ctx context.Context,
	request ProbeRequest,
) (ProbeResult, error) {
	return probes.run(ctx, request, CheckEventNormalization, func(
		ctx context.Context,
		state *codexProbeState,
	) (codexProbeObservation, error) {
		marker := "dg-runtime-events-" + request.RunID
		turn, closeTurn, err := state.start(ctx, request, lockedRequest(
			fmt.Sprintf("Run printf %q, then reply with exactly %s.", marker, marker),
		))
		if err != nil {
			return codexProbeObservation{}, state.normalizationFailure("turn-start")
		}
		defer func() { _ = closeTurn() }()
		events, err := observeTurn(ctx, turn, nil, "lifecycle.succeeded")
		if err != nil {
			return codexProbeObservation{}, state.normalizationFailure("event-stream")
		}
		if turn.Wait(ctx) != nil {
			return codexProbeObservation{}, state.normalizationFailure("turn-completion")
		}
		if !hasEvent(events, "activity.process.started", "command") {
			return codexProbeObservation{}, state.normalizationFailure("command-start")
		}
		if !hasEvent(events, "activity.process.completed", "command") {
			return codexProbeObservation{}, state.normalizationFailure("command-completion")
		}
		if !strings.Contains(textOutput(events), marker) {
			return codexProbeObservation{}, state.normalizationFailure("marker")
		}
		if closeTurn() != nil {
			return codexProbeObservation{}, state.normalizationFailure("transport-close")
		}
		return codexProbeObservation{events: events, outcome: "normalized"}, nil
	})
}

func (probes *CodexProbes) Interrupt(
	ctx context.Context,
	request ProbeRequest,
) (ProbeResult, error) {
	return probes.run(ctx, request, CheckInterrupt, func(
		ctx context.Context,
		state *codexProbeState,
	) (codexProbeObservation, error) {
		turn, closeTurn, err := state.start(ctx, request, lockedRequest(
			"Run sleep 300 and wait for it to finish.",
		))
		if err != nil {
			return codexProbeObservation{}, err
		}
		defer func() { _ = closeTurn() }()
		interrupted := false
		events, err := observeTurn(ctx, turn, func(event dgruntime.Event) (bool, error) {
			if event.Type != "lifecycle.started" || interrupted {
				return false, nil
			}
			interrupted = true
			return false, turn.Interrupt(ctx)
		}, "lifecycle.cancelled")
		if err != nil || !interrupted || turn.Wait(ctx) != nil || closeTurn() != nil {
			return codexProbeObservation{}, ErrCodexProbeObservation
		}
		return codexProbeObservation{events: events, outcome: "interrupted"}, nil
	})
}

func (probes *CodexProbes) Cancellation(
	ctx context.Context,
	request ProbeRequest,
) (ProbeResult, error) {
	return probes.run(ctx, request, CheckCancellation, func(
		ctx context.Context,
		state *codexProbeState,
	) (codexProbeObservation, error) {
		turn, closeTurn, err := state.start(ctx, request, lockedRequest(
			"Run sleep 300 and wait for it to finish.",
		))
		if err != nil {
			return codexProbeObservation{}, err
		}
		defer func() { _ = closeTurn() }()
		events, err := observeTurn(ctx, turn, nil, "lifecycle.started")
		if err != nil {
			return codexProbeObservation{}, err
		}
		cancelledCtx, cancel := context.WithCancel(ctx)
		cancel()
		if !errors.Is(turn.Wait(cancelledCtx), context.Canceled) || closeTurn() != nil {
			return codexProbeObservation{}, ErrCodexProbeObservation
		}
		return codexProbeObservation{events: events, outcome: "wait-cancelled"}, nil
	})
}

func (probes *CodexProbes) CommandApproval(
	ctx context.Context,
	request ProbeRequest,
) (ProbeResult, error) {
	return probes.approval(ctx, request, CheckCommandApproval, "process.execute", dgruntime.StartRequest{
		Prompt: "Use exec_command to run printf 'dg-command-" + request.RunID +
			"' with sandbox_permissions set to require_escalated and justification set to 'Exercise the controlled command approval denial'. Request approval before executing; do not run this command with default permissions.",
		ApprovalMode: dgruntime.ApprovalInteractive,
		SandboxMode:  dgruntime.SandboxReadOnly,
	})
}

func (probes *CodexProbes) FileChangeApproval(
	ctx context.Context,
	request ProbeRequest,
) (ProbeResult, error) {
	return probes.approval(ctx, request, CheckFileChangeApproval, "workspace.change", dgruntime.StartRequest{
		// The destination must be outside the native writable workspace, otherwise
		// apply_patch can legitimately finish without an approval request.
		Prompt: "Apply exactly this patch using apply_patch. If a dedicated apply_patch tool is unavailable, invoke apply_patch through the command tool. Let apply_patch request approval for the destination outside the current workspace. Do not use another file-writing command or change the destination.\n*** Begin Patch\n*** Add File: /sandbox/dg-file-" + request.RunID +
			".txt\n+dg-file-" + request.RunID + "\n*** End Patch",
		WorkingDir:   "/tmp",
		ApprovalMode: dgruntime.ApprovalInteractive,
		SandboxMode:  dgruntime.SandboxWorkspaceWrite,
	})
}

func (probes *CodexProbes) approval(
	ctx context.Context,
	request ProbeRequest,
	name CheckName,
	action string,
	startRequest dgruntime.StartRequest,
) (ProbeResult, error) {
	return probes.run(ctx, request, name, func(
		ctx context.Context,
		state *codexProbeState,
	) (codexProbeObservation, error) {
		turn, closeTurn, err := state.start(ctx, request, startRequest)
		if err != nil {
			return codexProbeObservation{}, err
		}
		defer func() { _ = closeTurn() }()
		resolved := false
		interrupted := false
		events, err := observeTurn(ctx, turn, func(event dgruntime.Event) (bool, error) {
			if event.Type != "interaction.approval.requested" || resolved {
				return false, nil
			}
			if event.Payload["action"] != action {
				return false, ErrCodexProbeObservation
			}
			approvalID, ok := event.Payload["approvalId"].(string)
			if !ok || approvalID == "" {
				return false, ErrCodexProbeObservation
			}
			if err := turn.ResolveApproval(ctx, approvalID, dgruntime.ApprovalDeny); err != nil {
				return false, err
			}
			resolved = true
			if err := turn.Interrupt(ctx); err != nil {
				return false, err
			}
			interrupted = true
			return false, nil
		}, "lifecycle.cancelled")
		if err != nil || !resolved || !interrupted || turn.Wait(ctx) != nil || closeTurn() != nil {
			return codexProbeObservation{}, ErrCodexProbeObservation
		}
		return codexProbeObservation{events: events, outcome: "approval-denied"}, nil
	})
}

type codexProbeCall func(context.Context, *codexProbeState) (codexProbeObservation, error)

func (state *codexProbeState) normalizationFailure(stage string) error {
	switch stage {
	case "turn-start", "event-stream", "turn-completion", "command-start", "command-completion", "marker", "transport-close":
	default:
		return ErrCodexProbeObservation
	}
	if state.diagnosticModel != "" {
		return &LocalDiagnosticError{stage: "case-event-normalization-" + stage}
	}
	return ErrCodexProbeObservation
}

func (probes *CodexProbes) run(
	ctx context.Context,
	request ProbeRequest,
	name CheckName,
	probe codexProbeCall,
) (ProbeResult, error) {
	state, startedAt, err := probes.begin(ctx, request, name)
	if err != nil {
		return ProbeResult{}, err
	}
	observation, err := probe(ctx, state)
	finishedAt := state.now().UTC()
	if err != nil || ctx.Err() != nil || !validNormalizedEvents(observation.events) ||
		startedAt.IsZero() || !finishedAt.After(startedAt) {
		state.fail()
		var diagnosticFailure *LocalDiagnosticError
		if state.diagnosticModel != "" && errors.As(err, &diagnosticFailure) {
			return ProbeResult{}, diagnosticFailure
		}
		return ProbeResult{}, state.observationError(ctx)
	}
	encodedEvents, err := json.Marshal(observation.events)
	if err != nil || len(encodedEvents) > maxCodexProbeEventBytes {
		state.fail()
		return ProbeResult{}, ErrCodexProbeObservation
	}
	observationSHA256 := codexProbeCommitment(
		request,
		name,
		startedAt,
		finishedAt,
		encodedEvents,
		observation.outcome,
	)
	clear(encodedEvents)
	state.mu.Lock()
	if state.failed || !state.running {
		state.failed = true
		state.running = false
		state.mu.Unlock()
		return ProbeResult{}, ErrCodexProbeOrder
	}
	state.running = false
	state.mu.Unlock()
	return ProbeResult{
		StartedAt:         startedAt,
		FinishedAt:        finishedAt,
		ObservationSHA256: observationSHA256,
		Assertions:        codexProbeAssertion(name),
	}, nil
}

func (probes *CodexProbes) begin(
	ctx context.Context,
	request ProbeRequest,
	name CheckName,
) (*codexProbeState, time.Time, error) {
	if probes == nil || probes.state == nil || ctx == nil {
		return nil, time.Time{}, ErrCodexProbeConfiguration
	}
	state := probes.state
	state.mu.Lock()
	if state.failed ||
		state.running ||
		state.next >= len(codexProbeOrder) ||
		name != codexProbeOrder[state.next] ||
		request != state.request {
		state.failed = true
		state.mu.Unlock()
		return nil, time.Time{}, ErrCodexProbeOrder
	}
	state.running = true
	state.next++
	state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		state.fail()
		return nil, time.Time{}, errors.Join(ErrCodexProbeObservation, err)
	}
	return state, state.now().UTC(), nil
}

func (state *codexProbeState) start(
	ctx context.Context,
	request ProbeRequest,
	startRequest dgruntime.StartRequest,
) (dgruntime.Turn, func() error, error) {
	record, err := state.store.GetExecution(ctx, state.execution)
	if err != nil ||
		record.Execution.IsolationDomainID != state.execution.IsolationDomainID ||
		record.Execution.ID != state.execution.ID ||
		record.Execution.GatewayID != request.Resources.Gateway ||
		record.Execution.State != "ready" ||
		record.PlacementID == "" ||
		record.OperationID != runtimeOperationID(request.RunID) ||
		record.SandboxName == "" {
		return nil, func() error { return nil }, ErrCodexProbeObservation
	}
	session, err := state.provider.StartRuntime(ctx, state.execution)
	if err != nil || session == nil {
		if session != nil {
			_ = session.Close()
		}
		return nil, func() error { return nil }, ErrCodexProbeObservation
	}
	trackedSession := &trackedCodexProbeSession{RuntimeSession: session}
	var client *codex.Client
	if state.diagnosticModel == "" {
		client, err = codex.New(trackedSession)
	} else {
		client, err = codex.NewOpenShellConformance(trackedSession)
	}
	if err != nil {
		_ = trackedSession.Close()
		return nil, func() error { return nil }, ErrCodexProbeObservation
	}
	if state.diagnosticModel != "" && startRequest.Model == "" {
		startRequest.Model = state.diagnosticModel
	}
	turn, err := client.Start(ctx, startRequest)
	if err != nil {
		_ = client.Close()
		return nil, func() error { return nil }, ErrCodexProbeObservation
	}
	return turn, func() error {
		if err := client.Close(); err != nil {
			return err
		}
		return trackedSession.result()
	}, nil
}

func (session *trackedCodexProbeSession) Close() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return session.closeErr
	}
	session.closed = true
	session.closeErr = session.RuntimeSession.Close()
	return session.closeErr
}

func (session *trackedCodexProbeSession) result() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if !session.closed || session.closeErr != nil {
		return ErrCodexProbeObservation
	}
	return nil
}

func (state *codexProbeState) fail() {
	state.mu.Lock()
	state.failed = true
	state.running = false
	state.mu.Unlock()
}

func (state *codexProbeState) observationError(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return errors.Join(ErrCodexProbeObservation, err)
		}
	}
	return ErrCodexProbeObservation
}

func lockedRequest(prompt string) dgruntime.StartRequest {
	return dgruntime.StartRequest{
		Prompt:       prompt,
		ApprovalMode: dgruntime.ApprovalLocked,
		SandboxMode:  dgruntime.SandboxReadOnly,
	}
}

func runtimeFailureModel(runID string) string {
	return "dataground-runtime-unavailable-" + runID
}

func observeTurn(
	ctx context.Context,
	turn dgruntime.Turn,
	onEvent func(dgruntime.Event) (bool, error),
	stopType string,
) ([]dgruntime.Event, error) {
	events := make([]dgruntime.Event, 0, 16)
	for {
		select {
		case event, ok := <-turn.Events():
			if !ok || len(events) >= maxCodexProbeEvents {
				return nil, ErrCodexProbeObservation
			}
			events = append(events, event)
			switch event.Type {
			case "lifecycle.succeeded", "lifecycle.failed", "lifecycle.cancelled":
				if event.Type != stopType {
					return nil, ErrCodexProbeObservation
				}
			}
			if onEvent != nil {
				stop, err := onEvent(event)
				if err != nil {
					return nil, err
				}
				if stop {
					return events, nil
				}
			}
			if event.Type == stopType {
				return events, nil
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func validNormalizedEvents(events []dgruntime.Event) bool {
	if len(events) == 0 {
		return false
	}
	for index, event := range events {
		if event.Sequence != uint64(index+1) || event.Type == "" || event.Payload == nil ||
			valueExposesNativeProtocol(event.Payload) {
			return false
		}
	}
	return true
}

func valueExposesNativeProtocol(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
			switch normalized {
			case "threadid", "turnid", "gatewayendpoint", "runtimeendpoint", "sandboxname", "nativeid":
				return true
			}
			if valueExposesNativeProtocol(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if valueExposesNativeProtocol(child) {
				return true
			}
		}
	case string:
		lower := strings.ToLower(typed)
		return strings.Contains(lower, "http://127.0.0.1:8080") ||
			strings.Contains(lower, "https://127.0.0.1:8080") ||
			strings.Contains(lower, "ws://") ||
			strings.Contains(lower, "wss://")
	}
	return false
}

func textOutput(events []dgruntime.Event) string {
	var result strings.Builder
	for _, event := range events {
		if event.Type != "output.text.delta" {
			continue
		}
		text, ok := event.Payload["text"].(string)
		if !ok {
			return ""
		}
		result.WriteString(text)
	}
	return result.String()
}

func hasEvent(events []dgruntime.Event, eventType string, kind string) bool {
	for _, event := range events {
		if event.Type == eventType && event.Payload["kind"] == kind {
			return true
		}
	}
	return false
}

func codexProbeAssertion(name CheckName) Assertions {
	assertions := Assertions{ExposureChecked: true}
	switch name {
	case CheckInitialize:
		assertions.Initialized = true
	case CheckTurnSuccess:
		assertions.TurnCompleted = true
	case CheckTurnFailure:
		assertions.DeterministicFailure = true
	case CheckEventNormalization:
		assertions.EventsNormalized = true
	case CheckInterrupt:
		assertions.Interrupted = true
	case CheckCancellation:
		assertions.Cancelled = true
	case CheckCommandApproval:
		assertions.CommandApprovalHandled = true
	case CheckFileChangeApproval:
		assertions.FileApprovalHandled = true
	}
	return assertions
}

func codexProbeCommitment(
	request ProbeRequest,
	name CheckName,
	startedAt time.Time,
	finishedAt time.Time,
	events []byte,
	outcome string,
) [sha256.Size]byte {
	digest := sha256.New()
	writeLiveCommitmentPart(digest, []byte(codexProbeCommitmentDomain))
	writeLiveCommitmentPart(digest, []byte(request.RunID))
	writeLiveCommitmentPart(digest, []byte(name))
	writeLiveCommitmentPart(digest, []byte(request.Resources.Gateway))
	writeLiveCommitmentPart(digest, []byte(request.Resources.Sandbox))
	writeLiveCommitmentPart(digest, []byte(request.Resources.Provider))
	writeLiveCommitmentPart(digest, []byte(request.Resources.Runtime))
	writeLiveCommitmentPart(digest, []byte(request.Resources.Workspace))
	writeLiveCommitmentPart(digest, []byte(startedAt.UTC().Format(time.RFC3339Nano)))
	writeLiveCommitmentPart(digest, []byte(finishedAt.UTC().Format(time.RFC3339Nano)))
	writeLiveCommitmentPart(digest, events)
	writeLiveCommitmentPart(digest, []byte(outcome))
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func (CodexProbeConfig) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (CodexProbes) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

var (
	_ RuntimeProbes  = (*CodexProbes)(nil)
	_ json.Marshaler = CodexProbeConfig{}
	_ json.Marshaler = CodexProbes{}
)
