package openshell

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExportWorkspaceReclaimsOnlyManagedCrashOrphans(t *testing.T) {
	root := filepath.Join(t.TempDir(), "exports")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, exportFilePrefix+"orphan"+exportFileSuffix)
	if err := os.WriteFile(orphan, []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := OpenExportWorkspace(root, 32)
	if err != nil {
		t.Fatalf("open export workspace: %v", err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash orphan survived startup: %v", err)
	}
	if _, err := OpenExportWorkspace(root, 32); !errors.Is(err, ErrExportWorkspaceBusy) {
		t.Fatalf("concurrent open = %v, want busy", err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("close export workspace: %v", err)
	}
}

func TestExportWorkspaceRejectsUnsafeConfigurationAndEntries(t *testing.T) {
	if _, err := OpenExportWorkspace("relative", 1); !errors.Is(err, ErrExportWorkspaceUnsafe) {
		t.Fatalf("relative root = %v, want unsafe", err)
	}
	if _, err := OpenExportWorkspace(filepath.Join(t.TempDir(), "exports"), 0); !errors.Is(err, ErrExportWorkspaceUnsafe) {
		t.Fatalf("unbounded workspace = %v, want unsafe", err)
	}
	root := filepath.Join(t.TempDir(), "exports")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(root, "operator-note")
	if err := os.WriteFile(note, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExportWorkspace(root, 32); !errors.Is(err, ErrExportWorkspaceUnsafe) {
		t.Fatalf("unexpected content = %v, want unsafe", err)
	}
	if content, err := os.ReadFile(note); err != nil || string(content) != "retain" {
		t.Fatalf("unexpected content changed: %q, %v", content, err)
	}
}

func TestExportWorkspaceCannotCloseWithActiveDestination(t *testing.T) {
	workspace, err := OpenExportWorkspace(filepath.Join(t.TempDir(), "exports"), 32)
	if err != nil {
		t.Fatalf("open export workspace: %v", err)
	}
	path, cleanup, err := workspace.destination()
	if err != nil {
		t.Fatalf("allocate destination: %v", err)
	}
	if err := workspace.Close(); !errors.Is(err, ErrExportWorkspaceBusy) {
		t.Fatalf("close active workspace = %v, want busy", err)
	}
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatalf("write export: %v", err)
	}
	content, err := workspace.consume(path)
	if err != nil || string(content) != "content" {
		t.Fatalf("consume export = %q, %v", content, err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup export: %v", err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("close export workspace: %v", err)
	}
}
