package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestCompletedRuntimeMessageJournalRejectsMalformedOrUnfencedEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	event := persistence.InvocationRuntimeEvent{SourceSequence: 1, Type: dgruntime.MessageCompletedEvent, Payload: dgruntime.CompletedMessage{Text: "answer", Phase: "final"}.Payload()}
	first, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, event)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, event)
	if err != nil || replay.ID != first.ID || replay.Sequence != first.Sequence {
		t.Fatal("exact replay changed message identity", err)
	}
	event.Payload = dgruntime.CompletedMessage{Text: "different", Phase: "final"}.Payload()
	if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, event); !errors.Is(err, persistence.ErrInvocationRuntimeEventConflict) {
		t.Fatal("changed replay accepted", err)
	}
	event.SourceSequence = 2
	for _, payload := range []map[string]any{
		{"text": "answer"},
		{"text": "answer", "phase": "final_answer"},
		{"text": "answer", "phase": "final", "nativeId": "private"},
		{"text": strings.Repeat("x", dgruntime.MaximumMessageTextBytes+1), "phase": "final"},
	} {
		event.Payload = payload
		if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, event); !errors.Is(err, persistence.ErrInvocationRuntimeEventInvalid) {
			t.Fatal("malformed message was persisted", err)
		}
	}
	event.Payload = dgruntime.CompletedMessage{Text: "answer", Phase: "final"}.Payload()
	stale := fixture.claim
	stale.FencingToken++
	if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, stale, event); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatal("stale claim recorded a message", err)
	}
	foreign := fixture.claim
	foreign.IsolationDomainID = "iso_00000000000000000001"
	if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, foreign, event); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatal("foreign claim recorded a message", err)
	}
	events, err := fixture.repository.ListEvents(ctx, fixture.target.IsolationDomainID, fixture.target.InvocationID, first.Sequence)
	if err != nil || len(events) != 0 {
		t.Fatal("rejected messages changed the journal", events, err)
	}
}

func TestCompletedRuntimeOutputSurvivesWorkerReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := map[string]any{"type": "object"}
	fixture := newRuntimeQuestionFixtureWithSchema(t, ctx, false, schema)
	transport := &completedOutputTransport{interruptionTransport: interruptionTransport{questionBridgeTransport: questionBridgeTransport{scope: fixture.target.IsolationDomainID}}}
	driverFor := func(repository *persistence.Repository) *reconcile.InvocationRuntimeDriver {
		t.Helper()
		driver, err := reconcile.NewInvocationRuntimeDriver(repository, transport, reconcile.InvocationRuntimeRequestBuilderFunc(func(target persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error) {
			return dgruntime.StartRequest{Prompt: "result fixture", OutputSchema: target.OutputSchema, ApprovalMode: dgruntime.ApprovalLocked, SandboxMode: dgruntime.SandboxReadOnly}, nil
		}), transport, transport, transport, transport, reconcile.InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return driver
	}
	result, err := driverFor(fixture.repository).ApplyClaimed(ctx, fixture.claim, fixture.effect)
	if err != nil {
		t.Fatal("completed answer did not pass schema validation", err)
	}
	assertAnswer := func(result any) {
		t.Helper()
		encoded, err := json.Marshal(result)
		if err != nil || string(encoded) != `{"answer":42}` {
			t.Fatalf("answer = %s, %v", encoded, err)
		}
	}
	assertAnswer(result["output"])
	if err := fixture.repository.ScheduleRetry(ctx, fixture.claim, "unknown", "WORKER_REPLACED", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	repository := persistence.NewRepository(fixture.pool)
	worker := reconcile.New(repository, driverFor(repository), "output-replacement-worker")
	runToTerminal(t, ctx, worker, repository, fixture.target.IsolationDomainID, fixture.claim.ID, "succeeded")
	invocation, err := repository.GetInvocation(ctx, fixture.target.IsolationDomainID, fixture.target.InvocationID)
	if err != nil || invocation.State != "succeeded" || invocation.Error != nil {
		t.Fatal("result did not survive recovery", invocation, err)
	}
	assertAnswer(invocation.Result["output"])
	if transport.starts != 1 {
		t.Fatal("replacement worker repeated native execution", transport.starts)
	}
	events, err := repository.ListEvents(ctx, fixture.target.IsolationDomainID, fixture.target.InvocationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	completions := 0
	for _, event := range events {
		if event.Type == dgruntime.MessageCompletedEvent {
			completions++
		}
	}
	if completions != 2 {
		t.Fatal("completed messages were not retained", completions)
	}
}

type completedOutputTransport struct{ interruptionTransport }

func (transport *completedOutputTransport) New(execution.RuntimeSession) (reconcile.InvocationRuntimeAdapter, error) {
	return transport, nil
}
func (transport *completedOutputTransport) Start(context.Context, dgruntime.StartRequest) (dgruntime.Turn, error) {
	transport.starts++
	events := make(chan dgruntime.Event, 5)
	events <- dgruntime.Event{Sequence: 1, Type: "lifecycle.started", Payload: map[string]any{"message": "Runtime turn started."}}
	events <- dgruntime.Event{Sequence: 2, Type: "output.text.delta", Payload: map[string]any{"text": "Checking the data."}}
	events <- dgruntime.Event{Sequence: 3, Type: dgruntime.MessageCompletedEvent, Payload: dgruntime.CompletedMessage{Text: "Checking the data.", Phase: "commentary"}.Payload()}
	events <- dgruntime.Event{Sequence: 4, Type: dgruntime.MessageCompletedEvent, Payload: dgruntime.CompletedMessage{Text: `{"answer":42}`, Phase: "final"}.Payload()}
	events <- dgruntime.Event{Sequence: 5, Type: "lifecycle.succeeded", Payload: map[string]any{"message": "Runtime turn completed."}}
	close(events)
	return &completedOutputTurn{interruptionTurn{events: events}}, nil
}

type completedOutputTurn struct{ interruptionTurn }

func (*completedOutputTurn) Wait(context.Context) error { return nil }
