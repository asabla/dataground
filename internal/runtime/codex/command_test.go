package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
)

func nativeCommandParams(output any) map[string]any {
	return map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{"id": "native-command", "type": "commandExecution", "status": "completed", "aggregatedOutput": output, "exitCode": 0, "command": "private-command", "cwd": "/private/path", "processId": "private-process"}}
}

func TestCompletedCommandPreservesSnapshotAndSuppressesReplay(t *testing.T) {
	for _, output := range []any{nil, "", strings.Repeat("界", 30000)} {
		t.Run("snapshot", func(t *testing.T) {
			session := newScriptedSession(t, func(server *scriptServer) {
				server.completeStart("thread", "turn")
				params := nativeCommandParams(output)
				if output == nil {
					params["item"].(map[string]any)["status"] = "declined"
				}
				server.notify("item/completed", params)
				server.notify("item/completed", params)
				server.notify("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "completed"}})
				server.notify("item/completed", nativeCommandParams("late"))
			})
			client, err := codex.New(session)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "command"})
			if err != nil {
				t.Fatal(err)
			}
			if err := turn.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			snapshots, completions := 0, 0
			for {
				select {
				case event := <-turn.Events():
					encoded, _ := json.Marshal(event)
					if strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "native-command") || strings.Contains(string(encoded), "/private/path") {
						t.Fatal("native metadata escaped")
					}
					if event.Type == dgruntime.CommandCompletedEvent {
						snapshots++
						if event.Payload["text"] != output {
							t.Fatal("snapshot changed")
						}
					}
					if event.Type == "activity.process.completed" {
						completions++
						if output == nil && event.Payload["status"] != "denied" {
							t.Fatal("native denial lost")
						}
						if event.Payload["outputAvailable"] != (output != nil) {
							t.Fatal("output availability changed")
						}
					}
				default:
					if completions != 1 || (output == nil && snapshots != 0) || (output != nil && snapshots != 1) {
						t.Fatal(completions, snapshots)
					}
					return
				}
			}
		})
	}
}

func TestCompletedCommandRejectsInvalidSnapshot(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"foreign thread": func(p map[string]any) { p["threadId"] = "other" },
		"foreign turn":   func(p map[string]any) { p["turnId"] = "other" },
		"missing id":     func(p map[string]any) { delete(p["item"].(map[string]any), "id") },
		"oversized id":   func(p map[string]any) { p["item"].(map[string]any)["id"] = strings.Repeat("x", 1025) },
		"invalid output": func(p map[string]any) { p["item"].(map[string]any)["aggregatedOutput"] = 42 },
		"oversized output": func(p map[string]any) {
			p["item"].(map[string]any)["aggregatedOutput"] = strings.Repeat("x", dgruntime.MaximumCommandOutputBytes+1)
		},
		"nonterminal status":    func(p map[string]any) { p["item"].(map[string]any)["status"] = "inProgress" },
		"fractional exit":       func(p map[string]any) { p["item"].(map[string]any)["exitCode"] = 0.5 },
		"conflict status":       func(p map[string]any) { p["item"].(map[string]any)["status"] = "failed" },
		"conflict exit":         func(p map[string]any) { p["item"].(map[string]any)["exitCode"] = 2 },
		"conflict availability": func(p map[string]any) { p["item"].(map[string]any)["aggregatedOutput"] = nil },
		"conflict":              func(p map[string]any) { p["item"].(map[string]any)["aggregatedOutput"] = "changed" },
	} {
		t.Run(name, func(t *testing.T) {
			session := newScriptedSession(t, func(server *scriptServer) {
				server.completeStart("thread", "turn")
				if strings.HasPrefix(name, "conflict") {
					server.notify("item/completed", nativeCommandParams("original"))
				}
				p := nativeCommandParams("original")
				mutate(p)
				server.notify("item/completed", p)
			})
			client, err := codex.New(session)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "command"})
			if err != nil {
				t.Fatal(err)
			}
			if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrProtocol) {
				t.Fatal("invalid command accepted", err)
			}
		})
	}
}
