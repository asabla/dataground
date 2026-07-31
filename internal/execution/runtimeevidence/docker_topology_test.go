package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type dockerTopologyCall struct {
	environment []string
	binary      string
	args        []string
}

type dockerTopologyResult struct {
	output string
	err    error
}

type fakeDockerTopologyRunner struct {
	mu      sync.Mutex
	calls   []dockerTopologyCall
	results []dockerTopologyResult
	block   chan struct{}
	entered chan struct{}
}

func (runner *fakeDockerTopologyRunner) Run(
	_ context.Context,
	environment []string,
	binary string,
	args ...string,
) ([]byte, error) {
	runner.mu.Lock()
	runner.calls = append(runner.calls, dockerTopologyCall{
		environment: append([]string(nil), environment...),
		binary:      binary,
		args:        append([]string(nil), args...),
	})
	if len(runner.results) == 0 {
		runner.mu.Unlock()
		return nil, errors.New("unexpected command")
	}
	result := runner.results[0]
	runner.results = runner.results[1:]
	block := runner.block
	entered := runner.entered
	runner.block = nil
	runner.entered = nil
	runner.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if block != nil {
		<-block
	}
	return []byte(result.output), result.err
}

func TestDockerTopologyStartsExactProjectAndObservesCleanup(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-cross")
	containerID := strings.Repeat("a", 64)
	runner := &fakeDockerTopologyRunner{results: []dockerTopologyResult{
		{},
		{output: containerID + "\n"},
		{output: runtimeTopologyInspection(testRunID, containerID)},
		{err: errors.New("lost down acknowledgement")},
		{},
		{},
	}}
	topology, workspaceRoot := newTestDockerTopology(t, runner, func(context.Context) error {
		return nil
	})
	if err := topology.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := topology.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}

	runner.mu.Lock()
	calls := append([]dockerTopologyCall(nil), runner.calls...)
	runner.mu.Unlock()
	if len(calls) != 6 {
		t.Fatalf("command count = %d", len(calls))
	}
	project := "dg_runtime_" + testRunID
	if !reflect.DeepEqual(calls[0].args[:4], []string{
		"compose", "--project-name", project, "--file",
	}) || calls[0].args[5] != "up" {
		t.Fatalf("compose up arguments = %#v", calls[0].args)
	}
	if !reflect.DeepEqual(calls[3].args[len(calls[3].args)-3:], []string{
		"down", "--volumes", "--remove-orphans",
	}) {
		t.Fatalf("compose down arguments = %#v", calls[3].args)
	}
	if !reflect.DeepEqual(calls[5].args, []string{
		"volume", "ls", "--filter",
		"label=com.docker.compose.project=" + project,
		"--quiet",
	}) {
		t.Fatalf("volume observation arguments = %#v", calls[5].args)
	}
	environment := strings.Join(calls[0].environment, "\n")
	if strings.Contains(environment, "must-not-cross") {
		t.Fatal("unrelated secret crossed into Docker environment")
	}
	for _, expected := range []string{
		"DATAGROUND_RUNTIME_CONFORMANCE_RUN_ID=" + testRunID,
		"DATAGROUND_RUNTIME_CONFORMANCE_GATEWAY=dg-runtime-gateway-" + testRunID,
		"DATAGROUND_RUNTIME_CONFORMANCE_PROVIDER=dg-runtime-provider-" + testRunID,
	} {
		if !strings.Contains(environment, expected) {
			t.Fatalf("Docker environment is missing %q", expected)
		}
	}
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("topology workspace was not removed: %v, %#v", err, entries)
	}
	if err := topology.Cleanup(context.Background()); err != nil {
		t.Fatalf("idempotent Cleanup() error = %v", err)
	}
	if len(runner.calls) != 6 {
		t.Fatal("idempotent cleanup repeated native mutation")
	}
}

func TestDockerTopologyRecoversAmbiguousStart(t *testing.T) {
	runner := &fakeDockerTopologyRunner{results: []dockerTopologyResult{
		{err: errors.New("lost up acknowledgement")},
		{},
		{},
		{},
	}}
	topology, _ := newTestDockerTopology(t, runner, func(context.Context) error {
		return nil
	})
	if err := topology.Start(context.Background()); !errors.Is(err, ErrDockerTopologyStart) {
		t.Fatalf("Start() error = %v", err)
	}
	if err := topology.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() after ambiguous start = %v", err)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("command count = %d", len(runner.calls))
	}
}

func TestDockerTopologyRejectsContainerBindingDrift(t *testing.T) {
	containerID := strings.Repeat("b", 64)
	runner := &fakeDockerTopologyRunner{results: []dockerTopologyResult{
		{},
		{output: containerID + "\n"},
		{output: strings.Replace(
			runtimeTopologyInspection(testRunID, containerID),
			gatewayImage,
			"untrusted:latest",
			1,
		)},
		{},
		{},
		{},
	}}
	topology, _ := newTestDockerTopology(t, runner, func(context.Context) error {
		return nil
	})
	if err := topology.Start(context.Background()); !errors.Is(err, ErrDockerTopologyStart) {
		t.Fatalf("Start() error = %v", err)
	}
	if err := topology.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
}

func TestDockerTopologyOverlapPoisonsStartButPreservesCleanup(t *testing.T) {
	containerID := strings.Repeat("c", 64)
	entered := make(chan struct{})
	release := make(chan struct{})
	runner := &fakeDockerTopologyRunner{
		entered: entered,
		block:   release,
		results: []dockerTopologyResult{
			{},
			{output: containerID + "\n"},
			{output: runtimeTopologyInspection(testRunID, containerID)},
			{},
			{},
			{},
		},
	}
	topology, _ := newTestDockerTopology(t, runner, func(context.Context) error {
		return nil
	})
	first := make(chan error, 1)
	go func() {
		first <- topology.Start(context.Background())
	}()
	<-entered
	copy := *topology
	if err := copy.Start(context.Background()); !errors.Is(err, ErrDockerTopologyOrder) {
		t.Fatalf("overlap Start() error = %v", err)
	}
	close(release)
	if err := <-first; !errors.Is(err, ErrDockerTopologyOrder) {
		t.Fatalf("first Start() error = %v", err)
	}
	if err := topology.Cleanup(context.Background()); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
}

func TestDockerTopologyRejectsSourceDriftAndSerialization(t *testing.T) {
	repositoryRoot := runtimeTopologyRepositoryRoot(t)
	path := filepath.Join(
		repositoryRoot,
		filepath.FromSlash(runtimeTopologyComposePath),
	)
	content, err := readRuntimeTopologyFile(path, runtimeComposeSHA256)
	if err != nil {
		t.Fatalf("readRuntimeTopologyFile() error = %v", err)
	}
	clear(content)
	if _, err := readRuntimeTopologyFile(path, strings.Repeat("0", 64)); !errors.Is(err, ErrDockerTopologyDrift) {
		t.Fatalf("drift error = %v", err)
	}

	config := DockerTopologyConfig{RunID: testRunID}
	if _, err := json.Marshal(config); !errors.Is(err, ErrSerialization) {
		t.Fatalf("config MarshalJSON() error = %v", err)
	}
	if _, err := json.Marshal(DockerTopology{}); !errors.Is(err, ErrSerialization) {
		t.Fatalf("topology MarshalJSON() error = %v", err)
	}
}

func newTestDockerTopology(
	t *testing.T,
	runner dockerTopologyRunner,
	wait func(context.Context) error,
) (*DockerTopology, string) {
	t.Helper()
	workspaceRoot := t.TempDir()
	if err := os.Chmod(workspaceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	topology, err := newDockerTopology(
		DockerTopologyConfig{
			RunID:          testRunID,
			RepositoryRoot: runtimeTopologyRepositoryRoot(t),
			WorkspaceRoot:  workspaceRoot,
			DockerBinary:   "/usr/bin/docker",
		},
		dockerTopologyDependencies{
			runner: runner,
			wait:   wait,
			resolveBinary: func(value string) (string, error) {
				return value, nil
			},
			processIdentity: func() (int, int, int, error) {
				return 1000, 1000, 999, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("newDockerTopology() error = %v", err)
	}
	t.Cleanup(func() {
		_ = topology.Cleanup(context.Background())
	})
	return topology, workspaceRoot
}

func runtimeTopologyRepositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func runtimeTopologyInspection(runID string, containerID string) string {
	return strings.Join([]string{
		containerID,
		gatewayImage,
		"dg_runtime_" + runID,
		runID,
		"dg-runtime-gateway-" + runID,
		"dg-runtime-provider-" + runID,
	}, "\n") + "\n"
}
