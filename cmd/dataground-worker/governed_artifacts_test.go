package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestGovernedArtifactWorkspaceMatchesOpenShellExportBoundary(t *testing.T) {
	for _, sandboxPath := range []string{"/sandbox/report.json", "/sandbox/results/report.json"} {
		request, err := (governedCodexRuntimeRequestBuilder{}).BuildInvocationRuntimeRequest(
			governedArtifactTarget(sandboxPath),
		)
		if err != nil {
			t.Fatal(err)
		}
		if request.WorkingDir != "/sandbox" || request.SandboxMode != dgruntime.SandboxWorkspaceWrite ||
			request.ApprovalMode != dgruntime.ApprovalInteractive || request.Model != "" ||
			len(request.Artifacts) != 1 || request.Artifacts[0].SandboxPath != sandboxPath {
			t.Fatalf("governed artifact request does not bind the writable export workspace: %#v", request)
		}
	}
}

func TestGovernedArtifactPathsFailBeforeRuntimeRequest(t *testing.T) {
	for _, sandboxPath := range []string{"/tmp/report.json", "/workspace/report.json", "/sandbox", "/sandbox/", "/sandbox-neighbor/report.json", "/sandbox/../report.json", "/sandbox//report.json", "report.json", "/sandbox/report\x00.json"} {
		t.Run(sandboxPath, func(t *testing.T) {
			request, err := (governedCodexRuntimeRequestBuilder{}).BuildInvocationRuntimeRequest(
				governedArtifactTarget(sandboxPath),
			)
			if !errors.Is(err, reconcile.ErrInvocationRuntimeInputInvalid) || !reflect.DeepEqual(request, dgruntime.StartRequest{}) {
				t.Fatalf("invalid artifact path produced a runtime request: %#v, %v", request, err)
			}
		})
	}
}

func TestGovernedRequestWithoutArtifactsRetainsReadOnlyDefaults(t *testing.T) {
	target := governedArtifactTarget("/sandbox/report.json")
	delete(target.Input, "artifacts")
	request, err := (governedCodexRuntimeRequestBuilder{}).BuildInvocationRuntimeRequest(target)
	if err != nil {
		t.Fatal(err)
	}
	if request.WorkingDir != "" || request.SandboxMode != dgruntime.SandboxReadOnly || request.ApprovalMode != dgruntime.ApprovalInteractive {
		t.Fatalf("unexpected governed read-only request: %#v", request)
	}
}

func governedArtifactTarget(sandboxPath string) persistence.InvocationRuntimeTarget {
	return persistence.InvocationRuntimeTarget{
		RuntimeProfile: reconcile.CodexAppServerRuntimeProfileV1,
		Input: map[string]any{
			"prompt": "produce the declared report",
			"artifacts": []any{map[string]any{
				"id": "report", "name": "Report", "sandboxPath": sandboxPath,
				"mediaType": "application/json", "kind": "file",
			}},
		},
	}
}
