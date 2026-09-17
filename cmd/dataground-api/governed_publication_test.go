package main

import (
	"strings"
	"testing"
)

func TestGovernedPublicationConfigurationRejectsAmbiguousAuthority(t *testing.T) {
	t.Parallel()
	lookup := func(path string) func(string) (string, bool) {
		return func(name string) (string, bool) { return path, name == governedPublicationConfigurationEnvironment }
	}
	if target, err := loadGovernedPublicationTarget(func(string) (string, bool) { return "", false }); err != nil || target != nil {
		t.Fatal(target, err)
	}
	if _, err := loadGovernedPublicationTarget(lookup("")); err == nil {
		t.Fatal("empty configured path")
	}
	valid := `{"contract":"dataground.api-governed-publication/v1","isolationDomainId":"iso_00000000000000000001","serviceId":"svc_00000000000000000001","revisionId":"rev_00000000000000000001","runtimeProfile":"codex.app-server/v1","expectedVersion":1,"planDigest":"sha256:` + strings.Repeat("a", 64) + `","policyDigest":"sha256:` + strings.Repeat("b", 64) + `","verificationDigest":"sha256:` + strings.Repeat("c", 64) + `"}`
	for name, content := range map[string]string{
		"valid":            valid,
		"old contract":     strings.Replace(valid, "api-governed-publication/v1", "api-governed-publication/v2", 1),
		"native endpoint":  strings.Replace(valid, `"contract":`, `"gateway":"http://127.0.0.1:1234","contract":`, 1),
		"actor":            strings.Replace(valid, `"contract":`, `"actorId":"operator","contract":`, 1),
		"duplicate":        strings.Replace(valid, `"expectedVersion":1`, `"expectedVersion":1,"expectedVersion":2`, 1),
		"missing version":  strings.Replace(valid, `"expectedVersion":1,`, "", 1),
		"malformed digest": strings.Replace(valid, strings.Repeat("a", 64), "invalid", 1),
		"missing digest":   strings.Replace(valid, `"policyDigest":"sha256:`+strings.Repeat("b", 64)+`",`, "", 1),
		"wrong runtime":    strings.Replace(valid, "codex.app-server/v1", "reference/v1", 1),
		"missing revision": strings.Replace(valid, `"revisionId":"rev_00000000000000000001",`, "", 1),
		"trailing":         valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeStartupFile(t, t.TempDir(), "publication.json", content)
			target, err := loadGovernedPublicationTarget(lookup(path))
			if name != "valid" {
				if err == nil {
					t.Fatal("invalid configuration admitted")
				}
				return
			}
			if err != nil || target == nil || !target.ValidReviewedInputs() || target.ActorID != "" || target.CorrelationID != "" {
				t.Fatal(target, err)
			}
		})
	}
}
