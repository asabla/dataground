package codex_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
	"github.com/asabla/dataground/internal/runtime/runtimetest"
)

func nativeMessageParams(thread, turn, id, text string, phase any) map[string]any {
	return map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{"type": "agentMessage", "id": id, "text": text, "phase": phase, "memoryCitation": map[string]any{"entries": []any{}, "threadIds": []string{runtimetest.NativeCanary + "citation-thread"}}}}
}

func TestCompletedMessageRejectsMalformedContentAndForeignScope(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"foreign thread": func(p map[string]any) { p["threadId"] = "other" },
		"foreign turn":   func(p map[string]any) { p["turnId"] = "other" },
		"missing text":   func(p map[string]any) { delete(p["item"].(map[string]any), "text") },
		"null text":      func(p map[string]any) { p["item"].(map[string]any)["text"] = nil },
		"invalid text":   func(p map[string]any) { p["item"].(map[string]any)["text"] = 42 },
		"oversized text": func(p map[string]any) {
			p["item"].(map[string]any)["text"] = strings.Repeat("x", dgruntime.MaximumMessageTextBytes+1)
		},
		"missing id":    func(p map[string]any) { delete(p["item"].(map[string]any), "id") },
		"oversized id":  func(p map[string]any) { p["item"].(map[string]any)["id"] = strings.Repeat("x", 1025) },
		"unknown phase": func(p map[string]any) { p["item"].(map[string]any)["phase"] = "future" },
		"invalid phase": func(p map[string]any) { p["item"].(map[string]any)["phase"] = false },
	} {
		t.Run(name, func(t *testing.T) {
			session := newScriptedSession(t, func(server *scriptServer) {
				server.completeStart("thread", "turn")
				params := nativeMessageParams("thread", "turn", "message", "Answer.", "final_answer")
				change(params)
				server.notify("item/completed", params)
			})
			client, err := codex.New(session)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "message"})
			if err != nil {
				t.Fatal(err)
			}
			if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrProtocol) {
				t.Fatal("invalid message did not fail closed", err)
			}
			for {
				select {
				case event := <-turn.Events():
					if event.Type == dgruntime.MessageCompletedEvent {
						t.Fatal("invalid message escaped")
					}
				default:
					return
				}
			}
		})
	}
}

func TestCompletedMessageRejectsConflictingReplay(t *testing.T) {
	session := newScriptedSession(t, func(server *scriptServer) {
		server.completeStart("thread", "turn")
		server.notify("item/completed", nativeMessageParams("thread", "turn", "message", "original", nil))
		server.notify("item/completed", nativeMessageParams("thread", "turn", "message", "replacement", nil))
	})
	client, err := codex.New(session)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "message"})
	if err != nil {
		t.Fatal(err)
	}
	if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrProtocol) {
		t.Fatal("changed completion accepted", err)
	}
}
