package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

type approvalWire struct {
	mu     sync.Mutex
	writes int
}

func (wire *approvalWire) Write(value []byte) (int, error) {
	wire.mu.Lock()
	defer wire.mu.Unlock()
	wire.writes++
	return len(value), nil
}
func (*approvalWire) Close() error { return nil }

var _ io.WriteCloser = (*approvalWire)(nil)

func TestQueuedApprovalRechecksTurnBeforeWriting(t *testing.T) {
	for _, boundary := range []string{"interruption", "completion", "native-clearance"} {
		t.Run(boundary, func(t *testing.T) {
			wire := &approvalWire{}
			client := &Client{
				input: wire, threadID: "thread", turnID: "turn",
				approvals: map[string]approval{
					"approval-1": {
						requestID: json.RawMessage(`"native-request"`),
						method:    "item/fileChange/requestApproval", nativeKey: "string:native-request",
					},
				},
				nativeRequests: map[string]struct{}{"string:native-request": {}},
				events:         make(chan dgruntime.Event, 4), done: make(chan struct{}),
				closed: make(chan struct{}), terminalDone: make(chan struct{}),
			}
			// Hold the transport busy while a human decision is accepted locally.
			client.writeMu.Lock()
			result := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			go func() { result <- client.ResolveApproval(ctx, "approval-1", dgruntime.ApprovalApprove) }()
			for {
				client.stateMu.Lock()
				queued := client.approvals["approval-1"].resolving
				client.stateMu.Unlock()
				if queued {
					break
				}
				if ctx.Err() != nil {
					client.writeMu.Unlock()
					t.Fatal("approval did not reach the writer")
				}
				runtime.Gosched()
			}
			if boundary == "completion" {
				client.handleTurnCompleted(wireMessage{Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`)})
			} else if boundary == "native-clearance" {
				client.handleResolvedRequest(wireMessage{Params: json.RawMessage(`{"threadId":"thread","requestId":"native-request"}`)})
			} else {
				client.stateMu.Lock()
				client.closeInteractionsLocked()
				client.stateMu.Unlock()
			}
			client.writeMu.Unlock()
			if err := <-result; !errors.Is(err, dgruntime.ErrApprovalNotFound) {
				t.Fatalf("stale queued approval write: %v", err)
			}
			wire.mu.Lock()
			writes := wire.writes
			wire.mu.Unlock()
			if writes != 0 {
				t.Fatal("stale approval reached native transport")
			}
			client.stateMu.Lock()
			remaining := len(client.approvals)
			client.stateMu.Unlock()
			if remaining != 0 {
				t.Fatal("failed write restored an expired approval")
			}
			select {
			case <-client.done:
				t.Fatal("local rejection poisoned the native transport")
			default:
			}
		})
	}
}

func TestNativeRequestIdentityMatchesPinnedStringOrInt64Contract(t *testing.T) {
	for _, pair := range [][2]string{{`"native-request"`, `"native-\u0072equest"`}, {`0`, `-0`}, {`9223372036854775807`, `9223372036854775807`}} {
		left, validLeft := nativeRequestKey(json.RawMessage(pair[0]))
		right, validRight := nativeRequestKey(json.RawMessage(pair[1]))
		if !validLeft || !validRight || left != right {
			t.Fatal("equivalent request IDs did not share identity")
		}
	}
	text, _ := nativeRequestKey(json.RawMessage(`"1"`))
	number, _ := nativeRequestKey(json.RawMessage(`1`))
	if text == number {
		t.Fatal("text and integer request IDs collided")
	}
	for _, raw := range []string{`null`, `1.5`, `true`, `{}`, `[]`, `9223372036854775808`, `"unterminated`, ``} {
		if validRequestID(json.RawMessage(raw)) {
			t.Fatalf("accepted invalid pinned request ID: %s", raw)
		}
	}
}
