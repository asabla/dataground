package codex

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

type queueSession struct {
	execution.RuntimeSession
	closed atomic.Bool
}

func (session *queueSession) Close() error { session.closed.Store(true); return nil }
func queueFixture() (*Client, *queueSession) {
	session := &queueSession{}
	return &Client{input: &approvalWire{}, session: session, inbound: make(chan wireMessage, inboundLimit), events: make(chan dgruntime.Event, eventLimit), done: make(chan struct{}), closed: make(chan struct{}), terminalDone: make(chan struct{}), threadID: "thread", turnID: "turn"}, session
}
func TestEventQueueFailsPromptlyOnBytesAndCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
	}{{"count", 1}, {"bytes", 1 << 20}} {
		t.Run(tc.name, func(t *testing.T) {
			size := tc.size
			client, session := queueFixture()
			text := strings.Repeat("x", size)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for i := 0; i <= eventLimit; i++ {
					if !client.emit("output.text.delta", map[string]any{"text": text}) {
						return
					}
				}
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				client.Close()
				t.Fatal("stalled consumer blocked the producer")
			}
			if !errors.Is(client.failure(), dgruntime.ErrProtocol) || !session.closed.Load() {
				t.Fatal("queue exhaustion did not close the native transport")
			}
			if client.eventBytes > eventByteLimit {
				t.Fatal("queued bytes exceeded the limit")
			}
			if size > 1 && len(client.events) >= eventLimit {
				t.Fatal("byte budget was not applied before the item limit")
			}
		})
	}
}
func TestEventQueueRejectsAnOversizedEncodedEvent(t *testing.T) {
	client, session := queueFixture()
	// JSON escaping expands this sub-frame raw string beyond the event limit.
	text := strings.Repeat("\x00", 2<<20)
	if client.emit("output.message.completed", map[string]any{"text": text}) {
		t.Fatal("oversized encoded event was accepted")
	}
	if len(client.events) != 0 || client.eventBytes != 0 || client.nextSequence != 0 {
		t.Fatal("rejected event changed the accepted backlog")
	}
	if !session.closed.Load() || !errors.Is(client.failure(), dgruntime.ErrProtocol) {
		t.Fatal("oversized event did not close the transport")
	}
}

func TestEventQueueReclaimsConsumedBytesAndPreservesSequence(t *testing.T) {
	client, _ := queueFixture()
	text := strings.Repeat("x", 1<<20)
	for i := uint64(1); i <= 40; i++ {
		if !client.emit("output.message.completed", map[string]any{"text": text, "phase": "final"}) {
			t.Fatal("consumed bytes were not reclaimed", client.failure())
		}
		event := <-client.events
		if event.Sequence != i || event.Payload["text"] != text {
			t.Fatal("ordered content changed")
		}
	}
	if client.eventBytes > 2<<20 {
		t.Fatal("byte accounting accumulated consumed content")
	}
}
func TestConcurrentEventProducersStayOrdered(t *testing.T) {
	client, _ := queueFixture()
	var producers sync.WaitGroup
	for i := 0; i < 128; i++ {
		producers.Add(1)
		go func() {
			defer producers.Done()
			client.emit("activity.tool.completed", map[string]any{"status": "completed"})
		}()
	}
	producers.Wait()
	for sequence := uint64(1); sequence <= 128; sequence++ {
		event := <-client.events
		if event.Sequence != sequence {
			t.Fatal("event order diverged from sequence", event.Sequence, sequence)
		}
	}
}
func TestTerminalEventOverflowCannotReportSuccess(t *testing.T) {
	client, _ := queueFixture()
	for i := 0; i < eventLimit; i++ {
		if !client.emit("output.text.delta", map[string]any{"text": "x"}) {
			t.Fatal("fixture filled early")
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.handleTurnCompleted(wireMessage{Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`)})
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		client.Close()
		t.Fatal("terminal event blocked on a full queue")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Wait(ctx); !errors.Is(err, dgruntime.ErrProtocol) {
		t.Fatal("lost terminal event reported completion", err)
	}
	select {
	case <-client.terminalDone:
		t.Fatal("terminal receipt accepted without its event")
	default:
	}
}
func TestSuccessfulWaitKeepsTheCompleteEventBacklog(t *testing.T) {
	client, _ := queueFixture()
	client.emit("output.text.delta", map[string]any{"text": "first"})
	client.emit("output.text.delta", map[string]any{"text": "second"})
	client.handleTurnCompleted(wireMessage{Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`)})
	if err := client.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= 3; sequence++ {
		select {
		case event := <-client.events:
			if event.Sequence != sequence {
				t.Fatal("reordered backlog")
			}
			if sequence == 3 && event.Type != "lifecycle.succeeded" {
				t.Fatal("missing terminal event")
			}
		default:
			t.Fatal("successful wait left an undelivered event")
		}
	}
}
func TestInboundQueueBoundsBytesAndReleasesHandledFrames(t *testing.T) {
	client, session := queueFixture()
	params, _ := json.Marshal(map[string]any{"private": strings.Repeat("x", 6<<20)})
	frame, _ := json.Marshal(map[string]any{"method": "extension/ignored", "params": json.RawMessage(params)})
	client.readLoop(strings.NewReader(strings.Repeat(string(frame)+"\n", 3)))
	if !session.closed.Load() || !errors.Is(client.failure(), dgruntime.ErrProtocol) || !strings.Contains(client.failure().Error(), "byte budget") {
		t.Fatal("native backlog did not fail at its byte bound")
	}
	if len(client.inbound) != 2 || client.inboundBytes.Load() > inboundByteLimit {
		t.Fatal("unbounded native backlog")
	}
	for len(client.inbound) > 0 {
		client.handleInbound(<-client.inbound)
	}
	if client.inboundBytes.Load() != 0 {
		t.Fatal("handled frames retained their byte reservation")
	}
}
func TestInboundByteReservationsRemainBoundedUnderConcurrentRelease(t *testing.T) {
	client, _ := queueFixture()
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				if client.reserveInbound(1 << 20) {
					if client.inboundBytes.Load() > inboundByteLimit {
						t.Error("reservation exceeded budget")
					}
					client.inboundBytes.Add(-(1 << 20))
				}
			}
		}()
	}
	workers.Wait()
	if client.inboundBytes.Load() != 0 {
		t.Fatal("reservation leaked")
	}
}
