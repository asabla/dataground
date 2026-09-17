package runtimetest

import (
	"context"
	"errors"
	"strings"
	"testing"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

// This deterministic model exercises the suite without a native runtime. It is
// not the product reference engine and gives no evidence about enforcement.
func TestDeterministicAdapterContract(t *testing.T) {
	Run(t, func(t *testing.T, scenario Scenario) Fixture {
		adapter := &modelAdapter{scenario: scenario, events: make(chan dgruntime.Event, 16), done: make(chan struct{})}
		return Fixture{Adapter: adapter, Release: adapter.release, Verify: func(context.Context) error {
			if scenario == RejectApproval || scenario == RejectQuestion {
				if adapter.started {
					return errors.New("unsupported mode started the model")
				}
			} else if !adapter.started || !adapter.finished {
				return errors.New("model did not complete")
			}
			if scenario == Interrupt && adapter.interrupts != 1 {
				return errors.New("interrupt was not delivered exactly once")
			}
			return nil
		}}
	}, Features{})
}

type modelAdapter struct {
	scenario                            Scenario
	events                              chan dgruntime.Event
	done                                chan struct{}
	sequence                            uint64
	started, finished, closed, released bool
	result                              error
	interrupts                          int
}

func (m *modelAdapter) Start(_ context.Context, request dgruntime.StartRequest) (dgruntime.Turn, error) {
	if m.closed {
		return nil, dgruntime.ErrClosed
	}
	if request.ApprovalMode != dgruntime.ApprovalLocked {
		return nil, dgruntime.ErrApprovalMode
	}
	if request.QuestionMode != dgruntime.QuestionDisabled || request.QuestionTimeout != 0 {
		return nil, dgruntime.ErrQuestionMode
	}
	if request.SandboxMode != dgruntime.SandboxReadOnly {
		return nil, dgruntime.ErrSandboxMode
	}
	if request.Prompt == "" || request.WorkingDir != "/workspace" {
		return nil, dgruntime.ErrProtocol
	}
	if m.started {
		return nil, dgruntime.ErrConcurrentTurn
	}
	m.started = true
	m.emit("lifecycle.started", map[string]any{"message": "Runtime turn started."})
	return m, nil
}
func (m *modelAdapter) emit(kind string, payload map[string]any) {
	m.sequence++
	m.events <- dgruntime.Event{Sequence: m.sequence, Type: kind, Payload: payload}
}
func (m *modelAdapter) finish(result error) {
	if m.finished {
		return
	}
	m.result, m.finished = result, true
	close(m.events)
	close(m.done)
}
func (m *modelAdapter) release() {
	if m.released {
		return
	}
	m.released = true
	if !m.started || m.finished {
		return
	}
	switch m.scenario {
	case Success:
		m.emit("output.text.delta", map[string]any{"text": OutputText})
		for _, kind := range []string{"tool", "process", "file"} {
			value := map[string]string{"tool": "tool", "process": "command", "file": "change"}[kind]
			m.emit("activity."+kind+".started", map[string]any{"kind": value})
			m.emit("activity."+kind+".completed", map[string]any{"kind": value})
		}
		m.emit("lifecycle.succeeded", map[string]any{"message": "Runtime turn completed."})
		m.finish(nil)
	case UsageSnapshots:
		for _, counts := range [][3]int{{12, 8, 20}, {12, 8, 20}, {10, 6, 16}} {
			m.emit("usage.recorded", map[string]any{"inputTokens": counts[0], "outputTokens": counts[1], "totalTokens": counts[2]})
		}
		fallthrough
	case Ownership, Validation:
		m.emit("lifecycle.succeeded", map[string]any{"message": "Runtime turn completed."})
		m.finish(nil)
	case Failure:
		m.emit("lifecycle.failed", map[string]any{"code": "RUNTIME_TURN_FAILED", "retryable": false})
		m.finish(dgruntime.ErrTurnFailed)
	case ProtocolFailure, ScopeViolation, ProcessFailure:
		m.finish(dgruntime.ErrProtocol)
	case Interrupt:
		// Only an explicit interrupt can complete this scenario.
	}
}
func (m *modelAdapter) Events() <-chan dgruntime.Event { return m.events }
func (m *modelAdapter) ResolveApproval(context.Context, string, dgruntime.ApprovalDecision) error {
	return dgruntime.ErrApprovalNotFound
}
func (m *modelAdapter) Interrupt(context.Context) error {
	if !m.started || m.finished {
		return dgruntime.ErrClosed
	}
	m.interrupts++
	m.emit("lifecycle.cancelled", map[string]any{"reason": "runtime interruption"})
	m.finish(nil)
	return nil
}
func (m *modelAdapter) Wait(ctx context.Context) error {
	if m.finished {
		return m.result
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.done:
		return m.result
	}
}
func (m *modelAdapter) Close() error {
	m.closed = true
	m.finish(dgruntime.ErrClosed)
	return nil
}

func TestEventValidationRejectsContractDrift(t *testing.T) {
	valid := func() dgruntime.Event {
		return dgruntime.Event{Sequence: 1, Type: "output.text.delta", Payload: map[string]any{"text": OutputText}}
	}
	if err := validateEvent(valid(), 1, "output.text.delta"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*dgruntime.Event){
		"sequence gap":      func(e *dgruntime.Event) { e.Sequence = 2 },
		"native event type": func(e *dgruntime.Event) { e.Type = "item/agentMessage/delta" },
		"absent payload":    func(e *dgruntime.Event) { e.Payload = nil },
		"native routing":    func(e *dgruntime.Event) { e.Payload["nested"] = map[string]any{"thread": NativeCanary + "thread"} },
		"invalid JSON":      func(e *dgruntime.Event) { e.Payload["invalid"] = make(chan int) },
		"oversized payload": func(e *dgruntime.Event) { e.Payload["text"] = strings.Repeat("x", 128<<10) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			event := valid()
			change(&event)
			if validateEvent(event, 1, "output.text.delta") == nil {
				t.Fatal("invalid event was accepted")
			}
		})
	}
}
