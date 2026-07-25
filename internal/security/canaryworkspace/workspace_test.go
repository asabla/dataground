package canaryworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/asabla/dataground/internal/security/canaryevidence"
)

const testRunID = "0123456789abcdef0123456789abcdef"

func TestWorkspaceOwnsExactLifecycle(t *testing.T) {
	root := t.TempDir()
	workspace, err := Open(Config{Root: root, RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	name := "dg-canary-" + testRunID
	if workspace.Name() != name {
		t.Fatalf("Name() = %q, want %q", workspace.Name(), name)
	}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("workspace mode = %v", info.Mode())
	}
	if _, err := json.Marshal(workspace); !errors.Is(err, ErrWorkspaceSerialization) {
		t.Fatalf("Marshal() error = %v", err)
	}
	if _, err := json.Marshal(*workspace); !errors.Is(err, ErrWorkspaceSerialization) {
		t.Fatalf("Marshal(value) error = %v", err)
	}

	request := canaryevidence.CleanupRequest{
		RunID: testRunID, ResourceKind: "workspace", ResourceName: name,
	}
	if err := workspace.Cleanup(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace still exists: %v", err)
	}
	if err := workspace.Cleanup(context.Background(), request); err != nil {
		t.Fatalf("replayed cleanup failed: %v", err)
	}
}

func TestWorkspaceRejectsWrongCleanupIdentity(t *testing.T) {
	root := t.TempDir()
	workspace, err := Open(Config{Root: root, RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	name := workspace.Name()
	requests := []canaryevidence.CleanupRequest{
		{RunID: "1123456789abcdef0123456789abcdef", ResourceKind: "workspace", ResourceName: name},
		{RunID: testRunID, ResourceKind: "sandbox", ResourceName: name},
		{RunID: testRunID, ResourceKind: "workspace", ResourceName: name + "-other"},
	}
	for _, request := range requests {
		if err := workspace.Cleanup(context.Background(), request); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("Cleanup(%+v) error = %v", request, err)
		}
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			t.Fatalf("wrong identity removed workspace: %v", err)
		}
	}
	if err := workspace.Cleanup(context.Background(), canaryevidence.CleanupRequest{
		RunID: testRunID, ResourceKind: "workspace", ResourceName: name,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceCancellationDoesNotRemove(t *testing.T) {
	root := t.TempDir()
	workspace, err := Open(Config{Root: root, RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := canaryevidence.CleanupRequest{
		RunID: testRunID, ResourceKind: "workspace", ResourceName: workspace.Name(),
	}
	if err := workspace.Cleanup(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, workspace.Name())); err != nil {
		t.Fatalf("cancelled cleanup removed workspace: %v", err)
	}
	if err := workspace.Cleanup(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceRefusesUnexpectedContent(t *testing.T) {
	root := t.TempDir()
	workspace, err := Open(Config{Root: root, RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, workspace.Name())
	unexpected := filepath.Join(path, "unexpected")
	if err := os.WriteFile(unexpected, []byte("not removed"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := canaryevidence.CleanupRequest{
		RunID: testRunID, ResourceKind: "workspace", ResourceName: workspace.Name(),
	}
	if err := workspace.Cleanup(context.Background(), request); !errors.Is(err, ErrWorkspaceNotEmpty) {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Lstat(unexpected); err != nil {
		t.Fatalf("unexpected content was removed: %v", err)
	}
	if err := os.Remove(unexpected); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Cleanup(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceRefusesExistingOrVanishedPath(t *testing.T) {
	t.Run("existing", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "dg-canary-"+testRunID)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(Config{Root: root, RunID: testRunID}); !errors.Is(err, ErrWorkspaceExists) {
			t.Fatalf("Open() error = %v", err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("existing path changed: %v", err)
		}
	})

	t.Run("vanished", func(t *testing.T) {
		root := t.TempDir()
		workspace, err := Open(Config{Root: root, RunID: testRunID})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(root, workspace.Name())); err != nil {
			t.Fatal(err)
		}
		err = workspace.Cleanup(context.Background(), canaryevidence.CleanupRequest{
			RunID: testRunID, ResourceKind: "workspace", ResourceName: workspace.Name(),
		})
		if !errors.Is(err, ErrWorkspaceUncertain) {
			t.Fatalf("Cleanup() error = %v", err)
		}
	})
}

func TestWorkspaceRefusesReplacedPath(t *testing.T) {
	root := t.TempDir()
	workspace, err := Open(Config{Root: root, RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, workspace.Name())
	original := path + "-original"
	if err := os.Rename(path, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	request := canaryevidence.CleanupRequest{
		RunID: testRunID, ResourceKind: "workspace", ResourceName: workspace.Name(),
	}
	if err := workspace.Cleanup(context.Background(), request); !errors.Is(err, ErrWorkspaceUnsafe) {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("replacement was removed: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, path); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Cleanup(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceRejectsUnsafeConfiguration(t *testing.T) {
	if _, err := Open(Config{}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("empty Open() error = %v", err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Config{Root: root, RunID: testRunID}); !errors.Is(err, ErrWorkspaceUnsafe) {
		t.Fatalf("permissive root Open() error = %v", err)
	}

	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Config{Root: link, RunID: testRunID}); !errors.Is(err, ErrWorkspaceUnsafe) {
		t.Fatalf("symlink root Open() error = %v", err)
	}
}

func TestWorkspaceCleanupIsConcurrentAndIdempotent(t *testing.T) {
	root := t.TempDir()
	workspace, err := Open(Config{Root: root, RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	request := canaryevidence.CleanupRequest{
		RunID: testRunID, ResourceKind: "workspace", ResourceName: workspace.Name(),
	}
	const callers = 8
	var wait sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- workspace.Cleanup(context.Background(), request)
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Cleanup() error = %v", err)
		}
	}
}
