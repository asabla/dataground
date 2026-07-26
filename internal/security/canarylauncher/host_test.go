package canarylauncher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/security/canaryharness"
)

const testRunID = "0123456789abcdef0123456789abcdef"

type hostCommandCall struct {
	environment []string
	binary string
	args []string
}

type hostCommandResult struct {
	output string
	err error
}

type fakeCommandRunner struct {
	calls []hostCommandCall
	results []hostCommandResult
}

func (runner *fakeCommandRunner) Run(
	_ context.Context,
	environment []string,
	binary string,
	args ...string,
) ([]byte, error) {
	runner.calls = append(runner.calls, hostCommandCall{
		environment: append([]string(nil), environment...),
		binary: binary,
		args: append([]string(nil), args...),
	})
	if len(runner.results) == 0 {
		return nil, errors.New("unexpected command")
	}
	result := runner.results[0]
	runner.results = runner.results[1:]
	return []byte(result.output), result.err
}

func TestComposeHostUsesRunBoundProjectAndObservesTeardown(t *testing.T) {
	names, err := canaryharness.NamesForRun(testRunID)
	if err != nil {
		t.Fatal(err)
	}
	containerID := strings.Repeat("a", 64)
	runner := &fakeCommandRunner{results: []hostCommandResult{
		{},
		{output: containerID + "\n"},
		{err: errors.New("lost down acknowledgement")},
		{},
		{},
	}}
	host, err := newComposeHost(
		testRunID,
		names,
		"/usr/bin/docker",
		"/repository/deploy/openshell/docker-compose.yml",
		runner,
	)
	if err != nil {
		t.Fatal(err)
	}
	host.wait = func(context.Context) error { return nil }

	actualID, err := host.Start(context.Background())
	if err != nil || actualID != containerID {
		t.Fatalf("Start() = %q, %v", actualID, err)
	}
	if err := host.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() did not recover lost acknowledgement: %v", err)
	}
	if len(runner.calls) != 5 {
		t.Fatalf("command count = %d", len(runner.calls))
	}
	if !reflect.DeepEqual(runner.calls[0].args, []string{
		"compose", "--project-name", "dg_canary_" + testRunID,
		"--file", "/repository/deploy/openshell/docker-compose.yml",
		"up", "--detach", "--remove-orphans",
	}) {
		t.Fatalf("compose up arguments = %#v", runner.calls[0].args)
	}
	if !reflect.DeepEqual(runner.calls[2].args, []string{
		"compose", "--project-name", "dg_canary_" + testRunID,
		"--file", "/repository/deploy/openshell/docker-compose.yml",
		"down", "--volumes", "--remove-orphans",
	}) {
		t.Fatalf("compose down arguments = %#v", runner.calls[2].args)
	}
	if !reflect.DeepEqual(runner.calls[4].args, []string{
		"volume", "ls", "--filter",
		"label=com.docker.compose.project=dg_canary_" + testRunID,
		"--quiet",
	}) {
		t.Fatalf("volume observation arguments = %#v", runner.calls[4].args)
	}
	if err := host.Stop(context.Background()); err != nil {
		t.Fatalf("idempotent Stop() error = %v", err)
	}
	if len(runner.calls) != 5 {
		t.Fatal("idempotent Stop() repeated native cleanup")
	}
}

func TestComposeHostOwnsAmbiguousStartForCleanup(t *testing.T) {
	names, err := canaryharness.NamesForRun(testRunID)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{results: []hostCommandResult{
		{err: errors.New("lost up acknowledgement")},
		{},
		{},
		{},
	}}
	host, err := newComposeHost(
		testRunID,
		names,
		"/usr/bin/docker",
		"/repository/deploy/openshell/docker-compose.yml",
		runner,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Start(context.Background()); !errors.Is(err, ErrLaunch) {
		t.Fatalf("Start() error = %v", err)
	}
	if err := host.Stop(context.Background()); err != nil {
		t.Fatalf("ambiguous start cleanup: %v", err)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("command count = %d", len(runner.calls))
	}
}

func TestLauncherEnvironmentsExcludeUnrelatedSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-cross")
	t.Setenv("PATH", "/trusted/bin")
	names, err := canaryharness.NamesForRun(testRunID)
	if err != nil {
		t.Fatal(err)
	}
	docker := strings.Join(dockerEnvironment(testRunID, names), "\n")
	openShell := strings.Join(openShellEnvironment(), "\n")
	if strings.Contains(docker, "must-not-cross") || strings.Contains(openShell, "must-not-cross") {
		t.Fatal("unrelated secret crossed into a child environment")
	}
	for _, expected := range []string{
		"DATAGROUND_CREDENTIAL_EVIDENCE_RUN_ID=" + testRunID,
		"DATAGROUND_CREDENTIAL_EVIDENCE_GATEWAY=" + names.Gateway,
		"DATAGROUND_CREDENTIAL_EVIDENCE_PROVIDER=" + names.Provider,
	} {
		if !strings.Contains(docker, expected) {
			t.Fatalf("Docker environment is missing %q", expected)
		}
	}
}

func TestPathsOverlapRejectsNestedLauncherState(t *testing.T) {
	root := t.TempDir()
	for _, candidate := range []string{
		root,
		filepath.Join(root, "state"),
	} {
		if !pathsOverlap(root, candidate) {
			t.Fatalf("overlap was not detected for %q", candidate)
		}
	}
	if pathsOverlap(filepath.Join(root, "repository"), filepath.Join(root, "state")) {
		t.Fatal("disjoint sibling paths were rejected")
	}
}

func TestReadVerifiedFileRejectsContentAndSymlinkDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "topology")
	content := []byte("checked topology\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256Hex(content)
	actual, err := readVerifiedFile(path, digest)
	if err != nil || string(actual) != string(content) {
		t.Fatalf("readVerifiedFile() = %q, %v", actual, err)
	}
	clear(actual)

	if err := os.WriteFile(path, []byte("modified topology\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readVerifiedFile(path, digest); !errors.Is(err, ErrTopologyDrift) {
		t.Fatalf("modified topology error = %v", err)
	}

	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readVerifiedFile(link, digest); !errors.Is(err, ErrTopologyDrift) {
		t.Fatalf("symlink topology error = %v", err)
	}
}
