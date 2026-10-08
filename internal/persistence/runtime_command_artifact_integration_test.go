package persistence_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestCommandOutputArtifactSurvivesWorkerReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixtureWithProfile(t, ctx, false, nil, persistence.GovernedInvocationRuntimeProfile)
	t.Cleanup(func() {
		if _, err := fixture.pool.Exec(context.Background(), `DELETE FROM invocation_runtime_attempts WHERE isolation_domain_id=$1`, fixture.claim.IsolationDomainID); err != nil {
			t.Error(err)
		}
	})
	ref := dgruntime.CommandArtifact{ArtifactID: identity.Derived("art", fixture.claim.IsolationDomainID+":"+fixture.target.InvocationID+":runtime-command:2"), Digest: "sha256:" + strings.Repeat("0", 64), SizeBytes: 70000, Status: "failed", Preview: "preview"}
	for _, event := range []persistence.InvocationRuntimeEvent{
		{SourceSequence: 2, Type: dgruntime.CommandArtifactEvent, Payload: ref.Payload()},
		{SourceSequence: 2, Type: dgruntime.CommandCompletedEvent, Payload: dgruntime.CompletedCommand{Text: "private raw output", Status: "failed"}.Payload()},
	} {
		if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, event); err == nil {
			t.Fatal("unbound or raw output entered journal")
		}
	}
	text := strings.Repeat("command output\x00界\n", 6000)
	transport := &commandOutputTransport{interruptionTransport: interruptionTransport{questionBridgeTransport: questionBridgeTransport{scope: fixture.target.IsolationDomainID}}, text: text}
	objects := &retentionObjects{values: map[string][]byte{}}
	finalizer, err := artifact.NewFinalizer(fixture.repository, objects, objects, artifact.FinalizerConfig{MaximumBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	driverFor := func(repository *persistence.Repository) *reconcile.InvocationRuntimeDriver {
		driver, err := reconcile.NewInvocationRuntimeDriver(repository, transport, reconcile.InvocationRuntimeRequestBuilderFunc(func(target persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error) {
			return dgruntime.StartRequest{Prompt: "command", ApprovalMode: dgruntime.ApprovalLocked, SandboxMode: dgruntime.SandboxReadOnly}, nil
		}), transport, transport, transport, finalizer, reconcile.InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return driver
	}
	result, err := driverFor(fixture.repository).ApplyClaimed(ctx, fixture.claim, fixture.effect)
	if err != nil {
		t.Fatal(err)
	}
	if result["output"].(map[string]any)["text"] != "answer" {
		t.Fatal("command output replaced the answer", result)
	}
	record, err := fixture.repository.GetInvocationArtifactRecord(ctx, fixture.claim.IsolationDomainID, ref.ArtifactID)
	if err != nil || record.Kind != "log" || !record.Sensitive || string(objects.values[record.ObjectKey]) != text || len(objects.values) != 1 {
		t.Fatal("snapshot lost", err)
	}
	events, err := fixture.repository.ListEvents(ctx, fixture.claim.IsolationDomainID, fixture.target.InvocationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == dgruntime.CommandCompletedEvent {
			t.Fatal("raw command output escaped")
		}
		if event.Type != dgruntime.CommandArtifactEvent {
			continue
		}
		count++
		encoded, _ := json.Marshal(event.Payload)
		if len(encoded) > 8192 {
			t.Fatal("unbounded event")
		}
		reference, err := dgruntime.ParseCommandArtifact(event.Payload)
		if err != nil || reference.ArtifactID != record.ID || reference.Digest != record.Digest || reference.Status != "failed" {
			t.Fatal("reference changed", err)
		}
		replay := persistence.InvocationRuntimeEvent{SourceSequence: 2, Type: event.Type, Payload: event.Payload}
		got, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, fixture.claim, replay)
		if err != nil || got.ID != event.ID {
			t.Fatal("exact replay changed", err)
		}
		for _, boundary := range []string{"digest", "size", "sequence", "scope", "lease", "preview"} {
			copy := reference
			claim := fixture.claim
			changed := replay
			switch boundary {
			case "digest":
				copy.Digest = "sha256:" + strings.Repeat("0", 64)
			case "size":
				copy.SizeBytes++
			case "sequence":
				changed.SourceSequence++
			case "scope":
				claim.IsolationDomainID = "iso_00000000000000000999"
			case "lease":
				claim.FencingToken++
			case "preview":
				copy.Preview = "substituted"
			}
			changed.Payload = copy.Payload()
			if _, err := fixture.repository.RecordInvocationRuntimeEvent(ctx, claim, changed); err == nil {
				t.Fatal("invalid reference accepted", boundary)
			}
		}
	}
	if count != 1 {
		t.Fatal("missing command artifact", count)
	}
	if err := fixture.repository.ScheduleRetry(ctx, fixture.claim, "unknown", "WORKER_REPLACED", time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	replacement := persistence.NewRepository(fixture.pool)
	runToTerminal(t, ctx, reconcile.New(replacement, driverFor(replacement), "command-output-replacement"), replacement, fixture.claim.IsolationDomainID, fixture.claim.ID, "succeeded")
	if transport.starts != 1 || transport.exports != 0 || len(objects.values) != 1 {
		t.Fatal("replacement repeated runtime effects")
	}
}

type commandOutputTransport struct {
	interruptionTransport
	text string
}

func (transport *commandOutputTransport) New(execution.RuntimeSession) (reconcile.InvocationRuntimeAdapter, error) {
	return transport, nil
}
func (transport *commandOutputTransport) Start(context.Context, dgruntime.StartRequest) (dgruntime.Turn, error) {
	transport.starts++
	code := int32(1)
	events := make(chan dgruntime.Event, 4)
	events <- dgruntime.Event{Sequence: 1, Type: "lifecycle.started", Payload: map[string]any{"message": "started"}}
	events <- dgruntime.Event{Sequence: 2, Type: dgruntime.CommandCompletedEvent, Payload: dgruntime.CompletedCommand{Text: transport.text, Status: "failed", ExitCode: &code}.Payload()}
	events <- dgruntime.Event{Sequence: 3, Type: dgruntime.MessageCompletedEvent, Payload: dgruntime.CompletedMessage{Text: "answer", Phase: "final"}.Payload()}
	events <- dgruntime.Event{Sequence: 4, Type: "lifecycle.succeeded", Payload: map[string]any{"message": "completed"}}
	close(events)
	return &completedOutputTurn{interruptionTurn{events: events}}, nil
}
