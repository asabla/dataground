package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/codex"
)

func TestLargeNativeOutputFailsClosedWhenTheConsumerStops(t *testing.T) {
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	session := newScriptedSession(t, func(server *scriptServer) {
		server.completeStart("thread", "turn")
		<-release
		text := strings.Repeat("x", dgruntime.MaximumMessageTextBytes)
		for i := 0; i < 20; i++ {
			frame, _ := json.Marshal(map[string]any{"method": "item/completed", "params": nativeMessageParams("thread", "turn", fmt.Sprintf("native-%d", i), text, "final_answer")})
			if _, err := server.writer.Write(append(frame, '\n')); err != nil {
				return
			}
		}
		frame, _ := json.Marshal(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "completed"}}})
		_, _ = server.writer.Write(append(frame, '\n'))
	})
	client, err := codex.New(session)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	turn, err := client.Start(ctx, dgruntime.StartRequest{Prompt: "large output"})
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	// Leave Events unread. A bounded turn must stop instead of claiming success
	// or retaining every large snapshot until the invocation deadline.
	if err := turn.Wait(ctx); !errors.Is(err, dgruntime.ErrProtocol) {
		t.Fatal("stalled event consumer did not stop the turn", err)
	}
	if err := session.scriptError(); err != nil {
		t.Fatal(err)
	}
}
