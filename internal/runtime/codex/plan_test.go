package codex

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func planMessage(text, status string) wireMessage {
	params, _ := json.Marshal(map[string]any{"threadId": "thread", "turnId": "turn", "explanation": "explanation\x00界", "plan": []any{map[string]any{"step": text, "status": status}}})
	return wireMessage{Method: "turn/plan/updated", Params: params}
}
func TestNativePlanSnapshotsPreserveChangesAndSuppressOnlyAdjacentDuplicates(t *testing.T) {
	client, _ := queueFixture()
	first := planMessage("private\x00界", "pending")
	client.handleNotification(first)
	client.handleNotification(first)
	client.handleNotification(planMessage("private\x00界", "inProgress"))
	client.handleNotification(first)
	client.handleNotification(wireMessage{Method: "turn/plan/updated", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","plan":[]}`)})
	if len(client.events) != 4 || client.planUpdates != 4 {
		t.Fatal("plan revisions were lost or duplicated", len(client.events))
	}
	for i, status := range []string{"pending", "in_progress", "pending", ""} {
		event := <-client.events
		plan, err := dgruntime.ParsePlanSnapshot(event.Payload)
		if err != nil || event.Sequence != uint64(i+1) || event.Type != dgruntime.PlanUpdatedEvent {
			t.Fatal("snapshot contract changed", err)
		}
		if i < 3 && (len(plan.Steps) != 1 || plan.Steps[0].Status != status || plan.Steps[0].Text != "private\x00界") {
			t.Fatal("step changed")
		}
		if i == 3 && len(plan.Steps) != 0 {
			t.Fatal("empty plan did not clear steps")
		}
	}
	close(client.terminalDone)
	client.handleNotification(planMessage("late", "completed"))
	if len(client.events) != 0 {
		t.Fatal("late update changed terminal turn")
	}
}
func TestNativePlanRejectsInvalidScopeContentAndBounds(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"thread": func(p map[string]any) { p["threadId"] = "other" }, "turn": func(p map[string]any) { p["turnId"] = "other" },
		"missing": func(p map[string]any) { delete(p, "plan") }, "null": func(p map[string]any) { p["plan"] = nil },
		"step-missing":     func(p map[string]any) { delete(p["plan"].([]any)[0].(map[string]any), "step") },
		"step-null":        func(p map[string]any) { p["plan"].([]any)[0].(map[string]any)["step"] = nil },
		"status":           func(p map[string]any) { p["plan"].([]any)[0].(map[string]any)["status"] = "in_progress" },
		"count":            func(p map[string]any) { p["plan"] = make([]any, 257) },
		"explanation-type": func(p map[string]any) { p["explanation"] = true },
		"size":             func(p map[string]any) { p["explanation"] = strings.Repeat("x", dgruntime.MaximumPlanBytes) },
	} {
		t.Run(name, func(t *testing.T) {
			client, session := queueFixture()
			message := planMessage("text", "pending")
			var params map[string]any
			_ = json.Unmarshal(message.Params, &params)
			mutate(params)
			message.Params, _ = json.Marshal(params)
			client.handleNotification(message)
			if !errors.Is(client.failure(), dgruntime.ErrProtocol) || !session.closed.Load() || len(client.events) != 0 {
				t.Fatal("invalid plan did not fail closed")
			}
		})
	}
	client, _ := queueFixture()
	client.planUpdates = maximumPlanUpdates
	client.handleNotification(planMessage("new", "pending"))
	if !errors.Is(client.failure(), dgruntime.ErrProtocol) || len(client.events) != 0 {
		t.Fatal("turn update bound not enforced")
	}
}

func TestNativePlanExplanationChangesAreNewUpdates(t *testing.T) {
	client, _ := queueFixture()
	for _, explanation := range []string{``, `,"explanation":null`, `,"explanation":""`, `,"explanation":"revised"`} {
		client.handleNotification(wireMessage{Method: "turn/plan/updated", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","plan":[]` + explanation + `}`)})
	}
	if len(client.events) != 3 {
		t.Fatal("explanation changes lost or duplicate null emitted")
	}
	for i := 0; i < 3; i++ {
		p, err := dgruntime.ParsePlanSnapshot((<-client.events).Payload)
		if err != nil {
			t.Fatal(err)
		}
		if (i == 0 && p.Explanation != nil) || (i == 1 && (p.Explanation == nil || *p.Explanation != "")) || (i == 2 && (p.Explanation == nil || *p.Explanation != "revised")) {
			t.Fatal("explanation changed")
		}
	}
	invalid, _ := queueFixture()
	invalid.handleNotification(wireMessage{Method: "turn/plan/updated", Params: append([]byte(`{"threadId":"thread","turnId":"turn","plan":[],"explanation":"`), append([]byte{0xff}, []byte(`"}`)...)...)})
	if !errors.Is(invalid.failure(), dgruntime.ErrProtocol) || len(invalid.events) != 0 {
		t.Fatal("invalid UTF-8 admitted")
	}
}
