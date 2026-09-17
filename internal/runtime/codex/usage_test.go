package codex_test

import (
	"context"
	"errors"
	"testing"
	"time"

	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
	"github.com/asabla/dataground/internal/runtime/runtimetest"
)

func nativeUsageParams(threadID, turnID string, input, output, total int) map[string]any {
	return map[string]any{"threadId": threadID, "turnId": turnID, "tokenUsage": map[string]any{
		"total":              map[string]any{"inputTokens": input, "outputTokens": output, "totalTokens": total, "cachedInputTokens": 0, "reasoningOutputTokens": 0, "private": runtimetest.NativeCanary + "detail"},
		"last":               map[string]any{"inputTokens": 1, "outputTokens": 0, "totalTokens": 1, "cachedInputTokens": 0, "reasoningOutputTokens": 0},
		"modelContextWindow": 100000,
	}}
}
func TestUsageRejectsInvalidCountsAndForeignScope(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"foreign thread": func(p map[string]any) { p["threadId"] = "other" },
		"foreign turn":   func(p map[string]any) { p["turnId"] = "other" },
		"absent total":   func(p map[string]any) { delete(p["tokenUsage"].(map[string]any), "total") },
		"absent last":    func(p map[string]any) { delete(p["tokenUsage"].(map[string]any), "last") },
		"missing count": func(p map[string]any) {
			delete(p["tokenUsage"].(map[string]any)["total"].(map[string]any), "inputTokens")
		},
		"negative count": func(p map[string]any) { p["tokenUsage"].(map[string]any)["total"].(map[string]any)["inputTokens"] = -1 },
		"null count": func(p map[string]any) {
			p["tokenUsage"].(map[string]any)["total"].(map[string]any)["outputTokens"] = nil
		},
		"fractional count": func(p map[string]any) {
			p["tokenUsage"].(map[string]any)["total"].(map[string]any)["totalTokens"] = 1.5
		},
		"unsafe integer": func(p map[string]any) {
			p["tokenUsage"].(map[string]any)["total"].(map[string]any)["totalTokens"] = int64(9007199254740992)
		},
		"overflow": func(p map[string]any) {
			p["tokenUsage"].(map[string]any)["total"].(map[string]any)["totalTokens"] = uint64(1 << 63)
		},
		"invalid nested count": func(p map[string]any) {
			p["tokenUsage"].(map[string]any)["last"].(map[string]any)["cachedInputTokens"] = -1
		},
	} {
		t.Run(name, func(t *testing.T) {
			session := newScriptedSession(t, func(server *scriptServer) {
				server.completeStart("thread", "turn")
				params := nativeUsageParams("thread", "turn", 12, 8, 20)
				change(params)
				server.notify("thread/tokenUsage/updated", params)
			})
			client, err := codex.New(session)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "usage"})
			if err != nil {
				t.Fatal(err)
			}
			if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrProtocol) {
				t.Fatal("invalid usage did not fail closed", err)
			}
			for {
				select {
				case event := <-turn.Events():
					if event.Type == "usage.recorded" {
						t.Fatal("invalid usage escaped")
					}
				default:
					return
				}
			}
		})
	}
}
