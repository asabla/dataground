package canarydocker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/asabla/dataground/internal/security/canarysource"
)

const (
	testRunID        = "0123456789abcdef0123456789abcdef"
	testContainerID  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testGatewayImage = "ghcr.io/nvidia/openshell/gateway@" +
		"sha256:e21f520a0678ba3cfe749957338b5fa78c75e8e52de13e4559ccbb582f781a0b"
)

func TestSourcesUseExactDockerCommands(t *testing.T) {
	t.Parallel()

	runner := preparedRunner()
	sources := preparedSources(t, runner)

	provider, err := sources.OpenProviderArguments(
		context.Background(),
		request("provider-arguments", "dg-canary-provider-"+testRunID),
	)
	if err != nil {
		t.Fatalf("open provider arguments: %v", err)
	}
	if _, err := io.Copy(io.Discard, provider); err != nil {
		t.Fatalf("read provider arguments: %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("close provider arguments: %v", err)
	}

	logs, err := sources.OpenGatewayLogs(
		context.Background(),
		request("gateway-logs", "dg-canary-gateway-"+testRunID),
	)
	if err != nil {
		t.Fatalf("open gateway logs: %v", err)
	}
	if _, err := io.Copy(io.Discard, logs); err != nil {
		t.Fatalf("read gateway logs: %v", err)
	}
	if err := logs.Close(); err != nil {
		t.Fatalf("close gateway logs: %v", err)
	}

	runner.mu.Lock()
	calls := append([]dockerCall(nil), runner.calls...)
	snapshots := runner.snapshots
	runner.mu.Unlock()
	expected := []dockerCall{
		{
			binary:        "/usr/bin/docker",
			includeStderr: false,
			args: []string{
				"inspect", "--type", "container", "--format", dockerArgumentsFormat,
				testContainerID,
			},
		},
		{
			binary:        "/usr/bin/docker",
			includeStderr: true,
			args:          []string{"logs", "--timestamps", testContainerID},
		},
	}
	if !reflect.DeepEqual(calls, expected) {
		t.Fatalf("Docker calls = %#v, want %#v", calls, expected)
	}
	if snapshots != 3 {
		t.Fatalf("snapshot calls = %d, want construction plus two revalidations", snapshots)
	}
}

func TestSourcesRejectContainerDriftBeforeOpening(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*containerSnapshot){
		"id": func(snapshot *containerSnapshot) {
			snapshot.id = strings.Repeat("f", 64)
		},
		"image": func(snapshot *containerSnapshot) {
			snapshot.image = "example.invalid/gateway@sha256:" + strings.Repeat("f", 64)
		},
		"state": func(snapshot *containerSnapshot) {
			snapshot.running = false
		},
		"service": func(snapshot *containerSnapshot) {
			snapshot.service = "other"
		},
		"project": func(snapshot *containerSnapshot) {
			snapshot.project = "other"
		},
		"run": func(snapshot *containerSnapshot) {
			snapshot.runID = strings.Repeat("f", 32)
		},
		"gateway": func(snapshot *containerSnapshot) {
			snapshot.gatewayName = "other-gateway"
		},
		"provider": func(snapshot *containerSnapshot) {
			snapshot.providerName = "other-provider"
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			runner := preparedRunner()
			sources := preparedSources(t, runner)
			runner.mu.Lock()
			mutate(&runner.snapshot)
			runner.mu.Unlock()
			if _, err := sources.OpenProviderArguments(
				context.Background(),
				request("provider-arguments", "dg-canary-provider-"+testRunID),
			); !errors.Is(err, ErrCredentialSource) {
				t.Fatalf("OpenProviderArguments() error = %v", err)
			}
			runner.mu.Lock()
			callCount := len(runner.calls)
			runner.mu.Unlock()
			if callCount != 0 {
				t.Fatalf("container drift opened %d streams", callCount)
			}
		})
	}
}

func TestSourcesShareOrderAcrossCopies(t *testing.T) {
	t.Parallel()

	runner := preparedRunner()
	sources := preparedSources(t, runner)
	copied := *sources

	provider, err := copied.OpenProviderArguments(
		context.Background(),
		request("provider-arguments", "dg-canary-provider-"+testRunID),
	)
	if err != nil {
		t.Fatalf("open provider arguments: %v", err)
	}
	_, _ = io.Copy(io.Discard, provider)
	if err := provider.Close(); err != nil {
		t.Fatalf("close provider arguments: %v", err)
	}
	logs, err := sources.OpenGatewayLogs(
		context.Background(),
		request("gateway-logs", "dg-canary-gateway-"+testRunID),
	)
	if err != nil {
		t.Fatalf("open gateway logs: %v", err)
	}
	_, _ = io.Copy(io.Discard, logs)
	if err := logs.Close(); err != nil {
		t.Fatalf("close gateway logs: %v", err)
	}
}

func TestSourcesRejectRequestDriftAndOverlap(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*canarysource.Request){
		"run": func(request *canarysource.Request) {
			request.RunID = strings.Repeat("f", 32)
		},
		"surface": func(request *canarysource.Request) {
			request.Surface = "gateway-logs"
		},
		"resource": func(request *canarysource.Request) {
			request.ResourceName = "other-provider"
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			runner := preparedRunner()
			sources := preparedSources(t, runner)
			value := request("provider-arguments", "dg-canary-provider-"+testRunID)
			mutate(&value)
			if _, err := sources.OpenProviderArguments(
				context.Background(),
				value,
			); !errors.Is(err, ErrCredentialSource) {
				t.Fatalf("OpenProviderArguments() error = %v", err)
			}
			runner.mu.Lock()
			callCount := len(runner.calls)
			runner.mu.Unlock()
			if callCount != 0 {
				t.Fatalf("request drift opened %d streams", callCount)
			}
		})
	}

	runner := preparedRunner()
	sources := preparedSources(t, runner)
	provider, err := sources.OpenProviderArguments(
		context.Background(),
		request("provider-arguments", "dg-canary-provider-"+testRunID),
	)
	if err != nil {
		t.Fatalf("open provider arguments: %v", err)
	}
	if _, err := sources.OpenGatewayLogs(
		context.Background(),
		request("gateway-logs", "dg-canary-gateway-"+testRunID),
	); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("overlapping gateway logs error = %v", err)
	}
	if err := provider.Close(); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("incomplete provider close error = %v", err)
	}
}

func TestSourcesFailClosedAfterIncompleteRead(t *testing.T) {
	t.Parallel()

	runner := preparedRunner()
	sources := preparedSources(t, runner)
	provider, err := sources.OpenProviderArguments(
		context.Background(),
		request("provider-arguments", "dg-canary-provider-"+testRunID),
	)
	if err != nil {
		t.Fatalf("open provider arguments: %v", err)
	}
	if err := provider.Close(); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("incomplete close error = %v", err)
	}
	if _, err := sources.OpenGatewayLogs(
		context.Background(),
		request("gateway-logs", "dg-canary-gateway-"+testRunID),
	); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("post-failure gateway logs error = %v", err)
	}
}

func TestSourcesSanitizeBackendFailures(t *testing.T) {
	t.Parallel()

	runner := preparedRunner()
	ambiguous := &trackedStream{Reader: strings.NewReader("sensitive source")}
	runner.open = func(context.Context, bool, string, ...string) (io.ReadCloser, error) {
		return ambiguous, errors.New("sensitive Docker failure")
	}
	sources := preparedSources(t, runner)
	_, err := sources.OpenProviderArguments(
		context.Background(),
		request("provider-arguments", "dg-canary-provider-"+testRunID),
	)
	if !errors.Is(err, ErrCredentialSource) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("OpenProviderArguments() error = %v", err)
	}
	if ambiguous.closes != 1 {
		t.Fatalf("ambiguous stream closes = %d", ambiguous.closes)
	}
}

func TestSourcesValidateConfigurationAndSerialization(t *testing.T) {
	t.Parallel()

	config := testConfig()
	runner := preparedRunner()
	for name, mutate := range map[string]func(*Config){
		"run": func(config *Config) {
			config.RunID = "invalid"
		},
		"gateway": func(config *Config) {
			config.GatewayName = "Invalid"
		},
		"provider": func(config *Config) {
			config.ProviderName = "Invalid"
		},
		"container": func(config *Config) {
			config.ContainerID = "short"
		},
		"image": func(config *Config) {
			config.GatewayImage = "latest"
		},
		"project": func(config *Config) {
			config.ComposeProject = "../escape"
		},
		"binary": func(config *Config) {
			config.DockerBinary = "docker"
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			invalid := config
			mutate(&invalid)
			if _, err := newWithRunner(
				context.Background(),
				invalid,
				runner,
			); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("newWithRunner() error = %v", err)
			}
		})
	}

	sources := preparedSources(t, preparedRunner())
	if _, err := json.Marshal(sources); !errors.Is(err, ErrSerialization) {
		t.Fatalf("marshal sources error = %v", err)
	}
}

func preparedSources(t *testing.T, runner *recordingRunner) *Sources {
	t.Helper()
	sources, err := newWithRunner(context.Background(), testConfig(), runner)
	if err != nil {
		t.Fatalf("newWithRunner(): %v", err)
	}
	return sources
}

func testConfig() Config {
	return Config{
		RunID:          testRunID,
		GatewayName:    "dg-canary-gateway-" + testRunID,
		ProviderName:   "dg-canary-provider-" + testRunID,
		ContainerID:    testContainerID,
		GatewayImage:   testGatewayImage,
		ComposeProject: "dg-canary",
		DockerBinary:   "/usr/bin/docker",
	}
}

func preparedRunner() *recordingRunner {
	return &recordingRunner{
		snapshot: containerSnapshot{
			id:           testContainerID,
			image:        testGatewayImage,
			running:      true,
			service:      gatewayService,
			project:      "dg-canary",
			runID:        testRunID,
			gatewayName:  "dg-canary-gateway-" + testRunID,
			providerName: "dg-canary-provider-" + testRunID,
		},
	}
}

func request(surface string, resourceName string) canarysource.Request {
	return canarysource.Request{
		RunID:        testRunID,
		Surface:      surface,
		ResourceName: resourceName,
	}
}

type dockerCall struct {
	binary        string
	includeStderr bool
	args          []string
}

type recordingRunner struct {
	mu        sync.Mutex
	snapshot  containerSnapshot
	snapshots int
	calls     []dockerCall
	open      func(context.Context, bool, string, ...string) (io.ReadCloser, error)
}

func (runner *recordingRunner) Snapshot(
	context.Context,
	string,
	string,
) (containerSnapshot, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.snapshots++
	return runner.snapshot, nil
}

func (runner *recordingRunner) Open(
	ctx context.Context,
	includeStderr bool,
	binary string,
	args ...string,
) (io.ReadCloser, error) {
	runner.mu.Lock()
	runner.calls = append(runner.calls, dockerCall{
		binary:        binary,
		includeStderr: includeStderr,
		args:          append([]string(nil), args...),
	})
	open := runner.open
	runner.mu.Unlock()
	if open != nil {
		return open(ctx, includeStderr, binary, args...)
	}
	return io.NopCloser(strings.NewReader("safe source")), nil
}

type trackedStream struct {
	io.Reader
	closes int
}

func (stream *trackedStream) Close() error {
	stream.closes++
	return nil
}
