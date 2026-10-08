package runtime_test

import (
	"encoding/json"
	"strings"
	"testing"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestCommandContracts(t *testing.T) {
	code := int32(-1)
	for _, status := range []string{"completed", "failed", "denied"} {
		value := dgruntime.CompletedCommand{Text: "exact\x00output", Status: status, ExitCode: &code}
		parsed, err := dgruntime.ParseCompletedCommand(value.Payload())
		if err != nil || parsed.Text != value.Text || *parsed.ExitCode != code {
			t.Fatal(parsed, err)
		}
		ref := dgruntime.CommandArtifact{ArtifactID: "art_" + strings.Repeat("a", 20), Digest: "sha256:" + strings.Repeat("a", 64), SizeBytes: int64(len(value.Text)), Status: status, ExitCode: &code, Preview: dgruntime.CommandPreview(value.Text)}
		encoded, _ := json.Marshal(ref.Payload())
		var decoded map[string]any
		_ = json.Unmarshal(encoded, &decoded)
		if _, err := dgruntime.ParseCommandArtifact(decoded); err != nil {
			t.Fatal(err)
		}
	}
	for name, mutate := range map[string]func(map[string]any){
		"oversized":       func(p map[string]any) { p["text"] = strings.Repeat("x", dgruntime.MaximumCommandOutputBytes+1) },
		"invalid utf8":    func(p map[string]any) { p["text"] = string([]byte{0xff}) },
		"null text":       func(p map[string]any) { p["text"] = nil },
		"extra":           func(p map[string]any) { p["nativeId"] = "private" },
		"missing exit":    func(p map[string]any) { delete(p, "exitCode") },
		"fractional exit": func(p map[string]any) { p["exitCode"] = 0.5 },
		"overflow exit":   func(p map[string]any) { p["exitCode"] = int64(1) << 32 },
		"unknown status":  func(p map[string]any) { p["status"] = "inProgress" },
	} {
		t.Run(name, func(t *testing.T) {
			p := dgruntime.CompletedCommand{Text: "x", Status: "completed"}.Payload()
			mutate(p)
			if _, err := dgruntime.ParseCompletedCommand(p); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	for name, mutate := range map[string]func(map[string]any){
		"extra":             func(p map[string]any) { p["nativeId"] = "private" },
		"missing exit":      func(p map[string]any) { delete(p, "exitCode") },
		"null size":         func(p map[string]any) { p["sizeBytes"] = nil },
		"negative size":     func(p map[string]any) { p["sizeBytes"] = -1 },
		"oversized size":    func(p map[string]any) { p["sizeBytes"] = dgruntime.MaximumCommandOutputBytes + 1 },
		"oversized preview": func(p map[string]any) { p["preview"] = strings.Repeat("x", 1025) },
		"invalid preview":   func(p map[string]any) { p["preview"] = string([]byte{0xff}) },
		"unknown status":    func(p map[string]any) { p["status"] = "inProgress" },
	} {
		t.Run("reference "+name, func(t *testing.T) {
			p := dgruntime.CommandArtifact{ArtifactID: "art_" + strings.Repeat("a", 20), Digest: "sha256:" + strings.Repeat("a", 64), Status: "completed"}.Payload()
			mutate(p)
			if _, err := dgruntime.ParseCommandArtifact(p); err == nil {
				t.Fatal("invalid reference accepted")
			}
		})
	}
}

func TestCommandPreviewPreservesNULInContentOnly(t *testing.T) {
	if got := dgruntime.CommandPreview("before\x00after"); got != `before\u0000after` {
		t.Fatal(got)
	}
	if got := dgruntime.CommandPreview(strings.Repeat("\x00", 2000)); len(got) > dgruntime.MaximumMessagePreviewBytes || strings.ContainsRune(got, 0) {
		t.Fatal("unbounded or nonpersistable preview")
	}
}
