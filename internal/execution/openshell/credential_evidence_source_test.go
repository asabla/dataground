package openshell

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/security/canarysource"
)

const credentialEvidenceTestRunID = "0123456789abcdef0123456789abcdef"

func TestCredentialEvidenceSourcesUseExactPinnedCommands(t *testing.T) {
	t.Parallel()

	provider, sources, created, streamRunner := preparedCredentialEvidenceSources(t)
	record, err := provider.store.GetExecution(context.Background(), execution.ExecutionRef{
		IsolationDomainID: created.IsolationDomainID,
		ID:                created.ID,
	})
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}

	tests := []struct {
		surface string
		open    func(context.Context, canarysource.Request) (io.ReadCloser, error)
		command []string
	}{
		{
			surface: "sandbox-process",
			open:    sources.OpenSandboxProcess,
			command: credentialEvidenceCommands["sandbox-process"],
		},
		{
			surface: "sandbox-environment",
			open:    sources.OpenSandboxEnvironment,
			command: credentialEvidenceCommands["sandbox-environment"],
		},
		{
			surface: "sandbox-filesystem",
			open:    sources.OpenSandboxFilesystem,
			command: credentialEvidenceCommands["sandbox-filesystem"],
		},
		{
			surface: "sandbox-logs",
			open:    sources.OpenSandboxLogs,
			command: credentialEvidenceCommands["sandbox-logs"],
		},
	}
	for _, test := range tests {
		stream, err := test.open(context.Background(), credentialEvidenceRequest(created.ID, test.surface))
		if err != nil {
			t.Fatalf("%s open error = %v", test.surface, err)
		}
		content, err := io.ReadAll(stream)
		if err != nil {
			t.Fatalf("%s read error = %v", test.surface, err)
		}
		if err := stream.Close(); err != nil {
			t.Fatalf("%s close error = %v", test.surface, err)
		}
		if string(content) != "safe source" {
			t.Fatalf("%s content = %q", test.surface, content)
		}
	}

	streamRunner.mu.Lock()
	calls := append([]runnerCall(nil), streamRunner.calls...)
	streamRunner.mu.Unlock()
	if len(calls) != len(tests) {
		t.Fatalf("stream calls = %d", len(calls))
	}
	for index, call := range calls {
		expected := append(
			[]string{
				"--gateway-endpoint", "http://127.0.0.1:8080",
				"sandbox", "exec", "--name", record.SandboxName,
				"--no-tty", "--",
			},
			tests[index].command...,
		)
		if call.binary != "openshell" || !reflect.DeepEqual(call.args, expected) {
			t.Fatalf("call %d = %#v, want %#v", index, call, expected)
		}
	}
	if record.SandboxName == created.ID || !strings.HasPrefix(record.SandboxName, "dg-") {
		t.Fatalf("test did not exercise private native routing: %q", record.SandboxName)
	}
}

func TestCredentialEvidenceSourcesRejectDriftBeforeOpening(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*canarysource.Request){
		"run": func(request *canarysource.Request) {
			request.RunID = "fedcba9876543210fedcba9876543210"
		},
		"surface": func(request *canarysource.Request) {
			request.Surface = "sandbox-environment"
		},
		"resource": func(request *canarysource.Request) {
			request.ResourceName = "other-execution"
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, sources, created, streamRunner := preparedCredentialEvidenceSources(t)
			request := credentialEvidenceRequest(created.ID, "sandbox-process")
			mutate(&request)
			if _, err := sources.OpenSandboxProcess(context.Background(), request); !errors.Is(err, ErrCredentialEvidenceSource) {
				t.Fatalf("OpenSandboxProcess() error = %v", err)
			}
			streamRunner.mu.Lock()
			callCount := len(streamRunner.calls)
			streamRunner.mu.Unlock()
			if callCount != 0 {
				t.Fatalf("drift opened %d streams", callCount)
			}
		})
	}
}

func TestCredentialEvidenceSourcesShareOrderAcrossCopies(t *testing.T) {
	t.Parallel()

	_, sources, created, streamRunner := preparedCredentialEvidenceSources(t)
	copied := *sources

	process, err := sources.OpenSandboxProcess(
		context.Background(),
		credentialEvidenceRequest(created.ID, "sandbox-process"),
	)
	if err != nil {
		t.Fatalf("open process: %v", err)
	}
	_, _ = io.Copy(io.Discard, process)
	if err := process.Close(); err != nil {
		t.Fatalf("close process: %v", err)
	}

	if _, err := copied.OpenSandboxProcess(
		context.Background(),
		credentialEvidenceRequest(created.ID, "sandbox-process"),
	); !errors.Is(err, ErrCredentialEvidenceSource) {
		t.Fatalf("copied process retry error = %v", err)
	}
	environment, err := copied.OpenSandboxEnvironment(
		context.Background(),
		credentialEvidenceRequest(created.ID, "sandbox-environment"),
	)
	if err != nil {
		t.Fatalf("copied environment open: %v", err)
	}
	_, _ = io.Copy(io.Discard, environment)
	if err := environment.Close(); err != nil {
		t.Fatalf("close environment: %v", err)
	}

	streamRunner.mu.Lock()
	callCount := len(streamRunner.calls)
	streamRunner.mu.Unlock()
	if callCount != 2 {
		t.Fatalf("stream calls = %d", callCount)
	}
}

func TestCredentialEvidenceSourcesRevalidatePrivateTarget(t *testing.T) {
	t.Parallel()

	provider, sources, created, streamRunner := preparedCredentialEvidenceSources(t)
	if err := provider.SetGatewayState(
		context.Background(),
		created.IsolationDomainID,
		created.GatewayID,
		execution.GatewayLost,
	); err != nil {
		t.Fatalf("mark gateway lost: %v", err)
	}
	if _, err := sources.OpenSandboxProcess(
		context.Background(),
		credentialEvidenceRequest(created.ID, "sandbox-process"),
	); !errors.Is(err, ErrCredentialEvidenceSource) {
		t.Fatalf("OpenSandboxProcess() error = %v", err)
	}
	streamRunner.mu.Lock()
	callCount := len(streamRunner.calls)
	streamRunner.mu.Unlock()
	if callCount != 0 {
		t.Fatalf("lost target opened %d streams", callCount)
	}
}

func TestCredentialEvidenceSourcesSanitizeOpenFailure(t *testing.T) {
	t.Parallel()

	ambiguous := &trackedEvidenceStream{Reader: strings.NewReader("sensitive source")}
	streamRunner := &recordingEvidenceStreamRunner{
		open: func(context.Context, string, ...string) (io.ReadCloser, error) {
			return ambiguous, errors.New("sensitive upstream failure")
		},
	}
	_, sources, created, _ := preparedCredentialEvidenceSourcesWithRunner(t, streamRunner)
	_, err := sources.OpenSandboxProcess(
		context.Background(),
		credentialEvidenceRequest(created.ID, "sandbox-process"),
	)
	if !errors.Is(err, ErrCredentialEvidenceSource) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("OpenSandboxProcess() error = %v", err)
	}
	if ambiguous.closes != 1 {
		t.Fatalf("ambiguous stream closes = %d", ambiguous.closes)
	}
}

func TestCredentialEvidenceSourcesRequireReadyBoundExecution(t *testing.T) {
	t.Parallel()

	provider, created := preparedCredentialEvidenceExecution(t)
	streamRunner := &recordingEvidenceStreamRunner{}
	if _, err := provider.NewCredentialEvidenceSources(
		context.Background(),
		CredentialEvidenceSourceConfig{
			RunID:     credentialEvidenceTestRunID,
			Execution: execution.ExecutionRef{IsolationDomainID: created.IsolationDomainID, ID: created.ID},
			Stream:    streamRunner,
		},
	); !errors.Is(err, ErrCredentialEvidenceSource) {
		t.Fatalf("NewCredentialEvidenceSources() error = %v", err)
	}
	streamRunner.mu.Lock()
	callCount := len(streamRunner.calls)
	streamRunner.mu.Unlock()
	if callCount != 0 {
		t.Fatalf("construction opened %d streams", callCount)
	}
}

func TestCredentialEvidenceSourcesRequirePinnedCLI(t *testing.T) {
	t.Parallel()

	provider, created := preparedCredentialEvidenceExecution(t)
	ref := execution.ExecutionRef{IsolationDomainID: created.IsolationDomainID, ID: created.ID}
	if err := provider.store.UpdateExecutionState(context.Background(), ref, "ready"); err != nil {
		t.Fatalf("mark execution ready: %v", err)
	}
	provider.expected = "0.0.87"
	if _, err := provider.NewCredentialEvidenceSources(
		context.Background(),
		CredentialEvidenceSourceConfig{
			RunID:     credentialEvidenceTestRunID,
			Execution: ref,
			Stream:    &recordingEvidenceStreamRunner{},
		},
	); !errors.Is(err, ErrCredentialEvidenceSource) {
		t.Fatalf("NewCredentialEvidenceSources() error = %v", err)
	}
}

func TestCredentialEvidenceSourcesCannotSerialize(t *testing.T) {
	t.Parallel()

	_, sources, _, _ := preparedCredentialEvidenceSources(t)
	if _, err := json.Marshal(sources); !errors.Is(err, ErrCredentialEvidenceSerialization) {
		t.Fatalf("marshal sources error = %v", err)
	}
}

func preparedCredentialEvidenceSources(
	t *testing.T,
) (*Provider, *CredentialEvidenceSources, execution.Execution, *recordingEvidenceStreamRunner) {
	t.Helper()
	return preparedCredentialEvidenceSourcesWithRunner(t, &recordingEvidenceStreamRunner{})
}

func preparedCredentialEvidenceSourcesWithRunner(
	t *testing.T,
	streamRunner *recordingEvidenceStreamRunner,
) (*Provider, *CredentialEvidenceSources, execution.Execution, *recordingEvidenceStreamRunner) {
	t.Helper()

	provider, created := preparedCredentialEvidenceExecution(t)
	ref := execution.ExecutionRef{IsolationDomainID: created.IsolationDomainID, ID: created.ID}
	if err := provider.store.UpdateExecutionState(context.Background(), ref, "ready"); err != nil {
		t.Fatalf("mark execution ready: %v", err)
	}
	sources, err := provider.NewCredentialEvidenceSources(
		context.Background(),
		CredentialEvidenceSourceConfig{
			RunID:     credentialEvidenceTestRunID,
			Execution: ref,
			Stream:    streamRunner,
		},
	)
	if err != nil {
		t.Fatalf("NewCredentialEvidenceSources(): %v", err)
	}
	return provider, sources, created, streamRunner
}

func preparedCredentialEvidenceExecution(t *testing.T) (*Provider, execution.Execution) {
	t.Helper()

	commandRunner := &scriptedRunner{results: []scriptedResult{
		{result: CommandResult{Stdout: []byte("[]")}},
		{result: CommandResult{}},
		{result: CommandResult{Stdout: []byte("openshell 0.0.86\n")}},
	}}
	provider, _, placement, policy, policyDigest := preparedProvider(t, commandRunner)
	created, err := provider.Create(
		context.Background(),
		createRequest(placement, policy, policyDigest),
	)
	if err != nil {
		t.Fatalf("create execution: %v", err)
	}
	return provider, created
}

func credentialEvidenceRequest(executionID string, surface string) canarysource.Request {
	return canarysource.Request{
		RunID:        credentialEvidenceTestRunID,
		Surface:      surface,
		ResourceName: executionID,
	}
}

type recordingEvidenceStreamRunner struct {
	mu    sync.Mutex
	calls []runnerCall
	open  func(context.Context, string, ...string) (io.ReadCloser, error)
}

func (runner *recordingEvidenceStreamRunner) Open(
	ctx context.Context,
	binary string,
	args ...string,
) (io.ReadCloser, error) {
	runner.mu.Lock()
	runner.calls = append(runner.calls, runnerCall{
		binary: binary,
		args:   append([]string(nil), args...),
	})
	open := runner.open
	runner.mu.Unlock()
	if open != nil {
		return open(ctx, binary, args...)
	}
	return io.NopCloser(strings.NewReader("safe source")), nil
}

type trackedEvidenceStream struct {
	io.Reader
	closes int
}

func (stream *trackedEvidenceStream) Close() error {
	stream.closes++
	return nil
}
