package codex_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/runtime/codex"
	"github.com/asabla/dataground/internal/runtime/runtimetest"
)

func TestSharedAdapterContract(t *testing.T) {
	runtimetest.Run(t, codexContractFixture, runtimetest.Features{Approvals: true, Questions: true})
}

func codexContractFixture(t *testing.T, scenario runtimetest.Scenario) runtimetest.Fixture {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	const threadID = runtimetest.NativeCanary + "thread"
	const turnID = runtimetest.NativeCanary + "turn"
	session := newScriptedSession(t, func(server *scriptServer) {
		server.completeStart(threadID, turnID)
		<-release
		complete := func(status string) {
			server.notify("turn/completed", map[string]any{"threadId": threadID, "turn": map[string]any{"id": turnID, "status": status, "error": map[string]any{"message": runtimetest.NativeCanary + "error"}}})
		}
		switch scenario {
		case runtimetest.CompletedMessages:
			for i, message := range []struct {
				text  string
				phase any
			}{{"Progress.", "commentary"}, {"Legacy answer.", nil}, {runtimetest.OutputText, "final_answer"}} {
				params := nativeMessageParams(threadID, turnID, fmt.Sprintf("%smessage-%d", runtimetest.NativeCanary, i), message.text, message.phase)
				server.notify("item/agentMessage/delta", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": params["item"].(map[string]any)["id"], "delta": "Partial preview."})
				server.notify("item/completed", params)
				server.notify("item/completed", params)
			}
			server.notify("item/completed", nativeMessageParams(threadID, turnID, runtimetest.NativeCanary+"message-1", "Legacy answer.", nil))
			complete("completed")
		case runtimetest.Success:
			server.notify("item/agentMessage/delta", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": runtimetest.NativeCanary + "message", "delta": runtimetest.OutputText})
			for _, kind := range []string{"mcpToolCall", "commandExecution", "fileChange"} {
				for _, state := range []string{"started", "completed"} {
					server.notify("item/"+state, map[string]any{"threadId": threadID, "turnId": turnID, "item": map[string]any{"id": runtimetest.NativeCanary + "item", "type": kind, "command": runtimetest.NativeCanary + "command", "path": runtimetest.NativeCanary + "path"}})
				}
			}
			server.notify("item/completed", nativeMessageParams(threadID, turnID, runtimetest.NativeCanary+"message", runtimetest.OutputText, "final_answer"))
			complete("completed")
		case runtimetest.UsageSnapshots:
			for _, counts := range [][3]int{{12, 8, 20}, {12, 8, 20}, {10, 6, 16}} {
				server.notify("thread/tokenUsage/updated", nativeUsageParams(threadID, turnID, counts[0], counts[1], counts[2]))
			}
			complete("completed")
		case runtimetest.Ownership, runtimetest.Validation:
			complete("completed")
		case runtimetest.Failure:
			complete("failed")
		case runtimetest.Interrupt:
			request := server.read()
			server.requireMethod(request, "turn/interrupt")
			var target map[string]string
			server.decodeParams(request, &target)
			if !reflect.DeepEqual(target, map[string]string{"threadId": threadID, "turnId": turnID}) {
				t.Fatal("interrupt changed its native target")
			}
			server.respond(request.ID, map[string]any{})
			complete("interrupted")
		case runtimetest.ProtocolFailure:
			server.writeRaw([]byte("{invalid-json}\n"))
		case runtimetest.ScopeViolation:
			server.notify("item/agentMessage/delta", map[string]any{"threadId": threadID + "other", "turnId": turnID, "delta": runtimetest.NativeCanary + "output"})
		case runtimetest.ProcessFailure:
			// The session wrapper reports an independent process failure after release.
		case runtimetest.Approve, runtimetest.Deny:
			requestID := runtimetest.NativeCanary + "approval"
			server.request(requestID, "item/commandExecution/requestApproval", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": runtimetest.NativeCanary + "item", "command": runtimetest.NativeCanary + "command"})
			response := server.read()
			expectedID, _ := json.Marshal(requestID)
			var decision struct {
				Decision string `json:"decision"`
			}
			if json.Unmarshal(response.Result, &decision) != nil || string(response.ID) != string(expectedID) || response.Error != nil {
				t.Fatal("approval response lost its native binding")
			}
			want := "decline"
			if scenario == runtimetest.Approve {
				want = "accept"
			}
			if decision.Decision != want {
				t.Fatal("approval decision changed")
			}
			complete("completed")
		case runtimetest.Question:
			params := nativeQuestionParams()
			params["threadId"], params["turnId"], params["itemId"] = threadID, turnID, runtimetest.NativeCanary+"item"
			prompts := params["questions"].([]map[string]any)
			prompts[0]["id"], prompts[1]["id"] = runtimetest.NativeCanary+"choice", runtimetest.NativeCanary+"context"
			requestID := runtimetest.NativeCanary + "question"
			server.request(requestID, "item/tool/requestUserInput", params)
			response := server.read()
			expectedID, _ := json.Marshal(requestID)
			var answer struct {
				Answers map[string]struct {
					Answers []string `json:"answers"`
				} `json:"answers"`
			}
			if json.Unmarshal(response.Result, &answer) != nil || string(response.ID) != string(expectedID) || response.Error != nil || len(answer.Answers) != 2 || !reflect.DeepEqual(answer.Answers[runtimetest.NativeCanary+"choice"].Answers, []string{"Local"}) || !reflect.DeepEqual(answer.Answers[runtimetest.NativeCanary+"context"].Answers, []string{"Explicit context"}) {
				t.Fatal("question response changed native binding or frozen answers")
			}
			complete("completed")
		default:
			t.Fatal("unsupported Codex contract fixture")
		}
	})
	var native execution.RuntimeSession = session
	if scenario == runtimetest.ProcessFailure {
		native = &contractFailedSession{scriptedSession: session, release: release}
	}
	client, err := codex.New(native)
	if err != nil {
		t.Fatal(err)
	}
	return runtimetest.Fixture{Adapter: client, Release: unblock, Verify: func(ctx context.Context) error {
		select {
		case err := <-session.scriptDone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
}

type contractFailedSession struct {
	*scriptedSession
	release <-chan struct{}
}

func (session *contractFailedSession) Wait() error {
	select {
	case <-session.release:
		return errors.New(runtimetest.NativeCanary + "process-error")
	case <-session.done:
		return nil
	}
}
