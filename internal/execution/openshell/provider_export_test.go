package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

func TestExportReturnsBoundedContentWithoutHostPath(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{}},
		{result: CommandResult{}},
	}}
	provider, _, placement, policy, digest := preparedProvider(t, runner)
	workspace, err := OpenExportWorkspace(filepath.Join(t.TempDir(), "exports"), 32)
	if err != nil {
		t.Fatalf("open export workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := workspace.Close(); err != nil {
			t.Errorf("close export workspace: %v", err)
		}
	})
	provider.exports = workspace
	created, err := provider.Create(context.Background(), createRequest(placement, policy, digest))
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	var destination string
	runner.runHook = func(args []string) {
		if !containsSequence(args, "sandbox", "download") {
			return
		}
		destination = args[len(args)-1]
		if err := os.WriteFile(destination, []byte("artifact-content"), 0o600); err != nil {
			t.Errorf("write exported content: %v", err)
		}
	}
	result, err := provider.Export(context.Background(), execution.ExportRequest{
		IsolationDomainID: created.IsolationDomainID,
		ExecutionID:       created.ID,
		SandboxPath:       "/workspace/result.json",
	})
	if err != nil {
		t.Fatalf("export content: %v", err)
	}
	if string(result.Content) != "artifact-content" {
		t.Fatalf("export content = %q", result.Content)
	}
	if destination == "" || filepath.Dir(destination) != workspace.root {
		t.Fatalf("provider destination escaped workspace: %q", destination)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("export destination survived return: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal export result: %v", err)
	}
	if strings.Contains(string(encoded), "artifact-content") ||
		strings.Contains(string(encoded), workspace.root) {
		t.Fatalf("protected export data serialized: %s", encoded)
	}
}

func TestExportFailsClosedForMissingWorkspaceUnsafePathAndOversize(t *testing.T) {
	runner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{}},
	}}
	provider, _, placement, policy, digest := preparedProvider(t, runner)
	created, err := provider.Create(context.Background(), createRequest(placement, policy, digest))
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	request := execution.ExportRequest{
		IsolationDomainID: created.IsolationDomainID,
		ExecutionID:       created.ID,
		SandboxPath:       "/workspace/result.json",
	}
	if _, err := provider.Export(context.Background(), request); !errors.Is(err, ErrExportWorkspaceUnavailable) {
		t.Fatalf("missing workspace = %v, want unavailable", err)
	}
	workspace, err := OpenExportWorkspace(filepath.Join(t.TempDir(), "exports"), 4)
	if err != nil {
		t.Fatalf("open export workspace: %v", err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	provider.exports = workspace
	request.SandboxPath = "../host"
	if _, err := provider.Export(context.Background(), request); err == nil {
		t.Fatal("relative sandbox path accepted")
	}
	if len(runner.calls) != 2 {
		t.Fatalf("unsafe export reached provider: %#v", runner.calls)
	}
	request.SandboxPath = "/workspace/result.json"
	runner.results = []scriptedResult{{result: CommandResult{}}}
	runner.runHook = func(args []string) {
		if containsSequence(args, "sandbox", "download") {
			_ = os.WriteFile(args[len(args)-1], []byte("oversized"), 0o600)
		}
	}
	if _, err := provider.Export(context.Background(), request); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("oversized export = %v, want too large", err)
	}
	entries, err := os.ReadDir(workspace.root)
	if err != nil {
		t.Fatalf("read export workspace: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != exportWorkspaceLock {
		t.Fatalf("export workspace retained content: %#v", entries)
	}
}
