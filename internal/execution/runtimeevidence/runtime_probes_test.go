package runtimeevidence

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

func TestCodexProbesOwnProtocolObservations(t *testing.T) {
	t.Parallel()

	fixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
	probes := fixture.open(t)
	request := testProbeRequest()

	results := make([]ProbeResult, 0, len(codexProbeOrder))
	for _, call := range []func(context.Context, ProbeRequest) (ProbeResult, error){
		probes.Initialize,
		probes.TurnSuccess,
		probes.TurnFailure,
		probes.EventNormalization,
		probes.Interrupt,
		probes.Cancellation,
		probes.CommandApproval,
		probes.FileChangeApproval,
	} {
		result, err := call(context.Background(), request)
		if err != nil {
			t.Fatalf("probe %d error = %v", len(results), err)
		}
		results = append(results, result)
	}
	if fixture.provider.opened != len(codexProbeOrder) {
		t.Fatalf("opened sessions = %d, want %d", fixture.provider.opened, len(codexProbeOrder))
	}
	for index, result := range results {
		if result.StartedAt.IsZero() || !result.FinishedAt.After(result.StartedAt) {
			t.Fatalf("probe %d has invalid interval: %#v", index, result)
		}
		if result.ObservationSHA256 == ([32]byte{}) {
			t.Fatalf("probe %d has empty observation commitment", index)
		}
		if !result.Assertions.ExposureChecked ||
			result.Assertions.NativeProtocolExposed ||
			result.Assertions.UpstreamEndpointExposed {
			t.Fatalf("probe %d has invalid exposure assertion: %#v", index, result.Assertions)
		}
	}
}

func TestCodexProbesCompleteConcreteScenario(t *testing.T) {
	t.Parallel()

	fixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
	probes := fixture.open(t)
	provider := &codexChainProvider{clock: fixture.clock}
	scenario, err := NewConcreteScenario(ScenarioConfig{
		RunID:    testRunID,
		Provider: provider,
		Runtime:  probes,
	})
	if err != nil {
		t.Fatalf("NewConcreteScenario() error = %v", err)
	}
	binding := LiveBinding{RunID: testRunID, Resources: namesForRun(testRunID)}
	for index, call := range []func(context.Context, LiveBinding) (LiveReceipt, error){
		scenario.GatewayReady,
		scenario.SandboxReady,
		scenario.Initialize,
		scenario.TurnSuccess,
		scenario.TurnFailure,
		scenario.EventNormalization,
		scenario.Interrupt,
		scenario.Cancellation,
		scenario.CommandApproval,
		scenario.FileChangeApproval,
		scenario.ArtifactExport,
		scenario.SandboxTeardown,
	} {
		receipt, err := call(context.Background(), binding)
		if err != nil {
			t.Fatalf("scenario case %d error = %v", index, err)
		}
		if receipt.ObservationSHA256 == ([sha256.Size]byte{}) {
			t.Fatalf("scenario case %d has empty commitment", index)
		}
	}
}

func TestCodexProbesFailClosed(t *testing.T) {
	t.Parallel()

	t.Run("binding drift is terminal", func(t *testing.T) {
		fixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
		probes := fixture.open(t)
		request := testProbeRequest()
		request.Resources.Runtime = "other-runtime"
		if _, err := probes.Initialize(context.Background(), request); !errors.Is(err, ErrCodexProbeOrder) {
			t.Fatalf("Initialize() error = %v", err)
		}
		if _, err := probes.Initialize(context.Background(), testProbeRequest()); !errors.Is(err, ErrCodexProbeOrder) {
			t.Fatalf("Initialize() after drift error = %v", err)
		}
	})

	t.Run("order drift is terminal", func(t *testing.T) {
		fixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
		probes := fixture.open(t)
		if _, err := probes.TurnSuccess(context.Background(), testProbeRequest()); !errors.Is(err, ErrCodexProbeOrder) {
			t.Fatalf("TurnSuccess() error = %v", err)
		}
		if _, err := probes.Initialize(context.Background(), testProbeRequest()); !errors.Is(err, ErrCodexProbeOrder) {
			t.Fatalf("Initialize() after order drift error = %v", err)
		}
	})

	t.Run("persisted execution substitution is rejected", func(t *testing.T) {
		fixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
		fixture.store.record.OperationID = "op-substituted"
		probes := fixture.open(t)
		if _, err := probes.Initialize(context.Background(), testProbeRequest()); !errors.Is(err, ErrCodexProbeObservation) {
			t.Fatalf("Initialize() error = %v", err)
		}
		if fixture.provider.opened != 0 {
			t.Fatalf("opened sessions = %d, want 0", fixture.provider.opened)
		}
	})

	t.Run("provider failure is sanitized", func(t *testing.T) {
		fixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
		fixture.provider.openErr = errors.New("private endpoint failed")
		probes := fixture.open(t)
		if _, err := probes.Initialize(context.Background(), testProbeRequest()); !errors.Is(err, ErrCodexProbeObservation) ||
			err.Error() != ErrCodexProbeObservation.Error() {
			t.Fatalf("Initialize() error = %v", err)
		}
	})

	t.Run("uncertain session close is rejected", func(t *testing.T) {
		fixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
		fixture.provider.closeErr = errors.New("native close acknowledgement lost")
		probes := fixture.open(t)
		if _, err := probes.Initialize(context.Background(), testProbeRequest()); !errors.Is(err, ErrCodexProbeObservation) {
			t.Fatalf("Initialize() error = %v", err)
		}
	})

	t.Run("overlap poisons both calls", func(t *testing.T) {
		fixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
		fixture.store.entered = make(chan struct{})
		fixture.store.release = make(chan struct{})
		probes := fixture.open(t)
		first := make(chan error, 1)
		go func() {
			_, err := probes.Initialize(context.Background(), testProbeRequest())
			first <- err
		}()
		<-fixture.store.entered
		if _, err := probes.TurnSuccess(context.Background(), testProbeRequest()); !errors.Is(err, ErrCodexProbeOrder) {
			t.Fatalf("overlapping TurnSuccess() error = %v", err)
		}
		close(fixture.store.release)
		if err := <-first; !errors.Is(err, ErrCodexProbeOrder) {
			t.Fatalf("overlapped Initialize() error = %v", err)
		}
	})

	t.Run("normalized endpoint exposure is terminal", func(t *testing.T) {
		scripts := codexProbeScripts(testRunID)
		scripts[1] = successScript("http://127.0.0.1:8080")
		fixture := newCodexProbeFixture(scripts...)
		probes := fixture.open(t)
		if _, err := probes.Initialize(context.Background(), testProbeRequest()); err != nil {
			t.Fatalf("Initialize() error = %v", err)
		}
		if _, err := probes.TurnSuccess(context.Background(), testProbeRequest()); !errors.Is(err, ErrCodexProbeObservation) {
			t.Fatalf("TurnSuccess() error = %v", err)
		}
		if _, err := probes.TurnFailure(context.Background(), testProbeRequest()); !errors.Is(err, ErrCodexProbeOrder) {
			t.Fatalf("TurnFailure() after exposure error = %v", err)
		}
	})

	t.Run("clock regression is rejected", func(t *testing.T) {
		fixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
		fixture.clock.regress = true
		probes := fixture.open(t)
		if _, err := probes.Initialize(context.Background(), testProbeRequest()); !errors.Is(err, ErrCodexProbeObservation) {
			t.Fatalf("Initialize() error = %v", err)
		}
	})

	t.Run("cancellation is terminal", func(t *testing.T) {
		fixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
		probes := fixture.open(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := probes.Initialize(ctx, testProbeRequest()); !errors.Is(err, context.Canceled) {
			t.Fatalf("Initialize() error = %v", err)
		}
		if _, err := probes.Initialize(context.Background(), testProbeRequest()); !errors.Is(err, ErrCodexProbeOrder) {
			t.Fatalf("Initialize() after cancellation error = %v", err)
		}
	})

	t.Run("configuration cannot be serialized", func(t *testing.T) {
		fixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
		config := fixture.config()
		if _, err := json.Marshal(config); !errors.Is(err, ErrSerialization) {
			t.Fatalf("json.Marshal(config) error = %v", err)
		}
		probes := fixture.open(t)
		if _, err := json.Marshal(probes); !errors.Is(err, ErrSerialization) {
			t.Fatalf("json.Marshal(probes) error = %v", err)
		}
	})
}

type codexProbeFixture struct {
	store    *codexProbeStore
	provider *codexProbeProvider
	clock    *probeClock
}

type codexChainProvider struct {
	clock *probeClock
}

func (provider *codexChainProvider) GatewayReady(
	context.Context,
	ProbeRequest,
) (ProbeResult, error) {
	return provider.result(CheckGatewayReady), nil
}

func (provider *codexChainProvider) SandboxReady(
	context.Context,
	ProbeRequest,
) (ProbeResult, error) {
	return provider.result(CheckSandboxReady), nil
}

func (provider *codexChainProvider) ArtifactExport(
	context.Context,
	ProbeRequest,
) (ProbeResult, error) {
	return provider.result(CheckArtifactExport), nil
}

func (provider *codexChainProvider) SandboxTeardown(
	context.Context,
	ProbeRequest,
) (ProbeResult, error) {
	return provider.result(CheckSandboxTeardown), nil
}

func (provider *codexChainProvider) result(name CheckName) ProbeResult {
	startedAt := provider.clock.now()
	finishedAt := provider.clock.now()
	return ProbeResult{
		StartedAt:         startedAt,
		FinishedAt:        finishedAt,
		ObservationSHA256: sha256.Sum256([]byte("codex-chain-" + string(name))),
		Assertions:        openShellProbeAssertion(name),
	}
}

func newCodexProbeFixture(scripts ...func(*codexProbeServer)) *codexProbeFixture {
	resources := namesForRun(testRunID)
	executionID := "exe-runtime-conformance"
	store := &codexProbeStore{record: execution.ExecutionRecord{
		Execution: execution.Execution{
			IsolationDomainID: runtimeIsolationDomain(testRunID),
			ID:                executionID,
			GatewayID:         resources.Gateway,
			State:             "ready",
		},
		PlacementID: "plc-runtime-conformance",
		OperationID: runtimeOperationID(testRunID),
		SandboxName: "native-sandbox",
	}}
	return &codexProbeFixture{
		store:    store,
		provider: &codexProbeProvider{scripts: scripts},
		clock: &probeClock{
			value: time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
		},
	}
}

func (fixture *codexProbeFixture) config() CodexProbeConfig {
	return CodexProbeConfig{
		RunID:       testRunID,
		ExecutionID: fixture.store.record.Execution.ID,
		Store:       fixture.store,
		Provider:    fixture.provider,
		Now:         fixture.clock.now,
	}
}

func (fixture *codexProbeFixture) open(t *testing.T) *CodexProbes {
	t.Helper()
	probes, err := NewCodexProbes(fixture.config())
	if err != nil {
		t.Fatalf("NewCodexProbes() error = %v", err)
	}
	return probes
}

type codexProbeStore struct {
	record  execution.ExecutionRecord
	err     error
	entered chan struct{}
	release chan struct{}
}

func (store *codexProbeStore) GetExecution(
	_ context.Context,
	_ execution.ExecutionRef,
) (execution.ExecutionRecord, error) {
	if store.entered != nil {
		close(store.entered)
		<-store.release
	}
	return store.record, store.err
}

type codexProbeProvider struct {
	mu       sync.Mutex
	scripts  []func(*codexProbeServer)
	opened   int
	openErr  error
	closeErr error
}

func (provider *codexProbeProvider) StartRuntime(
	_ context.Context,
	_ execution.ExecutionRef,
) (execution.RuntimeSession, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.openErr != nil {
		return nil, provider.openErr
	}
	if provider.opened >= len(provider.scripts) {
		return nil, errors.New("unexpected runtime session")
	}
	session := newCodexProbeSession(provider.scripts[provider.opened])
	session.closeErr = provider.closeErr
	provider.opened++
	return session, nil
}

type codexProbeSession struct {
	inputWriter  *io.PipeWriter
	outputReader *io.PipeReader
	errorReader  *io.PipeReader
	done         chan struct{}
	closeOnce    sync.Once
	closeErr     error
}

func newCodexProbeSession(script func(*codexProbeServer)) *codexProbeSession {
	clientInput, serverInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	clientErrors, serverErrors := io.Pipe()
	session := &codexProbeSession{
		inputWriter:  serverInput,
		outputReader: clientOutput,
		errorReader:  clientErrors,
		done:         make(chan struct{}),
	}
	server := &codexProbeServer{
		reader: bufio.NewReader(clientInput),
		writer: serverOutput,
	}
	go func() {
		defer clientInput.Close()
		defer serverOutput.Close()
		defer serverErrors.Close()
		script(server)
		<-session.done
	}()
	return session
}

func (session *codexProbeSession) Input() io.WriteCloser { return session.inputWriter }
func (session *codexProbeSession) Output() io.ReadCloser { return session.outputReader }
func (session *codexProbeSession) Errors() io.ReadCloser { return session.errorReader }
func (session *codexProbeSession) Wait() error           { <-session.done; return nil }
func (session *codexProbeSession) Close() error {
	session.closeOnce.Do(func() {
		close(session.done)
		_ = session.inputWriter.Close()
		_ = session.outputReader.Close()
		_ = session.errorReader.Close()
	})
	return session.closeErr
}

type codexProbeFrame struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type codexProbeServer struct {
	reader *bufio.Reader
	writer io.Writer
}

func (server *codexProbeServer) read() codexProbeFrame {
	line, err := server.reader.ReadBytes('\n')
	if err != nil {
		panic(err)
	}
	var frame codexProbeFrame
	if err := json.Unmarshal(line, &frame); err != nil {
		panic(err)
	}
	return frame
}

func (server *codexProbeServer) respond(id json.RawMessage, result any) {
	var nativeID any
	if err := json.Unmarshal(id, &nativeID); err != nil {
		panic(err)
	}
	server.write(map[string]any{"id": nativeID, "result": result})
}

func (server *codexProbeServer) request(id any, method string, params any) {
	server.write(map[string]any{"id": id, "method": method, "params": params})
}

func (server *codexProbeServer) notify(method string, params any) {
	server.write(map[string]any{"method": method, "params": params})
}

func (server *codexProbeServer) write(value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	if _, err := server.writer.Write(append(encoded, '\n')); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		panic(err)
	}
}

func (server *codexProbeServer) start() {
	initialize := server.read()
	if initialize.Method != "initialize" {
		panic("expected initialize")
	}
	server.respond(initialize.ID, map[string]any{})
	if server.read().Method != "initialized" {
		panic("expected initialized")
	}
	thread := server.read()
	if thread.Method != "thread/start" {
		panic("expected thread/start")
	}
	server.respond(thread.ID, map[string]any{"thread": map[string]any{"id": "native-thread"}})
	turn := server.read()
	if turn.Method != "turn/start" {
		panic("expected turn/start")
	}
	server.notify("turn/started", map[string]any{
		"threadId": "native-thread",
		"turn":     map[string]any{"id": "native-turn", "status": "inProgress", "items": []any{}},
	})
	server.respond(turn.ID, map[string]any{
		"turn": map[string]any{"id": "native-turn", "status": "inProgress", "items": []any{}},
	})
}

func (server *codexProbeServer) interrupt() {
	frame := server.read()
	if frame.Method != "turn/interrupt" {
		panic("expected turn/interrupt")
	}
	server.respond(frame.ID, map[string]any{})
	server.notify("turn/completed", map[string]any{
		"threadId": "native-thread",
		"turn":     map[string]any{"id": "native-turn", "status": "interrupted", "items": []any{}},
	})
}

func codexProbeScripts(runID string) []func(*codexProbeServer) {
	successMarker := "dg-runtime-success-" + runID
	eventMarker := "dg-runtime-events-" + runID
	return []func(*codexProbeServer){
		func(server *codexProbeServer) {
			server.start()
			server.interrupt()
		},
		successScript(successMarker),
		func(server *codexProbeServer) {
			server.start()
			server.notify("turn/completed", map[string]any{
				"threadId": "native-thread",
				"turn":     map[string]any{"id": "native-turn", "status": "failed", "items": []any{}},
			})
		},
		func(server *codexProbeServer) {
			server.start()
			server.notify("item/started", map[string]any{
				"threadId": "native-thread",
				"turnId":   "native-turn",
				"item":     map[string]any{"type": "commandExecution"},
			})
			server.notify("item/completed", map[string]any{
				"threadId": "native-thread",
				"turnId":   "native-turn",
				"item":     map[string]any{"type": "commandExecution"},
			})
			server.notify("item/agentMessage/delta", map[string]any{
				"threadId": "native-thread",
				"turnId":   "native-turn",
				"delta":    eventMarker,
			})
			server.notify("turn/completed", map[string]any{
				"threadId": "native-thread",
				"turn":     map[string]any{"id": "native-turn", "status": "completed", "items": []any{}},
			})
		},
		func(server *codexProbeServer) {
			server.start()
			server.interrupt()
		},
		func(server *codexProbeServer) {
			server.start()
		},
		approvalScript("item/commandExecution/requestApproval"),
		approvalScript("item/fileChange/requestApproval"),
	}
}

func successScript(marker string) func(*codexProbeServer) {
	return func(server *codexProbeServer) {
		server.start()
		server.notify("item/agentMessage/delta", map[string]any{
			"threadId": "native-thread",
			"turnId":   "native-turn",
			"delta":    marker,
		})
		server.notify("turn/completed", map[string]any{
			"threadId": "native-thread",
			"turn":     map[string]any{"id": "native-turn", "status": "completed", "items": []any{}},
		})
	}
}

func approvalScript(method string) func(*codexProbeServer) {
	return func(server *codexProbeServer) {
		server.start()
		server.request("native-approval", method, map[string]any{
			"threadId": "native-thread",
			"turnId":   "native-turn",
			"itemId":   "native-item",
		})
		response := server.read()
		var result struct {
			Decision string `json:"decision"`
		}
		if json.Unmarshal(response.Result, &result) != nil || result.Decision != "decline" {
			panic("expected declined approval")
		}
		server.interrupt()
	}
}
