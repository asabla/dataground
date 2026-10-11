package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installationArgs() []string {
	return []string{"--archive-file", "/unread/source.tar", "--archive-digest", "sha256:" + strings.Repeat("a", 64), "--isolation-domain", "iso_" + strings.Repeat("1", 20), "--workspace", "wsp_" + strings.Repeat("2", 20), "--source", "src_" + strings.Repeat("3", 20), "--actor", "operator", "--correlation-id", "cor_" + strings.Repeat("4", 20), "--storage-endpoint", "http://127.0.0.1:9000", "--storage-bucket", "sources", "--storage-profile-digest", "sha256:" + strings.Repeat("b", 64)}
}
func TestSourceInstallerRequiresReviewedScopeAndLoopbackStorage(t *testing.T) {
	if _, err := parseOptions(installationArgs()); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"https://127.0.0.1:9000", "http://localhost:9000", "http://127.0.0.1:9000/path", "http://user:secret@127.0.0.1:9000", "http://192.0.2.1:9000", "http://127.0.0.1:9000?", "http://127.0.0.1:9000#fragment"} {
		args := installationArgs()
		args[15] = endpoint
		if _, err := parseOptions(args); err == nil {
			t.Fatal("unreviewed endpoint accepted")
		}
	}
	for _, args := range [][]string{installationArgs()[:18], append(installationArgs(), "extra"), append(installationArgs(), "--actor", "replacement")} {
		if _, err := parseOptions(args); err == nil {
			t.Fatal("incomplete or duplicate input accepted")
		}
	}
}
func TestSourceInstallerRejectsUnsafeFilesBeforeDatabaseAccess(t *testing.T) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source.tar")
	if err := os.WriteFile(path, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readArchive(path); err != nil || !bytes.Equal(got, archive.Bytes()) {
		t.Fatal("private source read failed", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readArchive(link); err == nil {
		t.Fatal("source symlink accepted")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readArchive(path); err == nil {
		t.Fatal("shared source accepted")
	}
	os.Chmod(path, 0o600)
	hardlink := filepath.Join(filepath.Dir(path), "hardlink")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readArchive(path); err == nil {
		t.Fatal("multiply linked source accepted")
	}
	os.Remove(hardlink)
	sum := sha256.Sum256(archive.Bytes())
	args := installationArgs()
	args[1] = path
	args[3] = "sha256:" + hex.EncodeToString(sum[:])
	t.Setenv("DATAGROUND_DATABASE_URL", "invalid database must not be contacted")
	if err := run(context.Background(), args, io.Discard); err == nil || strings.Contains(err.Error(), "database") {
		t.Fatal("wrong storage profile passed preflight", err)
	}
}
