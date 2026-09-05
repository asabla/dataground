package codex_test

import (
	"context"
	"errors"
	"testing"
	"time"

	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
)

func TestApprovalHandlesExpireBeforeTerminalEventsAreObserved(t *testing.T) {
	for _, status := range []string{"completed", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			finish := make(chan struct{})
			lateResponse := make(chan wireRecord, 1)
			session := newScriptedSession(t, func(server *scriptServer) {
				server.completeStart("thread", "turn")
				params := map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "item"}
				server.request("approval-request", "item/fileChange/requestApproval", params)
				<-finish
				server.notify("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": status}})
				server.request("late-request", "item/fileChange/requestApproval", params)
				lateResponse <- server.read()
			})
			client, err := codex.New(session)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "inspect", ApprovalMode: dgruntime.ApprovalInteractive})
			if err != nil {
				t.Fatal(err)
			}
			event := waitForEvent(t, turn.Events(), "interaction.approval.requested")
			close(finish)
			terminal := "lifecycle.succeeded"
			if status == "interrupted" {
				terminal = "lifecycle.cancelled"
			}
			if status == "failed" {
				terminal = "lifecycle.failed"
			}
			waitForEvent(t, turn.Events(), terminal)
			if err := turn.ResolveApproval(ctx, event.Payload["approvalId"].(string), dgruntime.ApprovalApprove); !errors.Is(err, dgruntime.ErrApprovalNotFound) {
				t.Fatalf("terminal approval remained actionable: %v", err)
			}
			select {
			case response := <-lateResponse:
				if response.Error == nil || response.Error.Code != -32600 || string(response.ID) != `"late-request"` {
					t.Fatalf("post-terminal request was not rejected: %s", response.Raw)
				}
			case <-ctx.Done():
				t.Fatal("late request response timed out")
			}
			if err := session.scriptError(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInterruptionClosesApprovalsBeforeNativeAcknowledgement(t *testing.T) {
	receivedInterrupt := make(chan struct{})
	acknowledge := make(chan struct{})
	session := newScriptedSession(t, func(server *scriptServer) {
		server.completeStart("thread", "turn")
		server.request("approval-request", "item/commandExecution/requestApproval", map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "item"})
		interrupt := server.read()
		server.requireMethod(interrupt, "turn/interrupt")
		close(receivedInterrupt)
		<-acknowledge
		server.respond(interrupt.ID, map[string]any{})
		server.notify("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "interrupted"}})
	})
	client, err := codex.New(session)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "inspect", ApprovalMode: dgruntime.ApprovalInteractive})
	if err != nil {
		t.Fatal(err)
	}
	event := waitForEvent(t, turn.Events(), "interaction.approval.requested")
	interrupted := make(chan error, 1)
	go func() { interrupted <- turn.Interrupt(ctx) }()
	select {
	case <-receivedInterrupt:
	case <-ctx.Done():
		t.Fatal("interrupt was not sent")
	}
	if err := turn.ResolveApproval(ctx, event.Payload["approvalId"].(string), dgruntime.ApprovalApprove); !errors.Is(err, dgruntime.ErrApprovalNotFound) {
		t.Fatalf("interrupted approval remained actionable: %v", err)
	}
	close(acknowledge)
	if err := <-interrupted; err != nil {
		t.Fatal(err)
	}
	if err := turn.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.scriptError(); err != nil {
		t.Fatal(err)
	}
}

func TestTransportClosureRetiresPendingApprovalHandles(t *testing.T) {
	session := newScriptedSession(t, func(server *scriptServer) {
		server.completeStart("thread", "turn")
		server.request("approval-request", "item/fileChange/requestApproval", map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "item"})
	})
	client, err := codex.New(session)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "inspect", ApprovalMode: dgruntime.ApprovalInteractive})
	if err != nil {
		t.Fatal(err)
	}
	event := waitForEvent(t, turn.Events(), "interaction.approval.requested")
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := turn.ResolveApproval(ctx, event.Payload["approvalId"].(string), dgruntime.ApprovalApprove); !errors.Is(err, dgruntime.ErrApprovalNotFound) {
		t.Fatalf("closed transport retained an actionable approval: %v", err)
	}
}

func TestNativeRequestClearanceRetiresOnlyTheExactApproval(t *testing.T) {
	decision := make(chan wireRecord, 1)
	session := newScriptedSession(t, func(server *scriptServer) {
		server.completeStart("thread", "turn")
		params := map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "item"}
		server.request("request-a", "item/fileChange/requestApproval", params)
		server.request("request-b", "item/fileChange/requestApproval", params)
		server.writeRaw([]byte("{\"method\":\"serverRequest/resolved\",\"params\":{\"threadId\":\"thread\",\"requestId\":\"request-\\u0061\"}}\n"))
		server.notify("serverRequest/resolved", map[string]any{"threadId": "thread", "requestId": "already-resolved"})
		server.notify("item/agentMessage/delta", map[string]any{"threadId": "thread", "turnId": "turn", "delta": "clearance processed"})
		decision <- server.read()
	})
	client, err := codex.New(session)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "inspect", ApprovalMode: dgruntime.ApprovalInteractive})
	if err != nil {
		t.Fatal(err)
	}
	first := waitForEvent(t, turn.Events(), "interaction.approval.requested")
	second := waitForEvent(t, turn.Events(), "interaction.approval.requested")
	waitForEvent(t, turn.Events(), "output.text.delta")
	if err := turn.ResolveApproval(ctx, first.Payload["approvalId"].(string), dgruntime.ApprovalApprove); !errors.Is(err, dgruntime.ErrApprovalNotFound) {
		t.Fatalf("cleared native approval remained actionable: %v", err)
	}
	if err := turn.ResolveApproval(ctx, second.Payload["approvalId"].(string), dgruntime.ApprovalDeny); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-decision:
		if string(response.ID) != `"request-b"` {
			t.Fatalf("resolved wrong approval: %s", response.Raw)
		}
	case <-ctx.Done():
		t.Fatal("decision was not delivered")
	}
}

func TestMalformedOrCrossThreadClearanceFailsClosed(t *testing.T) {
	for _, params := range []string{
		`{"threadId":"other-thread","requestId":"request"}`,
		`{"threadId":"thread"}`,
		`{"threadId":"thread","requestId":1.5}`,
		`{"threadId":"thread","requestId":9223372036854775808}`,
		`{"threadId":"thread","requestId":null}`,
	} {
		t.Run(params, func(t *testing.T) {
			started := make(chan struct{})
			session := newScriptedSession(t, func(server *scriptServer) {
				server.completeStart("thread", "turn")
				server.request("request", "item/fileChange/requestApproval", map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "item"})
				<-started
				server.writeRaw([]byte(`{"method":"serverRequest/resolved","params":` + params + "}\n"))
			})
			client, err := codex.New(session)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "inspect", ApprovalMode: dgruntime.ApprovalInteractive})
			if err != nil {
				t.Fatal(err)
			}
			close(started)
			if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrProtocol) {
				t.Fatalf("invalid clearance did not fail closed: %v", err)
			}
			if err := turn.ResolveApproval(ctx, "approval-1", dgruntime.ApprovalApprove); !errors.Is(err, dgruntime.ErrApprovalNotFound) {
				t.Fatalf("invalid clearance retained approval authority: %v", err)
			}
		})
	}
}
