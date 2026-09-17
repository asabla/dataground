// Package runtimetest checks the worker-facing adapter contract against
// deterministic, adapter-owned native fixtures. It does not certify a live
// runtime, provider, capability manifest, or deployment.
package runtimetest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

type Scenario string

const (
	Success         Scenario = "success"
	Failure         Scenario = "failure"
	Interrupt       Scenario = "interrupt"
	ProtocolFailure Scenario = "protocol-failure"
	ScopeViolation  Scenario = "scope-violation"
	ProcessFailure  Scenario = "process-failure"
	Ownership       Scenario = "single-turn-ownership"
	Validation      Scenario = "invalid-start"
	Approve         Scenario = "approval-approve"
	Deny            Scenario = "approval-deny"
	Question        Scenario = "question-answer"
	RejectApproval  Scenario = "unsupported-approval"
	RejectQuestion  Scenario = "unsupported-question"
)

// NativeCanary belongs in fixture-native identifiers, paths, commands, and
// errors, never in intentionally public text. Normalization must remove it.
const NativeCanary = "dg-native-private-"
const OutputText = "Conformance output."

// Features describes this test configuration, not a certified capability
// profile. Disabled interactions must be rejected before a turn is admitted.
type Features struct{ Approvals, Questions bool }

// Fixture starts one native session. It emits lifecycle.started during Start,
// then waits for Release before delivering the scenario. Verify checks native
// effects, including exact decision/answer delivery and cancellation target.
// Release must be idempotent and unblock fixture cleanup after a failed test.
type Fixture struct {
	Adapter dgruntime.Adapter
	Release func()
	Verify  func(context.Context) error
}
type Factory func(*testing.T, Scenario) Fixture

// Run applies identical normalized assertions to every adapter. Each fixture
// owns protocol translation only; it must not supply expected normalized data.
func Run(t *testing.T, factory Factory, features Features) {
	t.Helper()
	if factory == nil {
		t.Fatal("runtime contract fixture factory is absent")
	}
	scenarios := []Scenario{Success, Failure, Interrupt, ProtocolFailure, ScopeViolation, ProcessFailure, Ownership, Validation}
	if features.Approvals {
		scenarios = append(scenarios, Approve, Deny)
	} else {
		scenarios = append(scenarios, RejectApproval)
	}
	if features.Questions {
		scenarios = append(scenarios, Question)
	} else {
		scenarios = append(scenarios, RejectQuestion)
	}
	for _, scenario := range scenarios {
		t.Run(string(scenario), func(t *testing.T) { run(t, factory(t, scenario), scenario) })
	}
}

func run(t *testing.T, f Fixture, scenario Scenario) {
	t.Helper()
	if f.Adapter == nil || f.Release == nil || f.Verify == nil {
		t.Fatal("runtime contract fixture is incomplete")
	}
	t.Cleanup(func() { f.Release(); _ = f.Adapter.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := dgruntime.StartRequest{Prompt: "Conformance input.", WorkingDir: "/workspace", ApprovalMode: dgruntime.ApprovalLocked, SandboxMode: dgruntime.SandboxReadOnly, QuestionMode: dgruntime.QuestionDisabled}
	if scenario == Approve || scenario == Deny || scenario == RejectApproval {
		request.ApprovalMode = dgruntime.ApprovalInteractive
		request.SandboxMode = dgruntime.SandboxWorkspaceWrite
	}
	if scenario == Question || scenario == RejectQuestion {
		request.QuestionMode = dgruntime.QuestionInteractive
		request.QuestionTimeout = time.Minute
	}
	if scenario == RejectApproval || scenario == RejectQuestion {
		turn, err := f.Adapter.Start(ctx, request)
		if err == nil || turn != nil {
			t.Fatal("unsupported interaction was admitted")
		}
		requireSafeError(t, err)
		if err := f.Adapter.Close(); err != nil {
			t.Fatal(err)
		}
		f.Release()
		if err := f.Verify(ctx); err != nil {
			t.Fatal(err)
		}
		return
	}
	if scenario == Validation {
		for _, change := range []func(*dgruntime.StartRequest){
			func(r *dgruntime.StartRequest) { r.Prompt = "" },
			func(r *dgruntime.StartRequest) { r.ApprovalMode = "unknown" },
			func(r *dgruntime.StartRequest) { r.SandboxMode = "unrestricted" },
			func(r *dgruntime.StartRequest) { r.WorkingDir = "../host" },
			func(r *dgruntime.StartRequest) { r.QuestionMode = "unknown" },
			func(r *dgruntime.StartRequest) { r.QuestionTimeout = time.Second },
			func(r *dgruntime.StartRequest) { r.QuestionMode = dgruntime.QuestionInteractive; r.QuestionTimeout = 0 },
			func(r *dgruntime.StartRequest) {
				r.QuestionMode = dgruntime.QuestionInteractive
				r.QuestionTimeout = 16 * time.Minute
			},
		} {
			invalid := request
			change(&invalid)
			turn, err := f.Adapter.Start(ctx, invalid)
			if err == nil || turn != nil {
				t.Fatal("invalid start acquired runtime authority")
			}
			requireSafeError(t, err)
		}
	}
	turn, err := f.Adapter.Start(ctx, request)
	if err != nil || turn == nil {
		t.Fatalf("start normalized turn: %v", err)
	}
	t.Cleanup(func() { _ = turn.Close() })
	stream := eventReader{t: t, ctx: ctx, events: turn.Events()}
	stream.next("lifecycle.started")
	if scenario == Ownership {
		next, err := f.Adapter.Start(ctx, request)
		if next != nil || !errors.Is(err, dgruntime.ErrConcurrentTurn) {
			t.Fatal("concurrent start was not rejected", err)
		}
		cancelled, stop := context.WithCancel(ctx)
		stop()
		if err := turn.Wait(cancelled); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled observer did not return cancellation", err)
		}
	}
	f.Release()
	switch scenario {
	case Success:
		output := stream.next("output.text.delta")
		if output.Payload["text"] != OutputText {
			t.Fatal("output text changed")
		}
		for _, kind := range []string{"tool", "process", "file"} {
			for _, state := range []string{"started", "completed"} {
				event := stream.next("activity." + kind + "." + state)
				want := map[string]string{"tool": "tool", "process": "command", "file": "change"}[kind]
				if event.Payload["kind"] != want {
					t.Fatal("activity classification changed")
				}
			}
		}
		stream.next("lifecycle.succeeded")
	case Validation, Ownership:
		stream.next("lifecycle.succeeded")
	case Failure:
		failed := stream.next("lifecycle.failed")
		if failed.Payload["code"] != "RUNTIME_TURN_FAILED" || failed.Payload["retryable"] != false {
			t.Fatal("runtime failure lost safe classification")
		}
	case Interrupt:
		if err := turn.Interrupt(ctx); err != nil {
			t.Fatal(err)
		}
		stream.next("lifecycle.cancelled")
	case ProtocolFailure, ScopeViolation, ProcessFailure:
		requireWait(t, ctx, turn, dgruntime.ErrProtocol)
		// A protocol failure must not invent successful completion.
		select {
		case event, open := <-turn.Events():
			if open {
				t.Fatalf("protocol failure emitted an extra event: %s", event.Type)
			}
		default:
		}
	case Approve, Deny:
		approval := stream.next("interaction.approval.requested")
		id, ok := approval.Payload["approvalId"].(string)
		if !ok || id == "" || approval.Payload["action"] != "process.execute" {
			t.Fatal("approval has no normalized authority")
		}
		interactive, ok := turn.(dgruntime.ApprovalTurn)
		if !ok {
			t.Fatal("declared approval support has no liveness contract")
		}
		pending, err := interactive.ApprovalPending(ctx, id)
		if err != nil || !pending {
			t.Fatal("approval resolved before platform decision", err)
		}
		pending, err = interactive.ApprovalPending(ctx, "unknown-handle")
		if err != nil || pending {
			t.Fatal("unknown approval gained authority", err)
		}
		if err := turn.ResolveApproval(ctx, id, "invalid"); !errors.Is(err, dgruntime.ErrApprovalDecision) {
			t.Fatal("invalid approval decision accepted", err)
		}
		decision := dgruntime.ApprovalDeny
		if scenario == Approve {
			decision = dgruntime.ApprovalApprove
		}
		if err := turn.ResolveApproval(ctx, id, decision); err != nil {
			t.Fatal(err)
		}
		if err := turn.ResolveApproval(ctx, id, decision); !errors.Is(err, dgruntime.ErrApprovalNotFound) {
			t.Fatal("duplicate approval retained authority", err)
		}
		stream.next("lifecycle.succeeded")
		pending, err = interactive.ApprovalPending(ctx, id)
		if err != nil || pending {
			t.Fatal("terminal approval retained authority", err)
		}
	case Question:
		question := stream.next("interaction.question.requested")
		id, ok := question.Payload["questionId"].(string)
		if !ok || id == "" {
			t.Fatal("question has no normalized handle")
		}
		prompts, ok := question.Payload["questions"].([]domain.QuestionPrompt)
		if !ok || domain.ValidateQuestionPrompts(prompts) != nil || len(prompts) != 2 || len(prompts[0].Options) != 2 || prompts[0].AllowFreeText || !prompts[1].AllowFreeText {
			t.Fatal("question semantics changed")
		}
		expiry, ok := question.Payload["expiresAt"].(string)
		at, err := time.Parse(time.RFC3339Nano, expiry)
		if !ok || err != nil || !at.After(time.Now()) || at.After(time.Now().Add(request.QuestionTimeout)) {
			t.Fatal("question expiry is not bounded")
		}
		interactive, ok := turn.(dgruntime.QuestionTurn)
		if !ok {
			t.Fatal("declared question support has no answer contract")
		}
		pending, err := interactive.QuestionPending(ctx, id)
		if err != nil || !pending {
			t.Fatal("question resolved without an answer", err)
		}
		if err := interactive.AnswerQuestion(ctx, id, nil); !errors.Is(err, domain.ErrQuestionInvalid) {
			t.Fatal("missing answer was accepted", err)
		}
		text := "Explicit context"
		answers := []domain.QuestionAnswer{{QuestionID: prompts[0].ID, OptionIDs: []string{prompts[0].Options[0].ID}}, {QuestionID: prompts[1].ID, Text: &text}}
		prompts[0].Options[0].Label = "tampered display copy"
		if err := interactive.AnswerQuestion(ctx, id, answers); err != nil {
			t.Fatal(err)
		}
		if err := interactive.AnswerQuestion(ctx, id, answers); !errors.Is(err, dgruntime.ErrQuestionNotFound) {
			t.Fatal("duplicate question answer retained authority", err)
		}
		stream.next("lifecycle.succeeded")
		pending, err = interactive.QuestionPending(ctx, id)
		if err != nil || pending {
			t.Fatal("terminal question retained authority", err)
		}
	default:
		t.Fatal("unknown runtime contract scenario")
	}
	want := error(nil)
	if scenario == Failure {
		want = dgruntime.ErrTurnFailed
	}
	if scenario == ProtocolFailure || scenario == ScopeViolation || scenario == ProcessFailure {
		want = dgruntime.ErrProtocol
	}
	requireWait(t, ctx, turn, want)
	requireWait(t, ctx, turn, want)
	if scenario == Ownership {
		next, err := f.Adapter.Start(ctx, request)
		if next != nil || !errors.Is(err, dgruntime.ErrConcurrentTurn) {
			t.Fatal("completed adapter admitted another invocation", err)
		}
	}
	if err := f.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := turn.Close(); err != nil {
		t.Fatal("close is not idempotent", err)
	}
	if err := f.Adapter.Close(); err != nil {
		t.Fatal(err)
	}
}

func requireWait(t *testing.T, ctx context.Context, turn dgruntime.Turn, want error) {
	t.Helper()
	err := turn.Wait(ctx)
	requireSafeError(t, err)
	if !errors.Is(err, want) {
		t.Fatalf("wait classification = %v, want %v", err, want)
	}
}
func requireSafeError(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), NativeCanary) {
		t.Fatal("native context escaped in adapter error")
	}
}

type eventReader struct {
	t        *testing.T
	ctx      context.Context
	events   <-chan dgruntime.Event
	sequence uint64
}

func (reader *eventReader) next(kind string) dgruntime.Event {
	reader.t.Helper()
	select {
	case event, ok := <-reader.events:
		if !ok {
			reader.t.Fatal("event stream closed before expected lifecycle")
		}
		reader.sequence++
		if err := validateEvent(event, reader.sequence, kind); err != nil {
			reader.t.Fatal(err)
		}
		return event
	case <-reader.ctx.Done():
		reader.t.Fatalf("missing normalized event %s", kind)
		return dgruntime.Event{}
	}
}
func validateEvent(event dgruntime.Event, sequence uint64, kind string) error {
	if sequence == 0 || event.Sequence != sequence || event.Type != kind || event.Payload == nil {
		return errors.New("normalized event sequence, type, or payload differs")
	}
	encoded, err := json.Marshal(event)
	if err != nil || len(encoded) > 128<<10 || strings.Contains(string(encoded), NativeCanary) {
		return errors.New("normalized event contains invalid, oversized, or private data")
	}
	return nil
}
