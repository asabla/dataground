package codex_test

import (
	"context"
	"encoding/base64"
	"errors"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
	"github.com/asabla/dataground/internal/runtime/runtimetest"
	"strings"
	"testing"
	"time"
)

func nativeImageParams() map[string]any {
	return map[string]any{"threadId": "thread", "turnId": "turn", "item": map[string]any{"id": "native-image", "type": "imageGeneration", "status": "completed", "result": runtimetest.ImageData}}
}
func TestCompletedImageRejectsMalformedOrChangedNativeOutput(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"scope": func(p map[string]any) { p["turnId"] = "other" }, "missing-id": func(p map[string]any) { delete(p["item"].(map[string]any), "id") },
		"missing-result": func(p map[string]any) { delete(p["item"].(map[string]any), "result") }, "status": func(p map[string]any) { p["item"].(map[string]any)["status"] = "in_progress" },
		"invalid": func(p map[string]any) { p["item"].(map[string]any)["result"] = "private" }, "path-only": func(p map[string]any) { p["item"].(map[string]any)["result"] = "/private/image.png" },
		"changed": func(p map[string]any) {
			b, _ := base64.StdEncoding.DecodeString(runtimetest.ImageData)
			p["item"].(map[string]any)["result"] = base64.StdEncoding.EncodeToString(append(b, 0))
		},
	} {
		t.Run(name, func(t *testing.T) {
			session := newScriptedSession(t, func(server *scriptServer) {
				server.completeStart("thread", "turn")
				if name == "changed" {
					server.notify("item/completed", nativeImageParams())
				}
				p := nativeImageParams()
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
			turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "image"})
			if err != nil {
				t.Fatal(err)
			}
			if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrProtocol) {
				t.Fatal("invalid image accepted", err)
			}
		})
	}
}
func TestCompletedImageBoundsTotalTurnContent(t *testing.T) {
	session := newScriptedSession(t, func(server *scriptServer) {
		server.completeStart("thread", "turn")
		content, _ := base64.StdEncoding.DecodeString(runtimetest.ImageData)
		content = append(content, make([]byte, dgruntime.MaximumImageBytes-len(content))...)
		for _, id := range []string{"one", "two", "three", "four", "five"} {
			p := nativeImageParams()
			p["item"].(map[string]any)["id"] = id
			p["item"].(map[string]any)["result"] = base64.StdEncoding.EncodeToString(content)
			server.notify("item/completed", p)
		}
	})
	client, err := codex.New(session)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "image"})
	if err != nil {
		t.Fatal(err)
	}
	if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrProtocol) {
		t.Fatal("turn image limit was not enforced", err)
	}
	events := collectEvents(t, turn.Events(), 5)
	for _, event := range events {
		if text, _ := event.Payload["data"].(string); strings.Contains(text, "private") {
			t.Fatal("native metadata leaked")
		}
	}
}
