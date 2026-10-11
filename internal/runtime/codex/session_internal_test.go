package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestSessionIdentityAndTurnBudgets(t *testing.T) {
	client, _ := queueFixture()
	client.sequential = true
	client.connection.active = client
	client.seenRequests = make(map[string]struct{})
	for index := 0; index < maximumSessionRequests; index++ {
		if !client.registerSessionRequest(wireMessage{ID: json.RawMessage(fmt.Sprint(index))}) {
			t.Fatal("request budget ended early")
		}
	}
	if client.registerSessionRequest(wireMessage{ID: json.RawMessage(fmt.Sprint(maximumSessionRequests))}) || !errors.Is(client.failure(), dgruntime.ErrProtocol) {
		t.Fatal("unbounded session identity retention")
	}
	client, _ = queueFixture()
	client.sequential = true
	client.seenRequests = make(map[string]struct{})
	encoded, _ := json.Marshal(strings.Repeat("x", 257))
	if client.registerSessionRequest(wireMessage{ID: encoded}) {
		t.Fatal("unbounded native identity accepted")
	}
	session := &Session{transport: &connection{done: make(chan struct{})}, turns: maximumSessionTurns}
	if _, err := session.StartTurn(context.Background(), dgruntime.StartRequest{Prompt: "next"}); !errors.Is(err, dgruntime.ErrSessionLimit) {
		t.Fatalf("turn limit = %v", err)
	}
}

func TestSessionTurnCloseDoesNotWaitForNativeDispatch(t *testing.T) {
	client, native := queueFixture()
	client.sequential = true
	client.connection.active = client
	client.dispatchMu.Lock()
	defer client.dispatchMu.Unlock()
	closed := make(chan struct{})
	go func() { _ = client.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close waited for a blocked native handler")
	}
	if !native.closed.Load() {
		t.Fatal("active turn did not release transport")
	}
}

func TestSessionWaitsForRetiredApprovalWrite(t *testing.T) {
	client, _ := queueFixture()
	client.sequential = true
	client.connection.active = client
	client.approvalMode = dgruntime.ApprovalInteractive
	client.approvals = map[string]approval{"approval-1": {requestID: json.RawMessage(`101`), method: "item/fileChange/requestApproval", nativeKey: "integer:101"}}
	client.nativeRequests = map[string]struct{}{"integer:101": {}}
	client.writeMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- client.ResolveApproval(ctx, "approval-1", dgruntime.ApprovalApprove) }()
	for {
		client.stateMu.Lock()
		inFlight := client.inFlightCommands
		client.stateMu.Unlock()
		if inFlight == 1 {
			break
		}
		if ctx.Err() != nil {
			client.writeMu.Unlock()
			t.Fatal("approval never reached writer")
		}
		runtime.Gosched()
	}
	client.handleTurnCompleted(wireMessage{Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`)})
	<-client.events
	request, err := normalizeStartRequest(dgruntime.StartRequest{Prompt: "next", ApprovalMode: dgruntime.ApprovalInteractive})
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{transport: client.connection, current: client, options: request, turns: 1}
	if _, err := session.StartTurn(ctx, request); !errors.Is(err, dgruntime.ErrConcurrentTurn) {
		client.writeMu.Unlock()
		t.Fatalf("next turn admitted before write retired: %v", err)
	}
	client.writeMu.Unlock()
	if err := <-result; !errors.Is(err, dgruntime.ErrApprovalNotFound) {
		t.Fatalf("retired approval write = %v", err)
	}
	client.stateMu.Lock()
	defer client.stateMu.Unlock()
	if client.inFlightCommands != 0 {
		t.Fatal("interaction ownership was not released")
	}
}
