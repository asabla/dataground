package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/workspace"
)

type bufferCloser struct{ bytes.Buffer }

func (*bufferCloser) Close() error { return nil }

func source(t *testing.T) ([]byte, string, *workspace.Snapshot) {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "private", Mode: 0o600, Size: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte{0, 1, 255}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	digest := digestOf(archive.Bytes())
	snapshot, err := workspace.Inspect(archive.Bytes(), digest)
	if err != nil {
		t.Fatal(err)
	}
	return archive.Bytes(), digest, snapshot
}
func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestExportRoundTripPreservesChangedWorkspace(t *testing.T) {
	archive, digest, base := source(t)
	parent := t.TempDir()
	if _, err := base.Materialize(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	changed := []byte{255, 0, 17, 0, 254}
	if err := os.WriteFile(filepath.Join(parent, "workspace/private"), changed, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bufferCloser
	capture := func(ctx context.Context, path string) (*workspace.Snapshot, error) {
		if path != "/sandbox" {
			t.Fatal("unexpected destination")
		}
		return workspace.Capture(ctx, parent)
	}
	if err := run(context.Background(), []string{"--archive-digest", digest}, io.NopCloser(bytes.NewReader(archive)), &output, capture, nil); err != nil {
		t.Fatal(err)
	}
	result, err := workspace.ApplyChanges(base, output.Bytes(), digestOf(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	restored := t.TempDir()
	if _, err = result.Materialize(context.Background(), restored); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{parent, restored} {
		data, err := os.ReadFile(filepath.Join(root, "workspace/private"))
		if err != nil || !bytes.Equal(data, changed) {
			t.Fatal("changed binary tree was not preserved", err)
		}
	}
	var repeated bufferCloser
	if err = run(context.Background(), []string{"--archive-digest", digest}, io.NopCloser(bytes.NewReader(archive)), &repeated, capture, nil); err != nil || !bytes.Equal(output.Bytes(), repeated.Bytes()) {
		t.Fatal("repeated export changed", err)
	}
}

func TestExportRejectsInvalidInputBeforeCapture(t *testing.T) {
	archive, digest, _ := source(t)
	for _, args := range [][]string{
		{}, {"--archive-digest", digest, "--destination", "/tmp/workspace"},
		{"--archive-digest", digest, "--archive-digest", digest},
		{"--archive-digest", digest, "--destination", "/workspace", "--destination", "/workspace"},
		{"--archive-digest", digest, "extra"}, {"--archive-digest", strings.ToUpper(digest)},
		{"--archive-digest", "sha256:" + strings.Repeat("0", 64)},
	} {
		var output bufferCloser
		called := false
		err := run(context.Background(), args, io.NopCloser(bytes.NewReader(archive)), &output, func(context.Context, string) (*workspace.Snapshot, error) { called = true; return nil, nil }, nil)
		if err == nil || called || output.Len() != 0 {
			t.Fatal("invalid input reached capture or produced output")
		}
	}
	for _, data := range [][]byte{archive[:len(archive)-1], append(bytes.Clone(archive), 1), make([]byte, workspace.MaximumArchiveBytes+1)} {
		var output bufferCloser
		err := run(context.Background(), []string{"--archive-digest", digestOf(data)}, io.NopCloser(bytes.NewReader(data)), &output, func(context.Context, string) (*workspace.Snapshot, error) {
			t.Error("invalid archive reached capture")
			return nil, nil
		}, nil)
		if err == nil || output.Len() != 0 {
			t.Fatal("invalid archive exported")
		}
	}
}

func TestExportCancelsBlockedStreams(t *testing.T) {
	archive, digest, base := source(t)
	for _, blocked := range []string{"input", "output"} {
		t.Run(blocked, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			var input io.ReadCloser = io.NopCloser(bytes.NewReader(archive))
			var output io.WriteCloser = &bufferCloser{}
			capture := func(context.Context, string) (*workspace.Snapshot, error) { close(entered); return base, nil }
			if blocked == "input" {
				input = reader
			} else {
				output = writer
			}
			done := make(chan error, 1)
			go func() { done <- run(ctx, []string{"--archive-digest", digest}, input, output, capture, nil) }()
			if blocked == "output" {
				select {
				case <-entered:
				case <-time.After(time.Second):
					t.Fatal("capture did not start")
				}
				// Read one byte to prove the writer is active, then leave the rest blocked.
				if _, err := io.ReadFull(reader, make([]byte, 1)); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancelled stream remained blocked")
			}
		})
	}
}

func TestExportCaptureFailureProducesNoArtifact(t *testing.T) {
	archive, digest, _ := source(t)
	var output bufferCloser
	err := run(context.Background(), []string{"--archive-digest", digest, "--destination", "/workspace"}, io.NopCloser(bytes.NewReader(archive)), &output, func(_ context.Context, parent string) (*workspace.Snapshot, error) {
		if parent != "/" {
			t.Fatal("unexpected root destination")
		}
		return nil, workspace.ErrCaptureUnavailable
	}, nil)
	if !errors.Is(err, workspace.ErrCaptureUnavailable) || output.Len() != 0 {
		t.Fatal("capture failure produced artifact", err)
	}
}

func TestStoppedDockerExport(t *testing.T) {
	archive, digest, base := source(t)
	stoppedArchive := dockerArchive(t,
		tar.Header{Name: "workspace/", Typeflag: tar.TypeDir, Mode: 0o755},
		tar.Header{Name: "workspace/empty/", Typeflag: tar.TypeDir, Mode: 0o755},
	)
	var output bufferCloser
	args := []string{"--archive-digest", digest, "--stopped-docker-archive-digest", digestOf(stoppedArchive)}
	err := run(context.Background(), args, io.NopCloser(bytes.NewReader(archive)), &output, nil, func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(stoppedArchive)), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := workspace.ApplyChanges(base, output.Bytes(), digestOf(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if _, err := result.Materialize(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(parent, "workspace"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "empty" || !entries[0].IsDir() {
		t.Fatal("artifact did not reconstruct the stopped workspace", err)
	}
}

func TestStoppedDockerExportRejectsUnverifiedArchives(t *testing.T) {
	archive, digest, _ := source(t)
	root := tar.Header{Name: "workspace/", Typeflag: tar.TypeDir, Mode: 0o755}
	valid := dockerArchive(t, root)
	for _, test := range []struct {
		name   string
		data   []byte
		digest string
		extra  []string
	}{
		{name: "missing root", data: archive},
		{name: "outside root", data: dockerArchive(t, root, tar.Header{Name: "outside"})},
		{name: "parent traversal", data: dockerArchive(t, root, tar.Header{Name: "workspace/../outside"})},
		{name: "duplicate root", data: dockerArchive(t, root, root)},
		{name: "hard link", data: dockerArchive(t, root, tar.Header{Name: "workspace/link", Typeflag: tar.TypeLink, Linkname: "workspace/original"})},
		{name: "escaping symlink", data: dockerArchive(t, root, tar.Header{Name: "workspace/link", Typeflag: tar.TypeSymlink, Linkname: "../outside"})},
		{name: "incomplete trailer", data: valid[:len(valid)-512]},
		{name: "data after trailer", data: append(bytes.Clone(valid), valid[:512]...)},
		{name: "wrong digest", data: valid, digest: digest},
		{name: "ambiguous destination", data: valid, extra: []string{"--destination", "/sandbox/workspace"}},
		{name: "duplicate digest", data: valid, extra: []string{"--stopped-docker-archive-digest", digestOf(valid)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stoppedDigest := test.digest
			if stoppedDigest == "" {
				stoppedDigest = digestOf(test.data)
			}
			args := append([]string{"--archive-digest", digest, "--stopped-docker-archive-digest", stoppedDigest}, test.extra...)
			var output bufferCloser
			err := run(context.Background(), args, io.NopCloser(bytes.NewReader(archive)), &output, nil, func() (io.ReadCloser, error) {
				if len(test.extra) > 0 {
					t.Fatal("ambiguous mode reached archive read")
				}
				return io.NopCloser(bytes.NewReader(test.data)), nil
			})
			if err == nil || output.Len() != 0 {
				t.Fatal("unverified stopped workspace produced an artifact")
			}
		})
	}
}

func dockerArchive(t *testing.T, headers ...tar.Header) []byte {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, header := range headers {
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
