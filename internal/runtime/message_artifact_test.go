package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMessageArtifactRejectsMalformedReferences(t *testing.T) {
	valid := MessageArtifact{ArtifactID: "art_00000000000000000001", Digest: "sha256:" + strings.Repeat("a", 64), SizeBytes: 70000, Phase: "final", Preview: "preview"}.Payload()
	for name, change := range map[string]func(map[string]any){
		"route":    func(p map[string]any) { p["objectKey"] = "private" },
		"missing":  func(p map[string]any) { delete(p, "preview") },
		"null":     func(p map[string]any) { p["preview"] = nil },
		"phase":    func(p map[string]any) { p["phase"] = "final_answer" },
		"oversize": func(p map[string]any) { p["sizeBytes"] = MaximumMessageTextBytes + 1 },
		"fraction": func(p map[string]any) { p["sizeBytes"] = 1.5 },
		"preview":  func(p map[string]any) { p["preview"] = strings.Repeat("x", MaximumMessagePreviewBytes+1) },
		"digest":   func(p map[string]any) { p["digest"] = "private" },
	} {
		t.Run(name, func(t *testing.T) {
			p := map[string]any{}
			for k, v := range valid {
				p[k] = v
			}
			change(p)
			if _, err := ParseMessageArtifact(p); err == nil {
				t.Fatal("malformed reference accepted")
			}
		})
	}
	encoded, _ := json.Marshal(valid)
	var decoded map[string]any
	_ = json.Unmarshal(encoded, &decoded)
	if _, err := ParseMessageArtifact(decoded); err != nil {
		t.Fatal(err)
	}
	preview := MessagePreview(strings.Repeat("界", 1000))
	if !utf8.ValidString(preview) || len(preview) > MaximumMessagePreviewBytes {
		t.Fatal("preview splits UTF-8")
	}
}
