package main

import (
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
)

func TestLoadGovernedDispatchTargetRequiresOneExactSupportedTarget(t *testing.T) {
	t.Parallel()

	lookup := func(path string) func(string) (string, bool) {
		return func(name string) (string, bool) {
			if name != governedDispatchConfigurationEnvironment || path == "" {
				return "", false
			}
			return path, true
		}
	}
	if target, err := loadGovernedDispatchTarget(lookup("")); err != nil || target != nil {
		t.Fatalf("unconfigured target = (%#v, %v)", target, err)
	}

	valid := `{"contract":"dataground.api-governed-dispatch/v1",` +
		`"isolationDomainId":"iso_00000000000000000001",` +
		`"serviceId":"svc_00000000000000000001",` +
		`"revisionId":"rev_00000000000000000001",` +
		`"runtimeProfile":"codex.app-server/v1"}`
	path := writeStartupFile(t, t.TempDir(), "governed-dispatch.json", valid)
	target, err := loadGovernedDispatchTarget(lookup(path))
	if err != nil {
		t.Fatalf("load governed dispatch target: %v", err)
	}
	if target == nil || target.IsolationDomainID != "iso_00000000000000000001" ||
		target.ServiceID != "svc_00000000000000000001" ||
		target.RevisionID != "rev_00000000000000000001" ||
		target.RuntimeProfile != persistence.GovernedInvocationRuntimeProfile {
		t.Fatalf("governed dispatch target = %#v", target)
	}

	tests := map[string]string{
		"old contract":       strings.Replace(valid, "/v1", "/v2", 1),
		"unknown member":     strings.Replace(valid, `"contract":`, `"unknown":true,"contract":`, 1),
		"duplicate member":   strings.Replace(valid, `"serviceId":`, `"serviceId":"svc_11111111111111111111","serviceId":`, 1),
		"wrong runtime":      strings.Replace(valid, "codex.app-server/v1", "reference/v1", 1),
		"missing revision":   strings.Replace(valid, `"revisionId":"rev_00000000000000000001",`, "", 1),
		"invalid identifier": strings.Replace(valid, "iso_00000000000000000001", "iso_other", 1),
		"trailing data":      valid + `{}`,
	}
	for name, content := range tests {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := writeStartupFile(t, t.TempDir(), "governed-dispatch.json", content)
			if _, err := loadGovernedDispatchTarget(lookup(path)); err == nil {
				t.Fatal("invalid governed dispatch configuration was accepted")
			}
		})
	}
}

func TestLoadGovernedDispatchTargetRejectsEmptyConfiguredPath(t *testing.T) {
	t.Parallel()
	if _, err := loadGovernedDispatchTarget(func(string) (string, bool) { return "", true }); err == nil {
		t.Fatal("empty configured path was accepted")
	}
}
