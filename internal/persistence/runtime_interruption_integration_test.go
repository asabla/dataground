package persistence_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestInterruptedInvocationSurvivesWorkerReplacementWithoutSuccessOrRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixtureWithReservation(t, ctx, false)
	transport := &interruptionTransport{questionBridgeTransport: questionBridgeTransport{scope: fixture.target.IsolationDomainID}}
	driverFor := func(repository *persistence.Repository) *reconcile.InvocationRuntimeDriver {
		t.Helper()
		driver, err := reconcile.NewInvocationRuntimeDriver(repository, transport, reconcile.InvocationRuntimeRequestBuilderFunc(func(persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error) {
			return dgruntime.StartRequest{Prompt: "interruption fixture", ApprovalMode: dgruntime.ApprovalLocked, SandboxMode: dgruntime.SandboxWorkspaceWrite, Artifacts: []dgruntime.ArtifactDeclaration{{ID: "report", Name: "Report", SandboxPath: "/workspace/report", MediaType: "text/plain", Kind: "file"}}}, nil
		}), transport, transport, transport, transport, reconcile.InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return driver
	}
	driver := driverFor(fixture.repository)
	result, err := driver.ApplyClaimed(ctx, fixture.claim, fixture.effect)
	if result != nil || !errors.Is(err, reconcile.ErrEffectTerminal) || !errors.Is(err, dgruntime.ErrTurnInterrupted) {
		t.Fatal("interruption became successful or ambiguous", result, err)
	}
	attempt, err := fixture.repository.GetInvocationRuntimeAttempt(ctx, fixture.target.IsolationDomainID, fixture.claim.ID)
	if err != nil || attempt.Status != "failed" || attempt.Result["code"] != "RUNTIME_TURN_INTERRUPTED" {
		t.Fatal("interruption was not durably recorded", attempt, err)
	}
	if _, err := fixture.repository.CompleteInvocationRuntimeAttempt(ctx, fixture.claim, fixture.effect, map[string]any{"status": "succeeded"}); !errors.Is(err, persistence.ErrInvocationRuntimeAttemptConflict) {
		t.Fatal("terminal interruption was overwritten", err)
	}
	// Simulate loss of the worker after its durable attempt receipt, before the
	// reconciler records the external-effect outcome and invocation failure.
	if err := fixture.repository.ScheduleRetry(ctx, fixture.claim, "unknown", "WORKER_REPLACED", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	repository := persistence.NewRepository(fixture.pool)
	worker := reconcile.New(repository, driverFor(repository), "replacement-worker")
	if ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil || !ran {
		t.Fatal("replacement worker did not settle interruption", ran, err)
	}
	invocation, err := repository.GetInvocation(ctx, fixture.target.IsolationDomainID, fixture.target.InvocationID)
	if err != nil || invocation.State != "failed" || invocation.Result != nil || invocation.Error == nil {
		t.Fatal("interrupted invocation is not a terminal failure", invocation, err)
	}
	if transport.starts != 1 || transport.exports != 0 || transport.finalizations != 0 {
		t.Fatal("interrupted runtime restarted or published artifacts", transport.starts, transport.exports, transport.finalizations)
	}
	if ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil || ran {
		t.Fatal("terminal interruption was automatically retried", ran, err)
	}
	events, err := repository.ListEvents(ctx, fixture.target.IsolationDomainID, fixture.target.InvocationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	interrupted, failed := false, false
	for _, event := range events {
		if event.Type == "lifecycle.succeeded" {
			t.Fatal("interrupted invocation emitted success")
		}
		if event.Type == "lifecycle.cancelled" && event.Source == "runtime" {
			interrupted = true
		}
		if event.Type == "lifecycle.failed" {
			failed = true
		}
	}
	if !interrupted || !failed {
		t.Fatal("runtime interruption or authoritative failure is missing from the journal")
	}
}

type interruptionTransport struct {
	questionBridgeTransport
	starts, exports, finalizations int
}

func (*interruptionTransport) AuthorizeInvocationRuntime(context.Context, persistence.InvocationRuntimeTarget, dgruntime.StartRequest) error {
	return nil
}
func (transport *interruptionTransport) New(execution.RuntimeSession) (reconcile.InvocationRuntimeAdapter, error) {
	return transport, nil
}
func (transport *interruptionTransport) Start(context.Context, dgruntime.StartRequest) (dgruntime.Turn, error) {
	transport.starts++
	events := make(chan dgruntime.Event, 3)
	events <- dgruntime.Event{Sequence: 1, Type: "lifecycle.started", Payload: map[string]any{"message": "Runtime turn started."}}
	events <- dgruntime.Event{Sequence: 2, Type: "output.text.delta", Payload: map[string]any{"text": "incomplete result"}}
	events <- dgruntime.Event{Sequence: 3, Type: "lifecycle.cancelled", Payload: map[string]any{"reason": "runtime interruption"}}
	close(events)
	return &interruptionTurn{events: events}, nil
}
func (transport *interruptionTransport) Export(context.Context, execution.ExportRequest) (execution.ExportResult, error) {
	transport.exports++
	return execution.ExportResult{}, errors.New("unexpected export")
}
func (transport *interruptionTransport) Finalize(context.Context, artifact.Finalization) (artifact.Record, error) {
	transport.finalizations++
	return artifact.Record{}, errors.New("unexpected finalization")
}

type interruptionTurn struct{ events <-chan dgruntime.Event }

func (turn *interruptionTurn) Events() <-chan dgruntime.Event { return turn.events }
func (*interruptionTurn) ResolveApproval(context.Context, string, dgruntime.ApprovalDecision) error {
	return dgruntime.ErrApprovalNotFound
}
func (*interruptionTurn) Interrupt(context.Context) error { return nil }
func (*interruptionTurn) Wait(context.Context) error      { return dgruntime.ErrTurnInterrupted }
func (*interruptionTurn) Close() error                    { return nil }
