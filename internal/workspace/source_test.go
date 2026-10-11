package workspace

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type archiveEntry struct {
	name, content, target string
	kind                  byte
	mode                  int64
}

func sourceArchive(t *testing.T, entries ...archiveEntry) ([]byte, string) {
	t.Helper()
	var data bytes.Buffer
	writer := tar.NewWriter(&data)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{Name: entry.name, Typeflag: kind, Mode: mode, Linkname: entry.target, Size: int64(len(entry.content))}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes(), sourceDigest(data.Bytes())
}
func sourceDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestSourceSnapshotMaterializesBinaryExecutableAndContainedLinks(t *testing.T) {
	data, digest := sourceArchive(t,
		archiveEntry{name: "src/data.bin", content: "a\x00b\xff"},
		archiveEntry{name: "bin/run", content: "#!/bin/sh\n", mode: 0o755},
		archiveEntry{name: "current", kind: tar.TypeSymlink, target: "src"},
		archiveEntry{name: "selected", kind: tar.TypeSymlink, target: "current/data.bin"},
	)
	snapshot, err := Inspect(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	clear(data) // The accepted snapshot owns its bytes.
	parent := t.TempDir()
	receipt, err := snapshot.Materialize(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ArchiveDigest != digest || receipt.EntryCount != 6 || receipt.SourceBytes != 14 {
		t.Fatalf("receipt = %#v", receipt)
	}
	for name, want := range map[string]string{"src/data.bin": "a\x00b\xff", "selected": "a\x00b\xff", "bin/run": "#!/bin/sh\n"} {
		actual, err := os.ReadFile(filepath.Join(parent, "workspace", name))
		if err != nil || string(actual) != want {
			t.Fatalf("materialized content mismatch: %v", err)
		}
	}
	info, err := os.Stat(filepath.Join(parent, "workspace", "bin/run"))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("executable intent was not retained privately")
	}
	if _, err := json.Marshal(snapshot); err == nil {
		t.Fatal("source snapshot was serializable")
	}
	if actual, err := snapshot.Verify(context.Background(), parent); err != nil || actual != receipt {
		t.Fatalf("read-back = %#v %v", actual, err)
	}
	if _, err := snapshot.Materialize(context.Background(), parent); !errors.Is(err, ErrSourceExists) {
		t.Fatal("existing workspace was replaced")
	}
	if err := os.WriteFile(filepath.Join(parent, "workspace", "src", "data.bin"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Verify(context.Background(), parent); !errors.Is(err, ErrSourceInvalid) {
		t.Fatal("changed workspace passed read-back")
	}
}

func TestSourceSnapshotRejectsUnsafeArchives(t *testing.T) {
	cases := map[string][]archiveEntry{
		"parent": {{name: "../escape"}}, "absolute": {{name: "/escape"}}, "backslash": {{name: `a\b`}},
		"git-control": {{name: ".git/config"}}, "duplicate": {{name: "file"}, {name: "file"}},
		"file-parent":   {{name: "dir"}, {name: "dir/file"}},
		"link-parent":   {{name: "dir", kind: tar.TypeSymlink, target: "target"}, {name: "target", kind: tar.TypeDir}, {name: "dir/file"}},
		"escape-link":   {{name: "link", kind: tar.TypeSymlink, target: "../outside"}},
		"absolute-link": {{name: "link", kind: tar.TypeSymlink, target: "/outside"}},
		"dangling-link": {{name: "link", kind: tar.TypeSymlink, target: "missing"}},
		"cycle":         {{name: "a", kind: tar.TypeSymlink, target: "b"}, {name: "b", kind: tar.TypeSymlink, target: "a"}},
		"hardlink":      {{name: "file"}, {name: "link", kind: tar.TypeLink, target: "file"}},
		"fifo":          {{name: "fifo", kind: tar.TypeFifo}}, "device": {{name: "device", kind: tar.TypeChar}},
		"setuid": {{name: "file", mode: 0o4755}}, "long-component": {{name: strings.Repeat("x", 256)}},
		"link-before-dotdot": {{name: "a", kind: tar.TypeDir}, {name: "a/root", kind: tar.TypeSymlink, target: ".."}, {name: "out", content: "safe"}, {name: "bad", kind: tar.TypeSymlink, target: "a/root/../out"}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			data, digest := sourceArchive(t, entries...)
			if _, err := Inspect(data, digest); !errors.Is(err, ErrSourceInvalid) {
				t.Fatalf("unsafe archive accepted: %v", err)
			}
		})
	}
	good, digest := sourceArchive(t, archiveEntry{name: "file", content: "content"})
	for name, data := range map[string][]byte{"truncated": good[:len(good)-512], "trailing": append(bytes.Clone(good), append([]byte("hidden"), make([]byte, 506)...)...), "digest": good} {
		t.Run(name, func(t *testing.T) {
			expected := sourceDigest(data)
			if name == "digest" {
				expected = "sha256:" + strings.Repeat("0", 64)
			}
			if _, err := Inspect(data, expected); !errors.Is(err, ErrSourceInvalid) {
				t.Fatal("invalid archive accepted", digest)
			}
		})
	}
}

func TestSourceMaterializationNeverReplacesExistingTargets(t *testing.T) {
	data, digest := sourceArchive(t, archiveEntry{name: "file", content: "new"})
	snapshot, err := Inspect(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"directory", "file", "link"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			target := filepath.Join(parent, "workspace")
			outside := t.TempDir()
			switch kind {
			case "directory":
				err = os.Mkdir(target, 0o700)
			case "file":
				err = os.WriteFile(target, []byte("original"), 0o600)
			case "link":
				err = os.Symlink(outside, target)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := snapshot.Materialize(context.Background(), parent); !errors.Is(err, ErrSourceExists) {
				t.Fatal("existing target was changed", err)
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 0 {
				t.Fatal("outside directory was changed")
			}
		})
	}
	parent := t.TempDir()
	var wg sync.WaitGroup
	outcomes := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := snapshot.Materialize(context.Background(), parent); outcomes <- err }()
	}
	wg.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrSourceExists) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal("concurrent materialization did not have one winner")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 1 || entries[0].Name() != "workspace" {
		t.Fatal("private staging directories remained")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	empty := t.TempDir()
	if _, err := snapshot.Materialize(ctx, empty); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
	entries, _ = os.ReadDir(empty)
	if len(entries) != 0 {
		t.Fatal("cancelled call mutated parent")
	}
}

func TestSourceArchiveLimitsAndGitPAXComment(t *testing.T) {
	tooLarge := make([]byte, MaximumArchiveBytes+512)
	if _, err := Inspect(tooLarge, sourceDigest(tooLarge)); !errors.Is(err, ErrSourceInvalid) {
		t.Fatal("archive byte limit ignored")
	}
	entries := make([]archiveEntry, MaximumEntries+1)
	for i := range entries {
		entries[i] = archiveEntry{name: fmt.Sprintf("file-%d", i)}
	}
	data, digest := sourceArchive(t, entries...)
	if _, err := Inspect(data, digest); !errors.Is(err, ErrSourceInvalid) {
		t.Fatal("entry limit ignored")
	}
	for i := range entries[:MaximumEntries] {
		entries[i] = archiveEntry{name: fmt.Sprintf("dir-%d/sub/file", i)}
	}
	data, digest = sourceArchive(t, entries[:MaximumEntries]...)
	if _, err := Inspect(data, digest); !errors.Is(err, ErrSourceInvalid) {
		t.Fatal("implicit directory limit ignored")
	}
	var large bytes.Buffer
	writer := tar.NewWriter(&large)
	if err := writer.WriteHeader(&tar.Header{Name: "file", Mode: 0o600, Size: MaximumSourceBytes + 1}); err != nil {
		t.Fatal(err)
	}
	data = append(large.Bytes(), make([]byte, 1024)...)
	if _, err := Inspect(data, sourceDigest(data)); !errors.Is(err, ErrSourceInvalid) {
		t.Fatal("expanded file limit ignored")
	}
	var gitArchive bytes.Buffer
	writer = tar.NewWriter(&gitArchive)
	if err := writer.WriteHeader(&tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": strings.Repeat("a", 40)}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteHeader(&tar.Header{Name: "src/file", Mode: 0o644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	writer.Write([]byte("x"))
	writer.Close()
	snapshot, err := Inspect(gitArchive.Bytes(), sourceDigest(gitArchive.Bytes()))
	if err != nil {
		t.Fatal("Git archive comment rejected", err)
	}
	if snapshot.Receipt().EntryCount != 2 {
		t.Fatal("archive metadata became a workspace file")
	}
}

func TestSourceReadbackRejectsUntrustedFilesystemChanges(t *testing.T) {
	for _, change := range []string{"extra", "mode", "setuid", "sticky-directory", "symlink", "hardlink"} {
		t.Run(change, func(t *testing.T) {
			data, digest := sourceArchive(t, archiveEntry{name: "file", content: "retained"})
			snapshot, err := Inspect(data, digest)
			if err != nil {
				t.Fatal(err)
			}
			parent := t.TempDir()
			if _, err := snapshot.Materialize(context.Background(), parent); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(parent, "workspace", "file")
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("retained"), 0o600); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "extra":
				err = os.WriteFile(filepath.Join(parent, "workspace", "extra"), nil, 0o600)
			case "mode":
				err = os.Chmod(target, 0o644)
			case "setuid":
				err = os.Chmod(target, os.ModeSetuid|0o600)
			case "sticky-directory":
				err = os.Chmod(filepath.Dir(target), os.ModeSticky|0o700)
			case "symlink":
				if err = os.Remove(target); err == nil {
					err = os.Symlink(outside, target)
				}
			case "hardlink":
				if err = os.Remove(target); err == nil {
					err = os.Link(outside, target)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := snapshot.Verify(context.Background(), parent); !errors.Is(err, ErrSourceInvalid) {
				t.Fatal("untrusted change passed read-back", err)
			}
			content, err := os.ReadFile(outside)
			if err != nil || string(content) != "retained" {
				t.Fatal("read-back changed outside content")
			}
		})
	}
}

type stagedCancellation struct {
	context.Context
	cancel context.CancelFunc
	calls  int
}

func (c *stagedCancellation) Err() error {
	c.calls++
	if c.calls == 3 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestSourceStagingCancellationAndParentIsolation(t *testing.T) {
	data, digest := sourceArchive(t, archiveEntry{name: "file", content: "retained"})
	snapshot, err := Inspect(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := snapshot.Materialize(&stagedCancellation{Context: ctx, cancel: cancel}, parent); !errors.Is(err, context.Canceled) {
		t.Fatal("staging cancellation ignored", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal("cancelled staging remained")
	}
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0o700)
	if _, err := snapshot.Materialize(context.Background(), parent); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatal("shared writable parent was accepted")
	}
	link := filepath.Join(t.TempDir(), "parent")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Materialize(context.Background(), link); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatal("symbolic parent was accepted")
	}
}
