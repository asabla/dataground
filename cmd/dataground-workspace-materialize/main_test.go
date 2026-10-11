package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/workspace"
)

func TestHelperValidatesBeforeInstallationAndReturnsOnlyReceipt(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "private-path", Mode: 0o600, Size: 7}); err != nil {
		t.Fatal(err)
	}
	writer.Write([]byte("private"))
	writer.Close()
	sum := sha256.Sum256(archive.Bytes())
	digest := "sha256:" + hex.EncodeToString(sum[:])
	var output bytes.Buffer
	called := false
	install := func(ctx context.Context, snapshot *workspace.Snapshot, parent string) (workspace.Receipt, error) {
		called = true
		if parent != "/sandbox" {
			t.Fatal("destination changed")
		}
		return snapshot.Receipt(), nil
	}
	if err := run(context.Background(), []string{"--archive-digest", digest}, io.NopCloser(bytes.NewReader(archive.Bytes())), &output, install); err != nil {
		t.Fatal(err)
	}
	if !called || strings.Contains(output.String(), "private") || !strings.Contains(output.String(), digest) {
		t.Fatal("invalid receipt or sensitive content disclosure")
	}
	for _, args := range [][]string{{"--archive-digest", digest, "--destination", "/tmp/workspace"}, {"--archive-digest", "sha256:" + strings.Repeat("0", 64)}, {"--archive-digest", digest, "extra"}} {
		called = false
		output.Reset()
		if err := run(context.Background(), args, io.NopCloser(bytes.NewReader(archive.Bytes())), &output, install); err == nil || called || output.Len() != 0 {
			t.Fatal("invalid input reached materialization")
		}
	}
}

func TestHelperCancelsBlockedInput(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--archive-digest", "sha256:" + strings.Repeat("0", 64)}, reader, io.Discard, func(context.Context, *workspace.Snapshot, string) (workspace.Receipt, error) {
			t.Error("cancelled read reached install")
			return workspace.Receipt{}, nil
		})
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled stdin remained blocked")
	}
}

func TestHelperReplaysOnlyAnExactExistingTree(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive.Bytes())
	digest := "sha256:" + hex.EncodeToString(sum[:])
	snapshot, err := workspace.Inspect(archive.Bytes(), digest)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	first, err := installSource(context.Background(), snapshot, parent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := installSource(context.Background(), snapshot, parent)
	if err != nil || first != second {
		t.Fatal("exact materialization replay changed", err)
	}
}
