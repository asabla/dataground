package reconcile

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestCodexInvocationRuntimeRequestBuilderMapsArtifactDeclarations(t *testing.T) {
	target := codexRuntimeRequestTarget()
	target.Input["artifacts"] = []any{
		map[string]any{
			"id":          "report",
			"name":        "Report",
			"sandboxPath": "/workspace/report.json",
			"mediaType":   "application/json",
			"kind":        "file",
		},
	}
	request, err := (CodexInvocationRuntimeRequestBuilder{}).
		BuildInvocationRuntimeRequest(target)
	if err != nil {
		t.Fatal(err)
	}
	want := []dgruntime.ArtifactDeclaration{{
		ID:          "report",
		Name:        "Report",
		SandboxPath: "/workspace/report.json",
		MediaType:   "application/json",
		Kind:        "file",
	}}
	if !reflect.DeepEqual(request.Artifacts, want) {
		t.Fatalf("artifacts = %#v, want %#v", request.Artifacts, want)
	}
	if request.SandboxMode != dgruntime.SandboxWorkspaceWrite {
		t.Fatalf("sandbox mode = %q", request.SandboxMode)
	}
	target.Input["artifacts"].([]any)[0].(map[string]any)["name"] = "changed"
	if request.Artifacts[0].Name != "Report" {
		t.Fatal("runtime request retained the durable artifact declaration map")
	}
}

func TestCodexInvocationRuntimeRequestBuilderRejectsInvalidArtifactDeclarations(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{
			"id":          "report",
			"name":        "Report",
			"sandboxPath": "/workspace/report.json",
			"mediaType":   "application/json",
			"kind":        "file",
		}
	}
	tests := map[string]func([]any) []any{
		"non-object": func([]any) []any { return []any{"report"} },
		"unknown field": func(items []any) []any {
			items[0].(map[string]any)["hostPath"] = "/tmp/report"
			return items
		},
		"relative path": func(items []any) []any {
			items[0].(map[string]any)["sandboxPath"] = "report.json"
			return items
		},
		"duplicate id": func(items []any) []any {
			return append(items, valid())
		},
		"duplicate path": func(items []any) []any {
			other := valid()
			other["id"] = "other"
			return append(items, other)
		},
		"unknown kind": func(items []any) []any {
			items[0].(map[string]any)["kind"] = "directory"
			return items
		},
		"oversized name": func(items []any) []any {
			items[0].(map[string]any)["name"] = strings.Repeat("a", 256)
			return items
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			target := codexRuntimeRequestTarget()
			target.Input["artifacts"] = change([]any{valid()})
			_, err := (CodexInvocationRuntimeRequestBuilder{}).
				BuildInvocationRuntimeRequest(target)
			if !errors.Is(err, ErrInvocationRuntimeInputInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestValidateInvocationRuntimeRequestRejectsUnsafeArtifactDeclarations(t *testing.T) {
	request := dgruntime.StartRequest{
		Prompt:       "persisted prompt",
		ApprovalMode: dgruntime.ApprovalLocked,
		SandboxMode:  dgruntime.SandboxReadOnly,
		Artifacts: []dgruntime.ArtifactDeclaration{{
			ID:          "report",
			Name:        "Report",
			SandboxPath: "/workspace/report.json",
			MediaType:   "application/json",
			Kind:        "file",
		}},
	}
	if err := validateInvocationRuntimeRequest(request); err == nil {
		t.Fatal("read-only artifact declaration was accepted")
	}
	request.SandboxMode = dgruntime.SandboxWorkspaceWrite
	if err := validateInvocationRuntimeRequest(request); err != nil {
		t.Fatalf("valid declaration rejected: %v", err)
	}
}

func TestMapCodexInvocationArtifactsDoesNotMutateTarget(t *testing.T) {
	target := persistence.InvocationRuntimeTarget{
		RuntimeProfile: CodexAppServerRuntimeProfileV1,
		Input: map[string]any{
			"prompt": "persisted prompt",
			"artifacts": []any{map[string]any{
				"id":          "report",
				"name":        "Report",
				"sandboxPath": "/workspace/report.json",
				"mediaType":   "application/json",
				"kind":        "file",
			}},
		},
	}
	request, err := (CodexInvocationRuntimeRequestBuilder{}).
		BuildInvocationRuntimeRequest(target)
	if err != nil {
		t.Fatal(err)
	}
	request.Artifacts[0].Name = "changed"
	if target.Input["artifacts"].([]any)[0].(map[string]any)["name"] != "Report" {
		t.Fatal("runtime request mutated durable declaration")
	}
}
