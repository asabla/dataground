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

func nativePatchParams() map[string]any {
	return map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{"id": "native-patch", "type": "fileChange", "status": "completed", "changes": []any{map[string]any{"path": "/sandbox/file.txt", "kind": map[string]any{"type": "update", "move_path": "/sandbox/renamed.txt"}, "diff": "-old\n+new\x00界\n"}}}}
}
func TestCompletedPatchNormalizesPathsAndSuppressesReplay(t *testing.T) {
	for _, status := range []string{"completed", "failed", "declined"} {
		t.Run(status, func(t *testing.T) {
			session := newScriptedSession(t, func(server *scriptServer) {
				server.completeStart("thread", "turn")
				p := nativePatchParams()
				p["item"].(map[string]any)["status"] = status
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
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "patch"})
			if err != nil {
				t.Fatal(err)
			}
			events := collectEvents(t, turn.Events(), 4)
			if err := turn.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			patch, err := dgruntime.ParseCompletedPatch(events[2].Payload)
			if err != nil || events[2].Type != dgruntime.PatchCompletedEvent || patch.Changes[0].Path != "file.txt" || *patch.Changes[0].MovePath != "renamed.txt" || patch.Changes[0].Diff != "-old\n+new\x00界\n" {
				t.Fatal(patch, err)
			}
			want := status
			if want == "declined" {
				want = "denied"
			}
			if patch.Status != want {
				t.Fatal("outcome changed")
			}
			encoded, _ := json.Marshal(events)
			if strings.Contains(string(encoded), "native-patch") || strings.Contains(string(encoded), "/sandbox") {
				t.Fatal("native metadata escaped")
			}
		})
	}
}
func TestCompletedPatchRejectsMalformedAndConflictingInput(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"scope":           func(p map[string]any) { p["turnId"] = "other" },
		"missing id":      func(p map[string]any) { delete(p["item"].(map[string]any), "id") },
		"missing changes": func(p map[string]any) { delete(p["item"].(map[string]any), "changes") },
		"null changes":    func(p map[string]any) { p["item"].(map[string]any)["changes"] = nil },
		"missing diff":    func(p map[string]any) { delete(patchChange(p), "diff") },
		"outside root":    func(p map[string]any) { patchChange(p)["path"] = "/sandbox-other/file.txt" },
		"traversal":       func(p map[string]any) { patchChange(p)["path"] = "/sandbox/../escape" },
		"outside move":    func(p map[string]any) { patchChange(p)["kind"].(map[string]any)["move_path"] = "/other/file" },
		"unknown kind":    func(p map[string]any) { patchChange(p)["kind"].(map[string]any)["type"] = "other" },
		"conflict diff":   func(p map[string]any) { patchChange(p)["diff"] = "different" },
		"conflict path":   func(p map[string]any) { patchChange(p)["path"] = "/sandbox/other.txt" },
		"conflict status": func(p map[string]any) { p["item"].(map[string]any)["status"] = "failed" },
	} {
		t.Run(name, func(t *testing.T) {
			session := newScriptedSession(t, func(server *scriptServer) {
				server.completeStart("thread", "turn")
				if strings.HasPrefix(name, "conflict") {
					server.notify("item/completed", nativePatchParams())
				}
				p := nativePatchParams()
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
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "patch"})
			if err != nil {
				t.Fatal(err)
			}
			if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrProtocol) {
				t.Fatal("invalid patch accepted", err)
			}
		})
	}
}
func patchChange(p map[string]any) map[string]any {
	return p["item"].(map[string]any)["changes"].([]any)[0].(map[string]any)
}

func TestPatchWorkspaceBindingRejectsChangedOrMalformedRoot(t *testing.T) {
	for _, root := range []string{"/other", "relative", "/sandbox/../other", "/" + strings.Repeat("x", 4096), "/sandbox\n"} {
		t.Run(root, func(t *testing.T) {
			session := newScriptedSession(t, func(server *scriptServer) {
				init := server.read()
				server.respond(init.ID, map[string]any{})
				server.read()
				thread := server.read()
				server.respond(thread.ID, map[string]any{"cwd": root, "thread": map[string]any{"id": "thread"}})
			})
			client, err := codex.New(session)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "patch", WorkingDir: "/sandbox"}); !errors.Is(err, dgruntime.ErrProtocol) {
				t.Fatal("workspace substitution accepted", err)
			}
		})
	}
}
