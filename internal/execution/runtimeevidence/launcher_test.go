package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

func TestLauncherOwnsCompositionAndCleanupOrder(t *testing.T) {
	t.Parallel()

	fixture := newLauncherFixture()
	result, err := launch(context.Background(), fixture.config(), fixture.dependencies())
	if err != nil {
		t.Fatalf("launch() error = %v", err)
	}
	if !result.complete {
		t.Fatal("launch() returned an incomplete result")
	}
	want := []string{
		"run-id",
		"policy",
		"workspace-open",
		"ports-open",
		"provider-check",
		"topology-open",
		"topology-start",
		"gateway-register",
		"source-open",
		"provider-open",
		"provider-provision",
		"creator-open",
		"creator-create",
		"harness-open",
		"harness-run",
		"creator-cleanup",
		"provider-cleanup",
		"source-cleanup",
		"workspace-cleanup",
		"topology-cleanup",
	}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events = %#v, want %#v", fixture.events, want)
	}
}

func TestLauncherPreflightsBeforeTopologyAndCredentialConsumption(t *testing.T) {
	t.Parallel()

	fixture := newLauncherFixture()
	fixture.ports.checkErr = errors.New("private version detail")
	result, err := launch(context.Background(), fixture.config(), fixture.dependencies())
	if !errors.Is(err, ErrLauncherRun) || stringsContainError(err, "private") {
		t.Fatalf("launch() error = %v", err)
	}
	if result.complete {
		t.Fatal("failed launch returned a complete result")
	}
	want := []string{
		"run-id",
		"policy",
		"workspace-open",
		"ports-open",
		"provider-check",
		"workspace-cleanup",
	}
	if !reflect.DeepEqual(fixture.events, want) {
		t.Fatalf("events = %#v, want %#v", fixture.events, want)
	}
}

func TestLauncherRetriesEveryCleanupAfterProvisionFailure(t *testing.T) {
	t.Parallel()

	fixture := newLauncherFixture()
	fixture.provider.provisionErr = errors.New("private provider detail")
	result, err := launch(context.Background(), fixture.config(), fixture.dependencies())
	if !errors.Is(err, ErrLauncherRun) || stringsContainError(err, "private") {
		t.Fatalf("launch() error = %v", err)
	}
	if result.complete {
		t.Fatal("failed launch returned a complete result")
	}
	wantTail := []string{
		"provider-provision",
		"provider-cleanup",
		"source-cleanup",
		"workspace-cleanup",
		"topology-cleanup",
	}
	if !reflect.DeepEqual(fixture.events[len(fixture.events)-len(wantTail):], wantTail) {
		t.Fatalf("events = %#v", fixture.events)
	}
}

func TestLauncherSealsResultWhenFinalCleanupFails(t *testing.T) {
	t.Parallel()

	fixture := newLauncherFixture()
	fixture.topology.cleanupErr = errors.New("private Docker detail")
	result, err := launch(context.Background(), fixture.config(), fixture.dependencies())
	if !errors.Is(err, ErrLauncherCleanup) || stringsContainError(err, "private") {
		t.Fatalf("launch() error = %v", err)
	}
	if result.complete {
		t.Fatal("cleanup failure released a complete result")
	}
}

func TestLauncherRejectsInvalidProvenanceBeforeMutation(t *testing.T) {
	t.Parallel()

	fixture := newLauncherFixture()
	config := fixture.config()
	config.Provenance.SourceCommit = "invalid"
	if _, err := launch(context.Background(), config, fixture.dependencies()); !errors.Is(
		err,
		ErrLauncherConfiguration,
	) {
		t.Fatalf("launch() error = %v", err)
	}
	if len(fixture.events) != 0 {
		t.Fatalf("events = %#v", fixture.events)
	}
	if _, err := json.Marshal(config); !errors.Is(err, ErrSerialization) {
		t.Fatalf("json.Marshal(config) error = %v", err)
	}
}

func TestRuntimeLauncherWorkspaceRemovesExactOwnedState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	value, err := newRuntimeLauncherWorkspace(root, testRunID)
	if err != nil {
		t.Fatalf("newRuntimeLauncherWorkspace() error = %v", err)
	}
	workspace := value.(*runtimeLauncherWorkspace)
	path := workspace.path
	if _, err := json.Marshal(workspace); !errors.Is(err, ErrSerialization) {
		t.Fatalf("json.Marshal(workspace) error = %v", err)
	}
	if err := workspace.Cleanup(context.Background(), CleanupRequest{
		RunID:        testRunID,
		ResourceKind: "workspace",
		ResourceName: namesForRun(testRunID).Workspace,
	}); err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace remains: %v", err)
	}
}

type launcherFixture struct {
	events    []string
	topology  *fakeLauncherTopology
	workspace *fakeLauncherWorkspace
	ports     *fakeLauncherPorts
	source    *fakeLauncherSource
	provider  *fakeLauncherProvider
	creator   *fakeLauncherCreator
	harness   *fakeLauncherHarness
}

func newLauncherFixture() *launcherFixture {
	fixture := &launcherFixture{}
	fixture.topology = &fakeLauncherTopology{fixture: fixture}
	fixture.workspace = &fakeLauncherWorkspace{fixture: fixture}
	fixture.ports = &fakeLauncherPorts{fixture: fixture}
	fixture.source = &fakeLauncherSource{fixture: fixture}
	fixture.provider = &fakeLauncherProvider{fixture: fixture}
	fixture.creator = &fakeLauncherCreator{fixture: fixture}
	fixture.harness = &fakeLauncherHarness{fixture: fixture}
	return fixture
}

func (fixture *launcherFixture) config() LauncherConfig {
	return LauncherConfig{
		RepositoryRoot:      "/repository",
		WorkspaceRoot:       "/workspace",
		CredentialDirectory: "/credentials",
		Provenance: Provenance{
			SourceCommit:  "1111111111111111111111111111111111111111",
			WorkflowRunID: 42,
		},
	}
}

func (fixture *launcherFixture) dependencies() launcherDependencies {
	return launcherDependencies{
		newRunID: func() (string, error) {
			fixture.events = append(fixture.events, "run-id")
			return testRunID, nil
		},
		readPolicy: func(config LauncherConfig) ([]byte, error) {
			fixture.events = append(fixture.events, "policy")
			if config.candidateImage != "" {
				return []byte(candidateRuntimeTestPolicy), nil
			}
			return []byte(executionCreatorTestPolicy), nil
		},
		openWorkspace: func(string, string) (launcherWorkspace, error) {
			fixture.events = append(fixture.events, "workspace-open")
			return fixture.workspace, nil
		},
		openPorts: func(string, string, launcherWorkspace) (launcherPorts, error) {
			fixture.events = append(fixture.events, "ports-open")
			return fixture.ports, nil
		},
		openTopology: func(DockerTopologyConfig) (launcherTopology, error) {
			fixture.events = append(fixture.events, "topology-open")
			return fixture.topology, nil
		},
		openSource: func(CredentialSourceConfig) (launcherCredentialSource, error) {
			fixture.events = append(fixture.events, "source-open")
			return fixture.source, nil
		},
		newProvider: func(
			context.Context,
			string,
			launcherCredentialSource,
			launcherPorts,
		) (launcherProviderBinding, error) {
			fixture.events = append(fixture.events, "provider-open")
			return fixture.provider, nil
		},
		newCreator: func(LauncherConfig, string, []byte, launcherPorts) (launcherExecutionCreator, error) {
			fixture.events = append(fixture.events, "creator-open")
			return fixture.creator, nil
		},
		newHarness: func(
			LauncherConfig,
			string,
			execution.Execution,
			launcherPorts,
			launcherProviderBinding,
			launcherExecutionCreator,
			launcherWorkspace,
		) (launcherHarness, error) {
			fixture.events = append(fixture.events, "harness-open")
			return fixture.harness, nil
		},
	}
}

type fakeLauncherTopology struct {
	fixture    *launcherFixture
	cleanupErr error
}

func (topology *fakeLauncherTopology) Start(context.Context) error {
	topology.fixture.events = append(topology.fixture.events, "topology-start")
	return nil
}

func (topology *fakeLauncherTopology) Cleanup(context.Context) error {
	topology.fixture.events = append(topology.fixture.events, "topology-cleanup")
	return topology.cleanupErr
}

type fakeLauncherWorkspace struct{ fixture *launcherFixture }

func (workspace *fakeLauncherWorkspace) Cleanup(context.Context, CleanupRequest) error {
	workspace.fixture.events = append(workspace.fixture.events, "workspace-cleanup")
	return nil
}

type fakeLauncherPorts struct {
	fixture  *launcherFixture
	checkErr error
}

func (ports *fakeLauncherPorts) Check(context.Context) error {
	ports.fixture.events = append(ports.fixture.events, "provider-check")
	return ports.checkErr
}

func (ports *fakeLauncherPorts) Register(context.Context, string) error {
	ports.fixture.events = append(ports.fixture.events, "gateway-register")
	return nil
}

type fakeLauncherSource struct{ fixture *launcherFixture }

func (source *fakeLauncherSource) Cleanup(context.Context) error {
	source.fixture.events = append(source.fixture.events, "source-cleanup")
	return nil
}

type fakeLauncherProvider struct {
	fixture      *launcherFixture
	provisionErr error
}

func (provider *fakeLauncherProvider) Provision(context.Context) error {
	provider.fixture.events = append(provider.fixture.events, "provider-provision")
	return provider.provisionErr
}

func (provider *fakeLauncherProvider) Cleanup(context.Context, CleanupRequest) error {
	provider.fixture.events = append(provider.fixture.events, "provider-cleanup")
	return nil
}

type fakeLauncherCreator struct{ fixture *launcherFixture }

func (creator *fakeLauncherCreator) Create(context.Context) (execution.Execution, error) {
	creator.fixture.events = append(creator.fixture.events, "creator-create")
	return execution.Execution{ID: "execution"}, nil
}

func (creator *fakeLauncherCreator) Cleanup(context.Context, CleanupRequest) error {
	creator.fixture.events = append(creator.fixture.events, "creator-cleanup")
	return nil
}

type fakeLauncherHarness struct{ fixture *launcherFixture }

func (harness *fakeLauncherHarness) Run(context.Context) (Result, error) {
	harness.fixture.events = append(harness.fixture.events, "harness-run")
	return Result{complete: true}, nil
}

func stringsContainError(err error, value string) bool {
	return err != nil && strings.Contains(err.Error(), value)
}
