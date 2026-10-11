package runtime

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func planFixture() PlanSnapshot {
	explanation := "explanation\x00界"
	return PlanSnapshot{SchemaVersion: PlanSchemaV1, Explanation: &explanation, Steps: []PlanStep{{Text: "one\x00界", Status: "completed"}, {Text: "two", Status: "in_progress"}, {Text: "three", Status: "pending"}}}
}
func TestPlanSnapshotPreservesContentAndRejectsInvalid(t *testing.T) {
	want := planFixture()
	got, err := ParsePlanSnapshot(want.Payload())
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("snapshot changed", err)
	}
	encoded, _ := json.Marshal(got)
	var roundtrip PlanSnapshot
	if json.Unmarshal(encoded, &roundtrip) != nil || !reflect.DeepEqual(roundtrip, want) {
		t.Fatal("content was not retained")
	}
	completed, active := got.Counts()
	if completed != 1 || active != 1 {
		t.Fatal("counts changed")
	}
	for name, mutate := range map[string]func(map[string]any){
		"version":             func(p map[string]any) { p["schemaVersion"] = "future" },
		"extra":               func(p map[string]any) { p["threadId"] = "private" },
		"missing-explanation": func(p map[string]any) { delete(p, "explanation") },
		"explanation-type":    func(p map[string]any) { p["explanation"] = 1 },
		"null-steps":          func(p map[string]any) { p["steps"] = nil },
		"step-count":          func(p map[string]any) { p["steps"] = make([]any, 257) },
		"status":              func(p map[string]any) { p["steps"].([]any)[0].(map[string]any)["status"] = "inProgress" },
		"null-text":           func(p map[string]any) { p["steps"].([]any)[0].(map[string]any)["text"] = nil },
		"extra-step":          func(p map[string]any) { p["steps"].([]any)[0].(map[string]any)["command"] = "private" },
		"utf8":                func(p map[string]any) { p["explanation"] = string([]byte{0xff}) },
		"bytes":               func(p map[string]any) { p["explanation"] = strings.Repeat("x", MaximumPlanBytes) },
		"escaping":            func(p map[string]any) { p["explanation"] = strings.Repeat("\x00", 200000) },
	} {
		t.Run(name, func(t *testing.T) {
			p := want.Payload()
			mutate(p)
			if _, err := ParsePlanSnapshot(p); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	if _, err := ParsePlanSnapshot(PlanSnapshot{SchemaVersion: PlanSchemaV1, Steps: []PlanStep{}}.Payload()); err != nil {
		t.Fatal("empty plan rejected", err)
	}
}
func TestPlanArtifactContract(t *testing.T) {
	value := PlanArtifact{ArtifactID: "art_" + strings.Repeat("a", 20), Digest: "sha256:" + strings.Repeat("a", 64), SizeBytes: 100, StepCount: 2, CompletedCount: 1, InProgressCount: 1, Preview: PlanPreview}
	if _, err := ParsePlanArtifact(value.Payload()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"extra": func(p map[string]any) { p["threadId"] = "private" }, "missing": func(p map[string]any) { delete(p, "completedCount") },
		"null": func(p map[string]any) { p["inProgressCount"] = nil }, "fraction": func(p map[string]any) { p["completedCount"] = 0.5 },
		"negative": func(p map[string]any) { p["completedCount"] = -1 }, "count-total": func(p map[string]any) { p["stepCount"] = 1 },
		"count-limit": func(p map[string]any) { p["stepCount"] = 257 }, "bytes": func(p map[string]any) { p["sizeBytes"] = MaximumPlanBytes + 1 },
		"preview": func(p map[string]any) { p["preview"] = "private" },
	} {
		t.Run(name, func(t *testing.T) {
			p := value.Payload()
			mutate(p)
			if _, err := ParsePlanArtifact(p); err == nil {
				t.Fatal("invalid reference accepted")
			}
		})
	}
}
