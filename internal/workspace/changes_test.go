package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func inspectedFixture(t *testing.T, entries ...archiveEntry) *Snapshot {
	t.Helper()
	data, digest := sourceArchive(t, entries...)
	snapshot, err := Inspect(data, digest)
	clear(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(snapshot.discard)
	return snapshot
}

func TestWorkspaceChangesCaptureAndApplyExactBinaryTree(t *testing.T) {
	base := inspectedFixture(t, archiveEntry{name: "deleted", content: "gone"}, archiveEntry{name: "binary", content: "before\x00\xff"}, archiveEntry{name: "unchanged", content: "retain"}, archiveEntry{name: "swap/child", content: "nested"})
	parent := t.TempDir()
	ctx := context.Background()
	if _, err := base.Materialize(ctx, parent); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(parent, "workspace")
	if err := os.Remove(filepath.Join(tree, "deleted")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "binary"), []byte("after\x00\xfe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(tree, "binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(tree, "swap")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "swap"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tree, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("binary", filepath.Join(tree, "link")); err != nil {
		t.Fatal(err)
	}
	final, err := Capture(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer final.discard()
	changes, err := Compare(base, final)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := changes.Write(ctx, &encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(changes); err == nil {
		t.Fatal("sensitive changes were serializable")
	}
	receipt := changes.Receipt()
	if receipt.BaseArchiveDigest != base.Receipt().ArchiveDigest || receipt.Digest != sourceDigest(encoded.Bytes()) || receipt.SizeBytes != int64(encoded.Len()) || receipt.ChangeCount != 6 {
		t.Fatalf("invalid receipt: %+v", receipt)
	}
	applied, err := ApplyChanges(base, encoded.Bytes(), receipt.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer applied.discard()
	if applied.Receipt().ArchiveDigest != final.Receipt().ArchiveDigest || !bytes.Equal(applied.archive, final.archive) {
		t.Fatal("patch did not recreate final tree")
	}
	restored := t.TempDir()
	if _, err := applied.Materialize(ctx, restored); err != nil {
		t.Fatal(err)
	}
	if _, err := applied.Verify(ctx, restored); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(restored, "workspace", "link")); err != nil || string(got) != "after\x00\xfe" {
		t.Fatal("binary target not restored", err)
	}
	again, err := Capture(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	defer again.discard()
	repeated, err := Compare(base, again)
	if err != nil || !bytes.Equal(repeated.encoded, changes.encoded) {
		t.Fatal("unchanged capture produced different artifact", err)
	}
	noChanges, err := Compare(final, again)
	if err != nil || noChanges.Receipt().ChangeCount != 0 {
		t.Fatal("unchanged tree generated changes", err)
	}
	if _, err := base.Verify(ctx, parent); err == nil {
		t.Fatal("export overwrote edited tree with base")
	}
}

func TestWorkspaceChangesRejectTamperingAndAmbiguousEncoding(t *testing.T) {
	base := inspectedFixture(t, archiveEntry{name: "dir/file", content: "base"})
	final := inspectedFixture(t, archiveEntry{name: "dir/file", content: "changed"}, archiveEntry{name: "new", content: "new"})
	valid, err := Compare(base, final)
	if err != nil {
		t.Fatal(err)
	}
	foreign := inspectedFixture(t, archiveEntry{name: "dir/file", content: "different"})
	if _, err := ApplyChanges(foreign, valid.encoded, valid.receipt.Digest); err == nil {
		t.Fatal("wrong base admitted")
	}
	for _, mode := range []string{"base", "result", "before", "duplicate", "order", "path", "kind", "link-escape", "hardlink", "oversize", "redundant", "missing-parent", "unknown-field", "duplicate-json", "trailing", "space", "digest"} {
		t.Run(mode, func(t *testing.T) {
			var document changeDocument
			if json.Unmarshal(valid.encoded, &document) != nil {
				t.Fatal("fixture")
			}
			switch mode {
			case "base":
				document.BaseArchiveDigest = "sha256:" + strings.Repeat("0", 64)
			case "result":
				document.ResultArchiveDigest = "sha256:" + strings.Repeat("0", 64)
			case "before":
				document.Changes[0].Before = "sha256:" + strings.Repeat("0", 64)
			case "duplicate":
				document.Changes = append(document.Changes, document.Changes[1])
			case "order":
				document.Changes[0], document.Changes[1] = document.Changes[1], document.Changes[0]
			case "path":
				document.Changes[1].Path = "../escape"
			case "kind":
				document.Changes[1].After.Kind = "socket"
			case "link-escape":
				document.Changes[1].After = &changeNode{Kind: "symlink", Target: "../outside"}
			case "hardlink":
				document.Changes[1].After = &changeNode{Kind: "hardlink", Target: "dir/file"}
			case "oversize":
				document.Changes[1].After.Content = make([]byte, MaximumSourceBytes+1)
			case "redundant":
				node := nodeFromEntry(base.entries["dir/file"])
				document.Changes[0].After = &node
			case "missing-parent":
				document.Changes = append([]fileChange{{Path: "dir", Before: sourceEntryDigest(base.entries["dir"])}}, document.Changes...)
			}
			encoded, _ := json.Marshal(document)
			switch mode {
			case "unknown-field":
				encoded = bytes.Replace(encoded, []byte(`"contract":`), []byte(`"unknown":true,"contract":`), 1)
			case "duplicate-json":
				encoded = bytes.Replace(encoded, []byte(`"contract":`), []byte(`"contract":"wrong","contract":`), 1)
			case "trailing":
				encoded = append(encoded, []byte(`{}`)...)
			case "space":
				encoded = append(encoded, '\n')
			}
			digest := sourceDigest(encoded)
			if mode == "digest" {
				digest = "sha256:" + strings.Repeat("0", 64)
			}
			if snapshot, err := ApplyChanges(base, encoded, digest); !errors.Is(err, ErrChangesInvalid) || snapshot != nil {
				t.Fatal("malformed patch admitted", err)
			}
		})
	}
}

func TestWorkspaceCaptureRejectsUnsafeNodesWithoutReadingTargets(t *testing.T) {
	for _, mode := range []string{"outside-link", "dangling-link", "cycle", "hardlink", "git", "special-mode", "writable", "root-link", "too-large", "too-many"} {
		t.Run(mode, func(t *testing.T) {
			base := inspectedFixture(t, archiveEntry{name: "file", content: "safe"})
			parent := t.TempDir()
			if _, err := base.Materialize(context.Background(), parent); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(parent, "workspace")
			outside := filepath.Join(t.TempDir(), "private")
			if err := os.WriteFile(outside, []byte("must-not-export"), 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mode {
			case "outside-link":
				err = os.Symlink(outside, filepath.Join(root, "bad"))
			case "dangling-link":
				err = os.Symlink("missing", filepath.Join(root, "bad"))
			case "cycle":
				if err = os.Symlink("bad", filepath.Join(root, "bad")); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				err = os.Link(outside, filepath.Join(root, "bad"))
			case "git":
				err = os.Mkdir(filepath.Join(root, ".git"), 0o700)
			case "special-mode":
				err = os.Chmod(filepath.Join(root, "file"), 0o600|os.ModeSetuid)
			case "writable":
				err = os.Chmod(filepath.Join(root, "file"), 0o666)
			case "root-link":
				if err = os.Rename(root, root+"-old"); err == nil {
					err = os.Symlink(root+"-old", root)
				}
			case "too-large":
				err = os.Truncate(filepath.Join(root, "file"), MaximumSourceBytes+1)
			case "too-many":
				for i := 0; i < MaximumEntries; i++ {
					if _, err = os.MkdirTemp(root, "entry-"); err != nil {
						break
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if snapshot, err := Capture(context.Background(), parent); !errors.Is(err, ErrCaptureUnavailable) || snapshot != nil {
				t.Fatal("unsafe tree captured", err)
			}
			if got, err := os.ReadFile(outside); err != nil || string(got) != "must-not-export" {
				t.Fatal("capture changed outside content")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Capture(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled capture", err)
	}
	empty := inspectedFixture(t)
	changes, err := Compare(empty, empty)
	if err != nil {
		t.Fatal(err)
	}
	if err := changes.Write(ctx, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled export", err)
	}
}

func TestWorkspaceChangesPreserveTypeReplacementAndBindCompleteResult(t *testing.T) {
	base := inspectedFixture(t, archiveEntry{name: "file", content: "data"}, archiveEntry{name: "link", kind: tar.TypeSymlink, target: "file"})
	final := inspectedFixture(t, archiveEntry{name: "file/child", content: "new"})
	changes, err := Compare(base, final)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyChanges(base, changes.encoded, changes.receipt.Digest)
	if err != nil {
		t.Fatal(err)
	}
	defer result.discard()
	if result.entries["file"].kind != tar.TypeDir || result.entries["file/child"].kind != tar.TypeReg {
		t.Fatal("type replacement lost")
	}
	var document changeDocument
	_ = json.Unmarshal(changes.encoded, &document)
	document.Changes = document.Changes[:len(document.Changes)-1] // Retain the old link; it now targets a directory and remains valid.
	encoded, _ := json.Marshal(document)
	if _, err := ApplyChanges(base, encoded, sourceDigest(encoded)); err == nil {
		t.Fatal("changed final tree bypassed result digest")
	}
}

func TestWorkspaceChangesRejectDeletionLeavingDanglingLink(t *testing.T) {
	base := inspectedFixture(t, archiveEntry{name: "file", content: "data"}, archiveEntry{name: "link", kind: tar.TypeSymlink, target: "file"})
	empty := inspectedFixture(t)
	changes, err := Compare(base, empty)
	if err != nil {
		t.Fatal(err)
	}
	var document changeDocument
	if err := json.Unmarshal(changes.encoded, &document); err != nil {
		t.Fatal(err)
	}
	document.Changes = document.Changes[:1]
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := ApplyChanges(base, encoded, sourceDigest(encoded)); result != nil || !errors.Is(err, ErrChangesInvalid) {
		t.Fatal("dangling result admitted", err)
	}
}
