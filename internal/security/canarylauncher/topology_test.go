package canarylauncher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTopologyWorkspaceFreezesAndRemovesExactInputs(t *testing.T) {
	root := t.TempDir()
	compose := []byte("services: {}\n")
	gateway := []byte("[openshell]\nversion = 1\n")
	workspace, err := openTopologyWorkspace(root, testRunID, compose, gateway)
	if err != nil {
		t.Fatal(err)
	}
	actualCompose, err := os.ReadFile(workspace.composePath)
	if err != nil || string(actualCompose) != string(compose) {
		t.Fatalf("compose copy = %q, %v", actualCompose, err)
	}
	actualGateway, err := os.ReadFile(workspace.gatewayPath)
	if err != nil || string(actualGateway) != string(gateway) {
		t.Fatalf("gateway copy = %q, %v", actualGateway, err)
	}
	for _, path := range []string{workspace.composePath, workspace.gatewayPath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("topology file mode = %v, %v", info, err)
		}
	}
	if _, err := json.Marshal(workspace); !errors.Is(err, ErrSerialization) {
		t.Fatalf("MarshalJSON() error = %v", err)
	}
	if err := workspace.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Lstat(workspace.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("topology directory survived cleanup: %v", err)
	}
	if err := workspace.Cleanup(context.Background()); err != nil {
		t.Fatalf("idempotent Cleanup() error = %v", err)
	}
}

func TestTopologyWorkspaceRejectsReplacementDuringCleanup(t *testing.T) {
	root := t.TempDir()
	workspace, err := openTopologyWorkspace(
		root,
		testRunID,
		[]byte("services: {}\n"),
		[]byte("[openshell]\nversion = 1\n"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(workspace.composePath); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("do not remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, workspace.composePath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := workspace.Cleanup(context.Background()); !errors.Is(err, ErrLaunch) {
		t.Fatalf("replacement cleanup error = %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "do not remove" {
		t.Fatalf("replacement target changed = %q, %v", content, err)
	}
}
