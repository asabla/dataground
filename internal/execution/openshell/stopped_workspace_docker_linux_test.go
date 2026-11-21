package openshell

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/workspace"
)

const stoppedWorkspacePath = "/tmp/dataground-stopped/workspace"

var (
	errStoppedWorkspaceSealed   = errors.New("fixture has a retained stop intent")
	errStoppedWorkspaceMismatch = errors.New("fixture stopped container identity is unconfirmed")
	dockerContainerID           = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// This fixture is the sole Docker lifecycle authority. Each controller opens
// the same private lock and intent, including after an uncertain response.
// Docker administrators retain the ability to restart or remove a container.
func TestWorkspaceStoppedDockerExport(t *testing.T) {
	image := os.Getenv("DATAGROUND_TEST_STOPPED_WORKSPACE_IMAGE")
	if image == "" {
		t.Skip("stopped workspace Docker image not configured")
	}
	imageRef := strings.SplitN(image, "@", 2)
	if len(imageRef) != 2 || !workspaceArchiveDigestPattern.MatchString(imageRef[1]) {
		t.Fatal("stopped workspace image must be digest pinned")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	docker := func(stdin io.Reader, args ...string) []byte {
		t.Helper()
		output, err := runStoppedWorkspaceDocker(ctx, stdin, args...)
		if err != nil {
			t.Fatalf("Docker %s: %v", args[0], err)
		}
		return output
	}
	docker(nil, "pull", image)
	name := "dataground-stopped-workspace-" + strings.ToLower(rand.Text())
	created := docker(nil, "create", "--name", name, "--user", "998:998", "--network", "none",
		"--restart=no", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", "64", "--memory", "512m", "--entrypoint", "/bin/sleep", image, "infinity")
	id := strings.TrimSpace(string(created))
	if !dockerContainerID.MatchString(id) {
		t.Fatal("Docker did not return an exact container identity")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if _, err := runStoppedWorkspaceDocker(cleanup, nil, "rm", "--force", id); err != nil {
			t.Errorf("remove the disposable Docker container: %v", err)
		}
	})
	dir := t.TempDir()
	first := stoppedDockerWorkspace{id: id, directory: dir, run: runStoppedWorkspaceDocker}
	if err := first.start(ctx); err != nil {
		t.Fatal(err)
	}
	base, archive, expected, encoded := changeTransferFixture(t)
	docker(bytes.NewReader(archive), "exec", "--interactive", id, "/bin/sh", "-c",
		`umask 077; mkdir -p "$1" && tar -xf - -C "$1" && printf 'changed\000\376' > "$1/src/file.bin" && mkdir "$1/empty"`,
		"workspace-fixture", stoppedWorkspacePath)
	docker(nil, "exec", "--detach", id, "/bin/sh", "-c",
		`while :; do printf 'active' > /tmp/dataground-stopped/writer; sleep 0.05; done`)
	docker(nil, "exec", id, "/bin/sh", "-c",
		`for i in $(seq 1 20); do test -s /tmp/dataground-stopped/writer && exit 0; sleep 0.05; done; exit 1`)
	if _, err := first.export(ctx, base); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsealed workspace reached capture: %v", err)
	}

	lost := errors.New("response lost after Docker completed the request")
	replacement := stoppedDockerWorkspace{id: id, directory: dir, run: func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		if args[0] == "stop" {
			t.Error("replacement issued another stop after Docker had completed it")
		}
		return runStoppedWorkspaceDocker(ctx, stdin, args...)
	}}
	first.run = func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		if args[0] == "stop" {
			if err := replacement.start(ctx); !errors.Is(err, errPolicyWorkspaceLocked) {
				t.Fatalf("concurrent start crossed the stop lock: %v", err)
			}
			if _, err := runStoppedWorkspaceDocker(ctx, stdin, args...); err != nil {
				return nil, err
			}
			return nil, lost
		}
		return runStoppedWorkspaceDocker(ctx, stdin, args...)
	}
	if err := first.stop(ctx); !errors.Is(err, lost) {
		t.Fatalf("expected an uncertain stop response: %v", err)
	}
	for _, controller := range []stoppedDockerWorkspace{first, replacement} {
		if err := controller.start(ctx); !errors.Is(err, errStoppedWorkspaceSealed) {
			t.Fatalf("controller accepted restart after the stop response was lost: %v", err)
		}
	}
	if err := replacement.stop(ctx); err != nil {
		t.Fatalf("observe the original stopped container: %v", err)
	}
	first.run = func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		if args[0] == "cp" {
			if err := replacement.start(ctx); !errors.Is(err, errPolicyWorkspaceLocked) {
				t.Fatalf("concurrent start crossed the archive lock: %v", err)
			}
		}
		output, err := runStoppedWorkspaceDocker(ctx, stdin, args...)
		if args[0] == "cp" && err == nil {
			return nil, lost
		}
		return output, err
	}
	if changes, err := first.export(ctx, base); changes != nil || !errors.Is(err, lost) {
		t.Fatalf("a lost archive response produced changes: %v", err)
	}
	for range 2 {
		changes, err := replacement.export(ctx, base)
		if err != nil || changes.Receipt() != expected.Receipt() {
			t.Fatalf("stopped layer did not match the expected final tree: %v", err)
		}
		var actual bytes.Buffer
		if err := changes.Write(ctx, &actual); err != nil || !bytes.Equal(actual.Bytes(), encoded) {
			t.Fatalf("recovered artifact bytes changed: %v", err)
		}
		result, err := workspace.ApplyChanges(base, actual.Bytes(), changes.Receipt().Digest)
		if err != nil || result.Receipt().ArchiveDigest != expected.Receipt().ResultArchiveDigest {
			t.Fatalf("recovered changes did not reconstruct the expected tree: %v", err)
		}
	}
	if err := replacement.start(ctx); !errors.Is(err, errStoppedWorkspaceSealed) {
		t.Fatalf("verified export released the restart fence: %v", err)
	}

	// A trusted daemon administrator can restart the container outside the
	// fixture. The original record must fail even after another stop.
	docker(nil, "start", id)
	docker(nil, "stop", "--time", "1", id)
	if changes, err := replacement.export(ctx, base); changes != nil || !errors.Is(err, errStoppedWorkspaceMismatch) {
		t.Fatalf("a new container generation was adopted: %v", err)
	}
	t.Log("verified stopped-container export, restart denial, and recovery after lost responses")
}

type stoppedDockerWorkspace struct {
	id, directory string
	run           func(context.Context, io.Reader, ...string) ([]byte, error)
}

type stoppedDockerObservation struct {
	ID         string
	AutoRemove bool
	Restart    string
	HasMounts  bool
	State      struct {
		Status                string
		Running, Restarting   bool
		Pid                   int
		StartedAt, FinishedAt string
	}
}

func (fixture stoppedDockerWorkspace) inspect(ctx context.Context) (stoppedDockerObservation, error) {
	const format = `{"ID":{{json .Id}},"State":{{json .State}},"Restart":{{json .HostConfig.RestartPolicy.Name}},"AutoRemove":{{json .HostConfig.AutoRemove}},"HasMounts":{{if .Mounts}}true{{else}}false{{end}}}`
	data, err := fixture.run(ctx, nil, "inspect", "--type", "container", "--format", format, fixture.id)
	var observation stoppedDockerObservation
	if err != nil {
		return observation, err
	}
	if err := json.Unmarshal(data, &observation); err != nil || observation.ID != fixture.id ||
		observation.AutoRemove || observation.Restart != "no" || observation.HasMounts {
		return stoppedDockerObservation{}, errStoppedWorkspaceMismatch
	}
	return observation, nil
}

func (observation stoppedDockerObservation) stopped() bool {
	return observation.State.Status == "exited" && !observation.State.Running && !observation.State.Restarting && observation.State.Pid == 0
}

func (observation stoppedDockerObservation) intent() []byte {
	return []byte(observation.ID + "\n" + observation.State.StartedAt + "\n")
}

func (fixture stoppedDockerWorkspace) withLock(effect func(path string) error) error {
	file, err := os.OpenFile(filepath.Join(fixture.directory, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := lockPolicyWorkspace(file); err != nil {
		return err
	}
	return effect(filepath.Join(fixture.directory, "stop-intent"))
}

func (fixture stoppedDockerWorkspace) start(ctx context.Context) error {
	return fixture.withLock(func(path string) error {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return errStoppedWorkspaceSealed
		}
		if observation, err := fixture.inspect(ctx); err != nil || (observation.State.Status != "created" && !observation.stopped()) {
			return errors.Join(errStoppedWorkspaceMismatch, err)
		}
		_, err := fixture.run(ctx, nil, "start", fixture.id)
		return err
	})
}

func (fixture stoppedDockerWorkspace) stop(ctx context.Context) error {
	return fixture.withLock(func(path string) error {
		observation, err := fixture.inspect(ctx)
		if err != nil {
			return err
		}
		intent, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) && observation.State.Running && !observation.State.Restarting {
			// A partial intent still blocks start and cannot confirm an export.
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			intent = observation.intent()
			_, writeErr := file.Write(intent)
			if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if !bytes.Equal(intent, observation.intent()) {
			return errStoppedWorkspaceMismatch
		}
		if observation.stopped() {
			return nil
		}
		if !observation.State.Running || observation.State.Restarting {
			return errStoppedWorkspaceMismatch
		}
		if _, err := fixture.run(ctx, nil, "stop", "--time", "1", fixture.id); err != nil {
			return err
		}
		stopped, err := fixture.inspect(ctx)
		if err != nil || !stopped.stopped() || !bytes.Equal(intent, stopped.intent()) {
			return errors.Join(errStoppedWorkspaceMismatch, err)
		}
		return nil
	})
}

func (fixture stoppedDockerWorkspace) export(ctx context.Context, base *workspace.Snapshot) (*workspace.ChangeSet, error) {
	var changes *workspace.ChangeSet
	err := fixture.withLock(func(path string) error {
		intent, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		before, err := fixture.inspect(ctx)
		if err != nil || !before.stopped() || !bytes.Equal(intent, before.intent()) {
			return errors.Join(errStoppedWorkspaceMismatch, err)
		}
		archive, err := fixture.run(ctx, nil, "cp", fixture.id+":"+stoppedWorkspacePath, "-")
		if err != nil {
			return err
		}
		after, err := fixture.inspect(ctx)
		if err != nil || !after.stopped() || after != before {
			return errors.Join(errStoppedWorkspaceMismatch, err)
		}
		snapshot, err := inspectDockerWorkspaceArchive(archive)
		if err != nil {
			return err
		}
		changes, err = workspace.Compare(base, snapshot)
		return err
	})
	return changes, err
}

func inspectDockerWorkspaceArchive(archive []byte) (*workspace.Snapshot, error) {
	raw := bytes.NewReader(archive)
	reader := tar.NewReader(raw)
	root, err := reader.Next()
	if err != nil || strings.TrimSuffix(root.Name, "/") != "workspace" || root.Typeflag != tar.TypeDir || root.Size != 0 {
		return nil, workspace.ErrSourceInvalid
	}
	var result bytes.Buffer
	writer := tar.NewWriter(&result)
	for entries := 0; ; entries++ {
		offset := len(archive) - raw.Len()
		header, err := reader.Next()
		if err == io.EOF {
			end := (offset + 511) / 512 * 512
			if len(archive)-end < 1024 || len(bytes.Trim(archive[end:], "\x00")) != 0 {
				return nil, workspace.ErrSourceInvalid
			}
			break
		}
		if err != nil || entries >= workspace.MaximumEntries {
			return nil, workspace.ErrSourceInvalid
		}
		name, scoped := strings.CutPrefix(header.Name, "workspace/")
		if !scoped || name == "" {
			return nil, workspace.ErrSourceInvalid
		}
		for key := range header.PAXRecords {
			if strings.HasPrefix(key, "GNU.sparse") {
				return nil, workspace.ErrSourceInvalid
			}
		}
		projected := tar.Header{Name: name, Typeflag: header.Typeflag, Linkname: header.Linkname, Mode: header.Mode, Size: header.Size}
		if err := writer.WriteHeader(&projected); err != nil {
			return nil, err
		}
		if _, err := io.Copy(writer, reader); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return workspace.Inspect(result.Bytes(), fmt.Sprintf("sha256:%x", sha256.Sum256(result.Bytes())))
}

func runStoppedWorkspaceDocker(ctx context.Context, input io.Reader, args ...string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdin = input
	output, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(output, workspace.MaximumArchiveBytes+1))
	if err != nil || len(data) > workspace.MaximumArchiveBytes {
		cancel()
		_ = command.Wait()
		return nil, errors.Join(workspace.ErrSourceInvalid, err)
	}
	if err := command.Wait(); err != nil {
		return nil, err
	}
	return data, nil
}
