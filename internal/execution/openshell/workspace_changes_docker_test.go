package openshell

import (
	"bufio"
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

// The controlled SSH peer tests either an exact candidate or disposable helper
// layer. It does not establish gateway authorization. The test owns all writers.
func TestWorkspaceChangeDockerExport(t *testing.T) {
	base := os.Getenv("DATAGROUND_TEST_WORKSPACE_CHANGE_BASE_IMAGE")
	image := os.Getenv("DATAGROUND_TEST_WORKSPACE_CHANGE_IMAGE")
	if image != "" && base != "" {
		t.Fatal("select either an exact change helper image or a base")
	}
	buildHelper := image == ""
	if !buildHelper {
		base = image
	}
	if base == "" {
		t.Skip("workspace change Docker base image not configured")
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
			t.Fatalf("Docker fixture failed: %s: %v", output, err)
		}
		return output
	}
	if !workspaceArchiveDigestPattern.MatchString(base) {
		docker("pull", base)
	}
	var images []struct {
		ID           string `json:"Id"`
		Architecture string
	}
	if err := json.Unmarshal(docker("image", "inspect", base), &images); err != nil || len(images) != 1 || (images[0].Architecture != "amd64" && images[0].Architecture != "arm64") {
		t.Fatal("invalid local image")
	}
	tag := "dataground-workspace-changes-" + strings.ToLower(rand.Text())
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, "docker", "rm", "--force", tag).Run()
		_ = exec.CommandContext(cleanup, "docker", "image", "rm", tag, tag+"-base").Run()
	})
	if buildHelper {
		root, err := filepath.Abs("../../..")
		if err != nil {
			t.Fatal(err)
		}
		build := t.TempDir()
		for _, helper := range []string{"dataground-workspace-materialize", "dataground-workspace-export"} {
			command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", filepath.Join(build, helper), "./cmd/"+helper)
			command.Dir = root
			command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+images[0].Architecture)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("helper build: %s: %v", output, err)
			}
		}
		dockerfile := []byte("ARG RUNTIME_IMAGE\nFROM ${RUNTIME_IMAGE}\nUSER root\nCOPY --chmod=0555 dataground-workspace-materialize dataground-workspace-export /usr/local/bin/\nUSER 998:998\nWORKDIR /sandbox\n")
		if err := os.WriteFile(filepath.Join(build, "Dockerfile"), dockerfile, 0o600); err != nil {
			t.Fatal(err)
		}
		docker("tag", images[0].ID, tag+"-base")
		docker("build", "--network", "none", "--build-arg", "RUNTIME_IMAGE="+tag+"-base", "--tag", tag, build)
	} else {
		docker("tag", images[0].ID, tag)
		docker("run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/bin/sh", tag, "-c", `
 test "$(id -u):$(id -g)" = "998:998" &&
 test "$(stat -c '%u:%g:%a' /usr/local/bin)" = "0:0:755" &&
 for helper in dataground-workspace-materialize dataground-workspace-export dataground-native-owner; do
 test "$(stat -c '%u:%g:%a' /usr/local/bin/$helper)" = "0:0:555" || exit 1
 done`)
	}

	docker("run", "--detach", "--name", tag, "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--memory", "512m", "--tmpfs", "/sandbox:rw,noexec,nosuid,nodev,mode=0700,uid=998,gid=998", "--entrypoint", "/bin/sleep", tag, "infinity")
	source, archive, expected, encoded := changeTransferFixture(t)
	install := exec.CommandContext(ctx, "docker", "exec", "--interactive", tag, "/usr/local/bin/dataground-workspace-materialize", "--archive-digest", source.Receipt().ArchiveDigest)
	install.Stdin = bytes.NewReader(archive)
	if output, err := install.CombinedOutput(); err != nil {
		t.Fatalf("materialize: %s: %v", output, err)
	}
	if !buildHelper {
		docker("exec", tag, "/bin/mkdir", "-m", "0700", "/sandbox/.codex")
		verifyCandidateNativeOwner(t, ctx, tag)
	}
	docker("exec", tag, "/bin/sh", "-c", `printf 'changed\000\376' > /sandbox/workspace/src/file.bin && mkdir /sandbox/workspace/empty`)
	transfer := func(lost bool) (*workspace.ChangeSet, error) {
		proxy := newSSHCommandTestProxy(t, workspaceChangeExportCommand+source.Receipt().ArchiveDigest, func(channel ssh.Channel) {
			command := exec.CommandContext(ctx, "docker", "exec", "--interactive", tag, "/usr/local/bin/dataground-workspace-export", "--destination", "/sandbox/workspace", "--archive-digest", source.Receipt().ArchiveDigest)
			command.Stdin = channel
			command.Stdout = channel
			command.Stderr = channel.Stderr()
			if lost {
				command.Stdout = io.Discard
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
		return provider.ExportWorkspaceChanges(ctx, ref, source)
	}
	if result, err := transfer(true); result != nil || err == nil {
		t.Fatal("lost acknowledgement confirmed export")
	}
	for range 2 {
		result, err := transfer(false)
		if err != nil || result == nil || result.Receipt() != expected.Receipt() {
			t.Fatal("Linux change export mismatch", err)
		}
		var actual bytes.Buffer
		if err := result.Write(ctx, &actual); err != nil || !bytes.Equal(actual.Bytes(), encoded) {
			t.Fatal("Linux artifact bytes mismatch", err)
		}
	}
	docker("exec", tag, "/bin/ln", "-s", "/etc/passwd", "/sandbox/workspace/unsafe")
	if result, err := transfer(false); result != nil || err == nil {
		t.Fatal("unsafe Linux workspace exported")
	}
	if data := docker("exec", tag, "/bin/cat", "/sandbox/workspace/src/file.bin"); !bytes.Equal(data, []byte("changed\x00\xfe")) {
		t.Fatal("export changed workspace")
	}
	t.Logf("verified Linux %s helper export and lost-acknowledgement recovery with a controlled SSH peer", images[0].Architecture)
}

// Exercise the installed owner and actual Codex binary without provider access.
// Descendant and strict-gateway tests cover the stronger cleanup invariants.
func verifyCandidateNativeOwner(t *testing.T, ctx context.Context, container string) {
	t.Helper()
	proxy := newSSHCommandTestProxy(t, "/usr/local/bin/dataground-native-owner", func(channel ssh.Channel) {
		command := exec.CommandContext(ctx, "docker", "exec", "--interactive", "--workdir", "/sandbox/workspace", "--env", "CODEX_HOME=/sandbox/.codex", container, "/usr/local/bin/dataground-native-owner")
		command.Stdin, command.Stdout, command.Stderr = channel, channel, channel.Stderr()
		status := uint32(0)
		if command.Run() != nil {
			status = 1
		}
		finishSSHTest(channel, status)
	})
	provider, ref, _ := sourceTransferProvider(t, proxy)
	session, err := provider.StartOwnedRuntime(ctx, ref)
	if err != nil {
		t.Fatal("candidate owner startup", err)
	}
	defer session.Close()
	go func() { _, _ = io.Copy(io.Discard, session.Errors()) }()
	if _, err := io.WriteString(session.Input(), `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"dataground-candidate-test","version":"1"}}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(session.Output())
	scanner.Buffer(make([]byte, 4096), 64<<10)
	if !scanner.Scan() {
		t.Fatal("candidate native initialization failed", scanner.Err())
	}
	var response struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(scanner.Bytes(), &response) != nil || response.ID != 1 || len(response.Result) == 0 || len(response.Error) != 0 {
		t.Fatal("candidate native initialization response invalid")
	}
	go func() { _, _ = io.Copy(io.Discard, session.Output()) }()
	if err := session.Quiesce(ctx); err != nil {
		t.Fatal("candidate owner cleanup", err)
	}
	binding, err := session.OwnerBinding()
	if err != nil {
		t.Fatal(err)
	}
	if confirmed, err := session.ConfirmedOwnerBinding(); err != nil || confirmed != binding {
		t.Fatal("candidate owner binding was not confirmed")
	}
	if err := session.Quiesce(ctx); err != nil {
		t.Fatal("candidate cleanup replay", err)
	}
}
