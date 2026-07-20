package openshell

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPolicyWorkspaceReclaimsCrashOrphansUnderExclusiveLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "policies")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root, policyFilePrefix+"orphan"+policyFileSuffix)
	if err := os.WriteFile(orphan, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := OpenPolicyWorkspace(root)
	if err != nil {
		t.Fatalf("open policy workspace: %v", err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash orphan survived startup: %v", err)
	}
	if _, err := OpenPolicyWorkspace(root); !errors.Is(err, ErrPolicyWorkspaceBusy) {
		t.Fatalf("concurrent workspace open = %v, want busy", err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("close policy workspace: %v", err)
	}
	reopened, err := OpenPolicyWorkspace(root)
	if err != nil {
		t.Fatalf("reopen released workspace: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened workspace: %v", err)
	}
}

func TestPolicyWorkspaceRejectsUnsafeRootAndUnexpectedContent(t *testing.T) {
	if _, err := OpenPolicyWorkspace("relative/policies"); !errors.Is(err, ErrPolicyWorkspaceUnsafe) {
		t.Fatalf("relative workspace = %v, want unsafe", err)
	}
	openRoot := t.TempDir()
	if err := os.Chmod(openRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPolicyWorkspace(openRoot); !errors.Is(err, ErrPolicyWorkspaceUnsafe) {
		t.Fatalf("group-readable workspace = %v, want unsafe", err)
	}

	root := filepath.Join(t.TempDir(), "policies")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "operator-note"), []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPolicyWorkspace(root); !errors.Is(err, ErrPolicyWorkspaceUnsafe) {
		t.Fatalf("unexpected workspace content = %v, want unsafe", err)
	}
	if _, err := os.Stat(filepath.Join(root, "operator-note")); err != nil {
		t.Fatalf("unexpected content was altered: %v", err)
	}

	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(t.TempDir(), "policies")
	if err := os.Symlink(target, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPolicyWorkspace(linkedRoot + string(os.PathSeparator)); !errors.Is(err, ErrPolicyWorkspaceUnsafe) {
		t.Fatalf("symlinked workspace = %v, want unsafe", err)
	}

	lockRoot := filepath.Join(t.TempDir(), "policies")
	if err := os.Mkdir(lockRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	lockTarget := filepath.Join(t.TempDir(), "lock-target")
	if err := os.WriteFile(lockTarget, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(lockTarget, filepath.Join(lockRoot, workspaceLock)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPolicyWorkspace(lockRoot); !errors.Is(err, ErrPolicyWorkspaceUnsafe) {
		t.Fatalf("symlinked lock = %v, want unsafe", err)
	}

	hardLinkRoot := filepath.Join(t.TempDir(), "policies")
	if err := os.Mkdir(hardLinkRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	hardLinkTarget := filepath.Join(hardLinkRoot, policyFilePrefix+"target"+policyFileSuffix)
	if err := os.WriteFile(hardLinkTarget, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(hardLinkTarget, filepath.Join(hardLinkRoot, policyFilePrefix+"linked"+policyFileSuffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPolicyWorkspace(hardLinkRoot); !errors.Is(err, ErrPolicyWorkspaceUnsafe) {
		t.Fatalf("hard-linked policy = %v, want unsafe", err)
	}
	if _, err := os.Stat(hardLinkTarget); err != nil {
		t.Fatalf("unsafe hard link was altered: %v", err)
	}
}

func TestPolicyWorkspaceCannotCloseWhilePolicyIsActive(t *testing.T) {
	workspace, err := OpenPolicyWorkspace(filepath.Join(t.TempDir(), "policies"))
	if err != nil {
		t.Fatalf("open policy workspace: %v", err)
	}
	path, cleanup, err := workspace.materialize([]byte("version: 1\n"))
	if err != nil {
		t.Fatalf("materialize policy: %v", err)
	}
	if err := workspace.Close(); !errors.Is(err, ErrPolicyWorkspaceBusy) {
		t.Fatalf("close active workspace = %v, want busy", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("materialized policy mode = %v, %v", info, err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup policy: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("repeat cleanup policy: %v", err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatalf("close cleaned workspace: %v", err)
	}
	if _, _, err := workspace.materialize([]byte("version: 1\n")); !errors.Is(err, ErrPolicyWorkspaceUnavailable) {
		t.Fatalf("materialize after close = %v, want unavailable", err)
	}
}
