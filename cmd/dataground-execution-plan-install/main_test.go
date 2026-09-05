package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

func testPlan() execution.ExecutionPlan {
	return execution.ExecutionPlan{
		SchemaVersion:     execution.ExecutionPlanSchemaV1,
		IsolationDomainID: "iso_00000000000000000001", RevisionID: "rev_00000000000000000001",
		RuntimeProfile: "codex.app-server/v1", EnvironmentRevisionID: "environment-1",
		ImageReference:            "registry.invalid/runtime@sha256:" + strings.Repeat("a", 64),
		EnvironmentManifestDigest: "sha256:" + strings.Repeat("b", 64),
		EnforcementBundleID:       "enforcement-1", EnforcementBundleDigest: "sha256:" + strings.Repeat("c", 64),
		RuntimeMatrixID: "matrix-1", RuntimeMatrixDigest: "sha256:" + strings.Repeat("d", 64),
		ProviderProfiles: []string{"codex"}, RequiredCapabilities: []string{"codex.app-server/v1"},
	}
}

func installation(t *testing.T) ([]string, string, []byte) {
	t.Helper()
	plan := testPlan()
	content, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	path := filepath.Join(t.TempDir(), "execution-plan.json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := execution.DigestExecutionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	return []string{
		"-plan-file", path, "-plan-digest", digest,
		"-isolation-domain", plan.IsolationDomainID, "-revision", plan.RevisionID,
		"-actor", "operator", "-correlation-id", "plan-install-1",
	}, path, content
}

func TestInstallationPinsReviewedPlanAndAttribution(t *testing.T) {
	arguments, _, _ := installation(t)
	binding, err := readInstallation(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(binding.Plan, testPlan()) || binding.ActorID != "operator" ||
		binding.CorrelationID != "plan-install-1" {
		t.Fatal("installation changed the reviewed binding")
	}
	// Both conventional flag spellings have identical semantics.
	var equals []string
	for index := 0; index < len(arguments); index += 2 {
		equals = append(equals, "-"+arguments[index]+"="+arguments[index+1])
	}
	second, err := readInstallation(equals)
	if err != nil || !reflect.DeepEqual(binding, second) {
		t.Fatalf("equivalent flags changed binding: %v", err)
	}
}

func TestInstallationRejectsWrongScopeDigestAndAmbiguousFlags(t *testing.T) {
	for name, change := range map[string]func([]string) []string{
		"missing flag":        func(a []string) []string { return a[:len(a)-2] },
		"unknown flag":        func(a []string) []string { return append(a, "-secret", "do-not-print") },
		"repeated flag":       func(a []string) []string { return append(a, "--actor=operator") },
		"positional":          func(a []string) []string { return append(a, "do-not-print") },
		"empty attribution":   func(a []string) []string { a[9] = ""; return a },
		"invalid attribution": func(a []string) []string { a[11] = "bad\ncorrelation"; return a },
		"wrong digest":        func(a []string) []string { a[3] = "sha256:" + strings.Repeat("0", 64); return a },
		"wrong domain":        func(a []string) []string { a[5] = "iso_00000000000000000002"; return a },
		"wrong revision":      func(a []string) []string { a[7] = "rev_00000000000000000002"; return a },
	} {
		t.Run(name, func(t *testing.T) {
			arguments, _, _ := installation(t)
			binding, err := readInstallation(change(arguments))
			if err == nil || !reflect.DeepEqual(binding, execution.ExecutionPlanBinding{}) {
				t.Fatal("invalid installation returned a binding")
			}
			if strings.Contains(err.Error(), "do-not-print") {
				t.Fatal("error exposed rejected flag values")
			}
		})
	}
}

func TestInstallationRejectsUnreviewedOrAmbiguousJSON(t *testing.T) {
	for name, change := range map[string]func([]byte) []byte{
		"unknown property":    func(b []byte) []byte { return bytes.Replace(b, []byte("{"), []byte(`{"secret":"do-not-print",`), 1) },
		"duplicate property":  func(b []byte) []byte { return bytes.Replace(b, []byte("{"), []byte(`{"runtimeProfile":"other",`), 1) },
		"wrong casing":        func(b []byte) []byte { return bytes.Replace(b, []byte("runtimeProfile"), []byte("RuntimeProfile"), 1) },
		"second object":       func(b []byte) []byte { return append(b, []byte("{}")...) },
		"extra whitespace":    func(b []byte) []byte { return append([]byte(" "), b...) },
		"missing newline":     func(b []byte) []byte { return bytes.TrimSuffix(b, []byte("\n")) },
		"duplicate providers": func(b []byte) []byte { return bytes.Replace(b, []byte(`["codex"]`), []byte(`["codex","codex"]`), 1) },
		"substituted content": func(b []byte) []byte { return bytes.Replace(b, []byte("environment-1"), []byte("environment-2"), 1) },
		"invalid image": func(b []byte) []byte {
			return bytes.Replace(b, []byte("@sha256:"+strings.Repeat("a", 64)), []byte(":latest"), 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			arguments, path, content := installation(t)
			if err := os.WriteFile(path, change(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := readInstallation(arguments)
			if err == nil || strings.Contains(err.Error(), "do-not-print") {
				t.Fatalf("invalid plan accepted or exposed: %v", err)
			}
		})
	}
}

func TestPlanFileRejectsUnsafeInputs(t *testing.T) {
	_, regular, content := installation(t)
	directory := t.TempDir()
	paths := map[string]string{"missing": filepath.Join(directory, "missing"), "directory": directory}
	for name, body := range map[string][]byte{"empty": {}, "oversized": make([]byte, maximumPlanBytes+1), "public": content} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if name == "public" {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		paths[name] = path
	}
	link := filepath.Join(directory, "symlink")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	paths["symlink"] = link
	fifo := filepath.Join(directory, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	paths["fifo"] = fifo
	for name, path := range paths {
		t.Run(name, func(t *testing.T) {
			if content, err := readPlanFile(path); err == nil || content != nil {
				t.Fatal("unsafe file was read")
			}
		})
	}
}

func TestRunValidatesPlanBeforeDatabaseAccess(t *testing.T) {
	arguments, _, _ := installation(t)
	t.Setenv("DATAGROUND_DATABASE_URL", "")
	if err := run(context.Background(), arguments); err == nil || err.Error() != "DATAGROUND_DATABASE_URL is required" {
		t.Fatalf("valid input did not reach database boundary: %v", err)
	}
	arguments[3] = "invalid"
	if err := run(context.Background(), arguments); err == nil || err.Error() != "execution plan does not match the reviewed digest" {
		t.Fatalf("invalid input reached database boundary: %v", err)
	}
}
