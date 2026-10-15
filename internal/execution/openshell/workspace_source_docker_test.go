package openshell

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/workspace"
	"golang.org/x/crypto/ssh"
)

// This runs the real Linux helper through the adapter's SSH transport. The SSH
// server is a controlled test peer; gateway authorization is tested separately.
func TestWorkspaceSourceDockerTransfer(t *testing.T) {
	base := os.Getenv("DATAGROUND_TEST_WORKSPACE_SOURCE_BASE_IMAGE")
	if base == "" {
		t.Skip("workspace source Docker base image not configured")
	}
	if !workspaceArchiveDigestPattern.MatchString(base) && !(strings.Contains(base, "@") && workspaceArchiveDigestPattern.MatchString(strings.SplitN(base, "@", 2)[1])) {
		t.Fatal("test image must be digest pinned")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	docker := func(args ...string) []byte {
		t.Helper()
		output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("Docker fixture command failed: %s: %v", output, err)
		}
		return output
	}
	// Resolve to one local immutable image ID before building the helper layer.
	if !workspaceArchiveDigestPattern.MatchString(base) {
		docker("pull", base)
	}
	var images []struct {
		ID           string `json:"Id"`
		Architecture string
	}
	if err := json.Unmarshal(docker("image", "inspect", base), &images); err != nil || len(images) != 1 {
		t.Fatal("cannot inspect pinned image")
	}
	if images[0].Architecture != "amd64" && images[0].Architecture != "arm64" {
		t.Fatal("unsupported fixture architecture")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	build := t.TempDir()
	binary := filepath.Join(build, "dataground-workspace-materialize")
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", binary, "./cmd/dataground-workspace-materialize")
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+images[0].Architecture)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("helper build: %s: %v", output, err)
	}
	dockerfile, err := os.ReadFile(filepath.Join(root, "deploy/openshell/workspace-source/Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(build, "Dockerfile"), dockerfile, 0600); err != nil {
		t.Fatal(err)
	}
	tag := "dataground-workspace-transfer-" + strings.ToLower(rand.Text())
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, "docker", "rm", "--force", tag).Run()
		_ = exec.CommandContext(cleanup, "docker", "image", "rm", tag, tag+"-base").Run()
	})
	docker("tag", images[0].ID, tag+"-base")
	docker("build", "--network", "none", "--build-arg", "RUNTIME_IMAGE="+tag+"-base", "--tag", tag, build)
	docker("run", "--detach", "--name", tag, "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--memory", "256m", "--tmpfs", "/sandbox:rw,mode=0700,uid=998,gid=998", "--entrypoint", "/bin/sleep", tag, "infinity")
	source, archive := transferSourceFixture(t)
	// Verify that the same digest survives actual process stdin and Linux install.
	transfer := func(lost bool) (workspace.Receipt, error) {
		t.Helper()
		proxy := newSSHCommandTestProxy(t, workspaceMaterializationCommand+source.Receipt().ArchiveDigest, func(channel ssh.Channel) {
			command := exec.CommandContext(ctx, "docker", "exec", "--interactive", tag, "/usr/local/bin/dataground-workspace-materialize", "--destination", "/sandbox/workspace", "--archive-digest", source.Receipt().ArchiveDigest)
			command.Stdin = channel
			command.Stderr = channel.Stderr()
			if lost {
				command.Stdout = io.Discard
			} else {
				command.Stdout = channel
			}
			err := command.Run()
			if lost {
				_ = channel.Close()
				return
			}
			status := uint32(0)
			if err != nil {
				status = 1
			}
			finishSSHTest(channel, status)
		})
		provider, ref, _ := sourceTransferProvider(t, proxy)
		return provider.MaterializeWorkspace(ctx, ref, source)
	}
	if got, err := transfer(true); err == nil || got != (workspace.Receipt{}) {
		t.Fatal("lost helper acknowledgement reported success")
	}
	if got, err := transfer(false); err != nil || got != source.Receipt() {
		t.Fatalf("helper replay failed: %v", err)
	}
	if got, err := transfer(false); err != nil || got != source.Receipt() {
		t.Fatalf("second helper replay failed: %v", err)
	}
	content := docker("exec", tag, "/bin/cat", "/sandbox/workspace/src/file.bin")
	if !bytes.Equal(content, []byte("source\x00\xff\n")) {
		t.Fatal("Linux materialization changed binary content")
	}
	if string(bytes.TrimSpace(docker("exec", tag, "stat", "-c", "%a", "/sandbox/workspace/src/file.bin"))) != "700" {
		t.Fatal("Linux executable mode changed")
	}
	docker("exec", tag, "/bin/sh", "-c", "printf changed > /sandbox/workspace/src/file.bin")
	if got, err := transfer(false); err == nil || got != (workspace.Receipt{}) {
		t.Fatal("modified workspace accepted during replay")
	}
	if string(docker("exec", tag, "/bin/cat", "/sandbox/workspace/src/file.bin")) != "changed" {
		t.Fatal("failed replay overwrote changed workspace")
	}
	var retained bytes.Buffer
	if err := source.WriteArchive(ctx, &retained); err != nil || !bytes.Equal(retained.Bytes(), archive) {
		t.Fatal("transfer changed control-plane snapshot")
	}
	t.Logf("verified Linux %s helper installation and lost-acknowledgement replay", images[0].Architecture)
}
