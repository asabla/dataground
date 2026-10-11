package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
	"strings"
	"testing"
	"time"
)

func nativeToolParams() map[string]any {
	return map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{"type": "mcpToolCall", "id": "native-tool", "status": "completed", "server": "private-server", "tool": "private-tool", "arguments": map[string]any{"secret": "private-input"}, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "result\x00界"}}, "structuredContent": map[string]any{"count": json.Number("9007199254740993")}}}}
}
func TestCompletedToolOutputNormalizesResultsAndSuppressesReplay(t *testing.T) {
	for _, kind := range []string{"mcp", "mcp-error", "mcp-error-only", "dynamic", "absent", "empty"} {
		t.Run(kind, func(t *testing.T) {
			session := newScriptedSession(t, func(server *scriptServer) {
				server.completeStart("thread", "turn")
				p := nativeToolParams()
				item := p["item"].(map[string]any)
				switch kind {
				case "mcp-error", "mcp-error-only":
					item["status"] = "failed"
					item["error"] = map[string]any{"message": "tool failed"}
					if kind == "mcp-error-only" {
						delete(item, "result")
					}
				case "dynamic":
					item["type"] = "dynamicToolCall"
					item["success"] = false
					item["contentItems"] = []any{map[string]any{"type": "inputText", "text": "result\x00界"}, map[string]any{"type": "inputImage", "imageUrl": "https://example.invalid/image.png"}}
				case "absent":
					delete(item, "result")
				case "empty":
					item["result"] = map[string]any{"content": []any{}}
				}
				server.notify("item/completed", p)
				server.notify("item/completed", p)
				server.notify("turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "completed"}})
			})
			client, err := codex.New(session)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "tool"})
			if err != nil {
				t.Fatal(err)
			}
			count := 4
			if kind == "absent" {
				count = 3
			}
			events := collectEvents(t, turn.Events(), count)
			if err := turn.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			status := "completed"
			if kind == "mcp-error" || kind == "mcp-error-only" || kind == "dynamic" {
				status = "failed"
			}
			if events[1].Payload["status"] != status {
				t.Fatal("tool outcome lost")
			}
			if kind != "absent" {
				output, err := dgruntime.ParseCompletedToolOutput(events[2].Payload)
				if err != nil || events[2].Type != dgruntime.ToolCompletedEvent || output.Status != status {
					t.Fatal("tool output lost", err)
				}
				if kind == "mcp" && output.StructuredContent.(map[string]any)["count"] != json.Number("9007199254740993") {
					t.Fatal("number rounded")
				}
				if kind == "dynamic" && output.Content[1].(map[string]any)["url"] != "https://example.invalid/image.png" {
					t.Fatal("image reference lost")
				}
			}
			encoded, _ := json.Marshal(events)
			for _, private := range []string{"private-server", "private-tool", "private-input", "native-tool"} {
				if strings.Contains(string(encoded), private) {
					t.Fatal("native metadata leaked")
				}
			}
		})
	}
}
func TestCompletedToolOutputRejectsMalformedOrConflictingInput(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"scope": func(p map[string]any) { p["turnId"] = "other" }, "id": func(p map[string]any) { delete(p["item"].(map[string]any), "id") }, "status": func(p map[string]any) { p["item"].(map[string]any)["status"] = "inProgress" }, "result": func(p map[string]any) { p["item"].(map[string]any)["result"] = map[string]any{} }, "error": func(p map[string]any) { p["item"].(map[string]any)["error"] = map[string]any{"message": "failed"} }, "changed-availability": func(p map[string]any) { delete(p["item"].(map[string]any), "result") },
		"changed-status": func(p map[string]any) { p["item"].(map[string]any)["status"] = "failed" },
		"changed-type":   func(p map[string]any) { p["item"].(map[string]any)["type"] = "dynamicToolCall" },
		"changed": func(p map[string]any) {
			p["item"].(map[string]any)["result"] = map[string]any{"content": []any{"changed"}}
		}, "dynamic-kind": func(p map[string]any) {
			i := p["item"].(map[string]any)
			i["type"] = "dynamicToolCall"
			i["contentItems"] = []any{map[string]any{"type": "unknown"}}
		}, "dynamic-conflict": func(p map[string]any) {
			i := p["item"].(map[string]any)
			i["type"] = "dynamicToolCall"
			i["status"] = "failed"
			i["success"] = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			session := newScriptedSession(t, func(server *scriptServer) {
				server.completeStart("thread", "turn")
				if strings.HasPrefix(name, "changed") {
					server.notify("item/completed", nativeToolParams())
				}
				p := nativeToolParams()
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
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "tool"})
			if err != nil {
				t.Fatal(err)
			}
			if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrProtocol) {
				t.Fatal("invalid tool output accepted", err)
			}
		})
	}
}
