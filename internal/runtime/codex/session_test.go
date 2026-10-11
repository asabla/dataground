package codex_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
)

func TestSessionOwnsSequentialTurnsAndFreezesOldHandles(t *testing.T) {
	t.Parallel()
	finishFirst := make(chan struct{})
	transport := newScriptedSession(t, func(server *scriptServer) {
		server.completeStart("thread", "first")
		server.request(101, "item/fileChange/requestApproval", map[string]any{"threadId": "thread", "turnId": "first"})
		<-finishFirst
		server.notify("thread/tokenUsage/updated", sessionUsage("first", 10))
		server.notify("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "first", "status": "completed"}})
		next := server.read()
		server.requireMethod(next, "turn/start")
		var params map[string]any
		server.decodeParams(next, &params)
		if params["threadId"] != "thread" || len(params) != 3 || params["outputSchema"] == nil {
			t.Errorf("next turn changed session configuration: %#v", params)
		}
		server.respond(next.ID, map[string]any{"turn": map[string]any{"id": "second"}})
		server.notify("turn/started", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "second"}})
		// Queued frames from the retired turn cannot enter the next event queue.
		server.notify("item/agentMessage/delta", map[string]any{"threadId": "thread", "turnId": "first", "delta": "retired content"})
		server.request(102, "item/fileChange/requestApproval", map[string]any{"threadId": "thread", "turnId": "second"})
		server.notify("serverRequest/resolved", map[string]any{"threadId": "thread", "requestId": 101})
		approval := server.read()
		if string(approval.ID) != "102" || approval.Method != "" {
			t.Errorf("wrong request controlled: %s", approval.Raw)
		}
		server.notify("thread/tokenUsage/updated", sessionUsage("second", 30))
		server.notify("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "second", "status": "failed"}})
		third := server.read()
		server.requireMethod(third, "turn/start")
		server.respond(third.ID, map[string]any{"turn": map[string]any{"id": "third"}})
		server.notify("turn/started", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "third"}})
		interrupted := server.read()
		server.requireMethod(interrupted, "turn/interrupt")
		server.decodeParams(interrupted, &params)
		if params["turnId"] != "third" {
			t.Errorf("interrupted wrong turn: %#v", params)
		}
		server.respond(interrupted.ID, map[string]any{})
		server.notify("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "third", "status": "interrupted"}})
	})
	session, err := codex.NewSession(transport)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := dgruntime.StartRequest{Prompt: "first", WorkingDir: "/sandbox/workspace", ApprovalMode: dgruntime.ApprovalInteractive}
	first, err := session.StartTurn(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	initial := collectEvents(t, first.Events(), 2)
	firstApproval := initial[1].Payload["approvalId"].(string)
	if _, err := session.StartTurn(ctx, request); !errors.Is(err, dgruntime.ErrConcurrentTurn) {
		t.Fatalf("concurrent start = %v", err)
	}
	for _, change := range []func(*dgruntime.StartRequest){
		func(r *dgruntime.StartRequest) { r.Model = "different" },
		func(r *dgruntime.StartRequest) { r.WorkingDir = "/workspace" },
		func(r *dgruntime.StartRequest) { r.SandboxMode = dgruntime.SandboxWorkspaceWrite },
		func(r *dgruntime.StartRequest) { r.ApprovalMode = dgruntime.ApprovalLocked },
		func(r *dgruntime.StartRequest) {
			r.QuestionMode = dgruntime.QuestionInteractive
			r.QuestionTimeout = time.Minute
		},
	} {
		changed := request
		change(&changed)
		if _, err := session.StartTurn(ctx, changed); !errors.Is(err, dgruntime.ErrSessionConfiguration) {
			t.Fatalf("changed configuration = %v", err)
		}
	}
	close(finishFirst)
	if err := first.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := session.StartTurn(ctx, request); !errors.Is(err, dgruntime.ErrTurnEventsPending) {
		t.Fatalf("undrained start = %v", err)
	}
	tail := collectEvents(t, first.Events(), 2)
	if tail[0].Type != "usage.session.recorded" || tail[1].Type != "lifecycle.succeeded" {
		t.Fatalf("first tail = %#v", tail)
	}
	request.Prompt = "second"
	request.OutputSchema = map[string]any{"type": "object"}
	second, err := session.StartTurn(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, second.Events(), 2)
	if events[0].Sequence != 1 || events[0].Type != "lifecycle.started" || events[1].Type != "interaction.approval.requested" {
		t.Fatalf("second events = %#v", events)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Interrupt(ctx); !errors.Is(err, dgruntime.ErrClosed) {
		t.Fatalf("old interrupt = %v", err)
	}
	if err := first.ResolveApproval(ctx, firstApproval, dgruntime.ApprovalApprove); !errors.Is(err, dgruntime.ErrApprovalNotFound) {
		t.Fatalf("old approval = %v", err)
	}
	if pending, err := first.(dgruntime.ApprovalTurn).ApprovalPending(ctx, firstApproval); pending || err != nil {
		t.Fatalf("old pending = %v %v", pending, err)
	}
	if err := first.Wait(ctx); err != nil {
		t.Fatalf("old result changed: %v", err)
	}
	if err := second.ResolveApproval(ctx, events[1].Payload["approvalId"].(string), dgruntime.ApprovalApprove); err != nil {
		t.Fatal(err)
	}
	if err := second.Wait(ctx); !errors.Is(err, dgruntime.ErrTurnFailed) {
		t.Fatalf("second result = %v", err)
	}
	tail = collectEvents(t, second.Events(), 2)
	if tail[0].Type != "usage.session.recorded" || tail[1].Type != "lifecycle.failed" {
		t.Fatalf("second tail = %#v", tail)
	}
	third, err := session.StartTurn(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	collectEvents(t, third.Events(), 1)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := third.Interrupt(ctx); err != nil {
		t.Fatal(err)
	}
	if err := third.Wait(ctx); !errors.Is(err, dgruntime.ErrTurnInterrupted) {
		t.Fatalf("third result = %v", err)
	}
	if err := second.Wait(ctx); !errors.Is(err, dgruntime.ErrTurnFailed) {
		t.Fatalf("second result changed = %v", err)
	}
	if err := transport.scriptError(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.StartTurn(ctx, request); !errors.Is(err, dgruntime.ErrClosed) {
		t.Fatalf("closed start = %v", err)
	}
}

func sessionUsage(turn string, total int) map[string]any {
	counts := map[string]any{"inputTokens": total, "outputTokens": 0, "totalTokens": total, "cachedInputTokens": 0, "reasoningOutputTokens": 0}
	return map[string]any{"threadId": "thread", "turnId": turn, "tokenUsage": map[string]any{"total": counts, "last": counts}}
}

func TestSessionRejectsReusedNativeIdentities(t *testing.T) {
	for _, boundary := range []string{"turn", "request"} {
		t.Run(boundary, func(t *testing.T) {
			transport := newScriptedSession(t, func(server *scriptServer) {
				server.completeStart("thread", "first")
				if boundary == "request" {
					server.request(101, "item/fileChange/requestApproval", map[string]any{"threadId": "thread", "turnId": "first"})
					server.read() // Locked policy declines without authority.
				}
				server.notify("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "first", "status": "completed"}})
				next := server.read()
				server.requireMethod(next, "turn/start")
				turnID := "second"
				if boundary == "turn" {
					turnID = "first"
				}
				server.respond(next.ID, map[string]any{"turn": map[string]any{"id": turnID}})
				if boundary == "request" {
					server.request(101, "item/fileChange/requestApproval", map[string]any{"threadId": "thread", "turnId": "second"})
				}
			})
			session, err := codex.NewSession(transport)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			request := dgruntime.StartRequest{Prompt: "start"}
			first, err := session.StartTurn(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if err := first.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			collectEvents(t, first.Events(), 2)
			next, err := session.StartTurn(ctx, request)
			if boundary == "turn" {
				if !errors.Is(err, dgruntime.ErrProtocol) {
					t.Fatalf("reused turn = %v", err)
				}
			} else {
				if err == nil {
					err = next.Wait(ctx)
				}
				if !errors.Is(err, dgruntime.ErrProtocol) {
					t.Fatalf("reused request = %v", err)
				}
			}
			if err := first.Wait(ctx); err != nil {
				t.Fatalf("earlier success lost: %v", err)
			}
		})
	}
}

func TestSessionRetiresQuestionAuthority(t *testing.T) {
	finish := make(chan struct{})
	transport := newScriptedSession(t, func(server *scriptServer) {
		completeQuestionStart(server)
		server.request(201, "item/tool/requestUserInput", nativeQuestionParams())
		<-finish
		server.notify("turn/completed", map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": "native-turn", "status": "completed"}})
		next := server.read()
		server.requireMethod(next, "turn/start")
		server.respond(next.ID, map[string]any{"turn": map[string]any{"id": "second"}})
		server.notify("turn/started", map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": "second"}})
		params := nativeQuestionParams()
		params["turnId"] = "second"
		server.request(202, "item/tool/requestUserInput", params)
		response := server.read()
		if string(response.ID) != "202" {
			t.Errorf("answered wrong request: %s", response.Raw)
		}
		server.notify("turn/completed", map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": "second", "status": "completed"}})
	})
	session, err := codex.NewSession(transport)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request := dgruntime.StartRequest{Prompt: "inspect", QuestionMode: dgruntime.QuestionInteractive, QuestionTimeout: time.Minute}
	first, err := session.StartTurn(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	collectEvents(t, first.Events(), 2)
	close(finish)
	if err := first.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	collectEvents(t, first.Events(), 1)
	second, err := session.StartTurn(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	collectEvents(t, second.Events(), 2)
	old := first.(dgruntime.QuestionTurn)
	if pending, err := old.QuestionPending(ctx, "question-1"); pending || err != nil {
		t.Fatal("old question retained authority")
	}
	text := "explicit"
	answers := []domain.QuestionAnswer{{QuestionID: "item_1", OptionIDs: []string{"option_1"}}, {QuestionID: "item_2", Text: &text}}
	if err := old.AnswerQuestion(ctx, "question-1", answers); !errors.Is(err, dgruntime.ErrQuestionNotFound) {
		t.Fatalf("old answer = %v", err)
	}
	if err := second.(dgruntime.QuestionTurn).AnswerQuestion(ctx, "question-1", answers); err != nil {
		t.Fatal(err)
	}
	if err := second.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSessionCancelledStartCannotBeRetried(t *testing.T) {
	initialized := make(chan struct{})
	transport := newScriptedSession(t, func(server *scriptServer) {
		server.requireMethod(server.read(), "initialize")
		close(initialized)
	})
	session, err := codex.NewSession(transport)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	request := dgruntime.StartRequest{Prompt: "start"}
	go func() { _, err := session.StartTurn(ctx, request); result <- err }()
	select {
	case <-initialized:
	case <-ctx.Done():
		t.Fatal("initialize was not observed")
	}
	if _, err := session.StartTurn(ctx, request); !errors.Is(err, dgruntime.ErrConcurrentTurn) {
		t.Fatalf("concurrent initialize = %v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled start = %v", err)
	}
	if _, err := session.StartTurn(context.Background(), request); !errors.Is(err, dgruntime.ErrProtocol) {
		t.Fatalf("ambiguous start was reusable: %v", err)
	}
}
