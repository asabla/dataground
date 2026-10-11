package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

func toolOutputFixture() CompletedToolOutput {
	return CompletedToolOutput{SchemaVersion: ToolOutputSchemaV1, Status: "failed", Content: []any{map[string]any{"type": "text", "text": "result\x00界"}}, StructuredContent: map[string]any{"count": json.Number("9007199254740993")}}
}
func TestCompletedToolOutputRetainsDataAndRejectsMalformed(t *testing.T) {
	value, err := ParseCompletedToolOutput(toolOutputFixture().Payload())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(value)
	if !strings.Contains(string(encoded), "9007199254740993") {
		t.Fatal("structured number rounded")
	}
	for name, mutate := range map[string]func(map[string]any){
		"error-status": func(p map[string]any) { p["status"] = "completed"; p["error"] = "failure" },
		"version":      func(p map[string]any) { p["schemaVersion"] = "future" }, "status": func(p map[string]any) { p["status"] = "inProgress" }, "extra": func(p map[string]any) { p["server"] = "native" },
		"null-content": func(p map[string]any) { p["content"] = nil }, "content-limit": func(p map[string]any) { p["content"] = make([]any, 257) }, "missing": func(p map[string]any) { delete(p, "structuredContent") },
		"invalid-string": func(p map[string]any) { p["content"] = []any{string([]byte{0xff})} }, "invalid-key": func(p map[string]any) { p["structuredContent"] = map[string]any{string([]byte{0xff}): 1} },
		"too-large": func(p map[string]any) { p["content"] = []any{strings.Repeat("x", MaximumToolOutputBytes)} }, "escaped-limit": func(p map[string]any) { p["content"] = []any{strings.Repeat("\x00", 200000)} },
		"cycle": func(p map[string]any) {
			value := map[string]any{}
			value["self"] = value
			p["structuredContent"] = value
		},
		"depth": func(p map[string]any) {
			var v any = "value"
			for i := 0; i < 65; i++ {
				v = []any{v}
			}
			p["structuredContent"] = v
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := toolOutputFixture().Payload()
			mutate(p)
			if _, err := ParseCompletedToolOutput(p); err == nil {
				t.Fatal("malformed tool output accepted")
			}
		})
	}
	empty := CompletedToolOutput{SchemaVersion: ToolOutputSchemaV1, Status: "completed", Content: []any{}}
	if _, err := ParseCompletedToolOutput(empty.Payload()); err != nil {
		t.Fatal("empty returned content rejected", err)
	}
}
func TestToolArtifactContract(t *testing.T) {
	value := ToolArtifact{ArtifactID: "art_" + strings.Repeat("a", 20), Digest: "sha256:" + strings.Repeat("a", 64), SizeBytes: 100, Status: "failed", ContentCount: 1, Preview: ToolOutputPreview}
	if _, err := ParseToolArtifact(value.Payload()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){"extra": func(p map[string]any) { p["server"] = "private" }, "null-count": func(p map[string]any) { p["contentCount"] = nil }, "fraction": func(p map[string]any) { p["contentCount"] = 1.5 }, "oversize": func(p map[string]any) { p["sizeBytes"] = MaximumToolOutputBytes + 1 }, "status": func(p map[string]any) { p["status"] = "unknown" }, "preview": func(p map[string]any) { p["preview"] = "content" }} {
		t.Run(name, func(t *testing.T) {
			p := value.Payload()
			mutate(p)
			if _, err := ParseToolArtifact(p); err == nil {
				t.Fatal("invalid reference accepted")
			}
		})
	}
}
