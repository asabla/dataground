package runtime_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func patchFixture() dgruntime.CompletedPatch {
	move := "renamed.txt"
	return dgruntime.CompletedPatch{SchemaVersion: dgruntime.PatchSchemaV1, Status: "completed", Changes: []dgruntime.PatchChange{{Path: "file.txt", Kind: "update", Diff: "-old\n+new\x00界\n", MovePath: &move}}}
}
func TestPatchSnapshotContract(t *testing.T) {
	patch := patchFixture()
	got, err := dgruntime.ParseCompletedPatch(patch.Payload())
	if err != nil || got.Changes[0].Diff != patch.Changes[0].Diff || *got.Changes[0].MovePath != "renamed.txt" {
		t.Fatal(got, err)
	}
	for _, value := range []string{"", ".", "..", "../escape", "/absolute", "a/../b", "a//b", "a\\b", "a\x00b", "a\nb", string([]byte{0xff})} {
		if dgruntime.ValidPatchPath(value) {
			t.Fatal("unsafe path accepted", value)
		}
	}
	for name, mutate := range map[string]func(map[string]any){
		"version":      func(p map[string]any) { p["schemaVersion"] = "future" },
		"status":       func(p map[string]any) { p["status"] = "inProgress" },
		"null changes": func(p map[string]any) { p["changes"] = nil },
		"extra":        func(p map[string]any) { p["nativeId"] = "private" },
		"unknown kind": func(p map[string]any) { p["changes"].([]any)[0].(map[string]any)["kind"] = "unknown" },
		"invalid text": func(p map[string]any) { p["changes"].([]any)[0].(map[string]any)["diff"] = string([]byte{0xff}) },
		"oversized encoded text": func(p map[string]any) {
			p["changes"].([]any)[0].(map[string]any)["diff"] = strings.Repeat("\x00", 200000)
		},
		"missing move": func(p map[string]any) { delete(p["changes"].([]any)[0].(map[string]any), "movePath") },
		"self move":    func(p map[string]any) { p["changes"].([]any)[0].(map[string]any)["movePath"] = "file.txt" },
		"move add":     func(p map[string]any) { p["changes"].([]any)[0].(map[string]any)["kind"] = "add" },
	} {
		t.Run(name, func(t *testing.T) {
			p := patchFixture().Payload()
			mutate(p)
			if _, err := dgruntime.ParseCompletedPatch(p); err == nil {
				t.Fatal("invalid patch accepted")
			}
		})
	}
	for _, kind := range []string{"add", "delete", "update"} {
		p := patchFixture()
		p.Changes[0].Kind = kind
		p.Changes[0].MovePath = nil
		if _, err := dgruntime.ParseCompletedPatch(p.Payload()); err != nil {
			t.Fatal(kind, err)
		}
	}
	p := patchFixture()
	p.Changes = make([]dgruntime.PatchChange, dgruntime.MaximumPatchChanges+1)
	if _, err := dgruntime.ParseCompletedPatch(p.Payload()); err == nil {
		t.Fatal("unbounded change count")
	}
}
func TestPatchArtifactContract(t *testing.T) {
	ref := dgruntime.PatchArtifact{ArtifactID: "art_" + strings.Repeat("a", 20), Digest: "sha256:" + strings.Repeat("a", 64), SizeBytes: 300, Status: "failed", ChangeCount: 1, Preview: dgruntime.PatchPreview(1)}
	if _, err := dgruntime.ParsePatchArtifact(ref.Payload()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"path":           func(p map[string]any) { p["path"] = "private" },
		"zero count":     func(p map[string]any) { p["changeCount"] = 0 },
		"huge count":     func(p map[string]any) { p["changeCount"] = 257 },
		"fraction":       func(p map[string]any) { p["changeCount"] = 1.5 },
		"missing digest": func(p map[string]any) { delete(p, "digest") },
		"invalid size":   func(p map[string]any) { p["sizeBytes"] = 0 },
		"preview":        func(p map[string]any) { p["preview"] = "private path" },
	} {
		t.Run(name, func(t *testing.T) {
			p := ref.Payload()
			mutate(p)
			if _, err := dgruntime.ParsePatchArtifact(p); err == nil {
				t.Fatal("invalid reference accepted")
			}
		})
	}
	if !utf8.ValidString(dgruntime.PatchPreview(256)) {
		t.Fatal("invalid preview")
	}
}
