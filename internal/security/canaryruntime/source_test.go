package canaryruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/security/canarysource"
)

const testRunID = "0123456789abcdef0123456789abcdef"

func TestSourcesProxyExactRuntimeSession(t *testing.T) {
	t.Parallel()

	session := newFakeSession("stderr")
	sources := validSources(t, session)
	if sources.Input() != session.input {
		t.Fatal("Input() did not preserve the exact session stream")
	}
	if sources.Output() != session.output {
		t.Fatal("Output() did not preserve the exact session stream")
	}
	if err := sources.Wait(); err != nil || session.waits != 1 {
		t.Fatalf("Wait() error = %v, calls = %d", err, session.waits)
	}
	if err := sources.Close(); err != nil || session.closes != 1 {
		t.Fatalf("Close() error = %v, calls = %d", err, session.closes)
	}
}

func TestSourcesCaptureExactRuntimeErrorsOnce(t *testing.T) {
	t.Parallel()

	session := newFakeSession("runtime stderr")
	sources := validSources(t, session)
	copied := *sources

	runtimeErrors := sources.Errors()
	capturedByRuntime, err := io.ReadAll(runtimeErrors)
	if err != nil {
		t.Fatalf("read runtime stderr: %v", err)
	}
	if err := runtimeErrors.Close(); err != nil {
		t.Fatalf("close runtime stderr: %v", err)
	}
	if string(capturedByRuntime) != "runtime stderr" {
		t.Fatalf("runtime stderr = %q", capturedByRuntime)
	}

	evidence, err := copied.OpenRuntimeErrors(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("OpenRuntimeErrors() error = %v", err)
	}
	inspected, err := io.ReadAll(evidence)
	if err != nil {
		t.Fatalf("read evidence stream: %v", err)
	}
	if string(inspected) != "runtime stderr" {
		t.Fatalf("evidence stream = %q", inspected)
	}
	if err := evidence.Close(); err != nil {
		t.Fatalf("close evidence stream: %v", err)
	}
	if _, err := sources.OpenRuntimeErrors(context.Background(), validRequest()); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("second OpenRuntimeErrors() error = %v", err)
	}
	if _, err := json.Marshal(sources); !errors.Is(err, ErrSerialization) {
		t.Fatalf("marshal sources error = %v", err)
	}
}

func TestSourcesCaptureEmptyRuntimeErrors(t *testing.T) {
	t.Parallel()

	sources := validSources(t, newFakeSession(""))
	stream := sources.Errors()
	if _, err := io.Copy(io.Discard, stream); err != nil {
		t.Fatalf("drain empty runtime stderr: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close empty runtime stderr: %v", err)
	}
	evidence, err := sources.OpenRuntimeErrors(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("OpenRuntimeErrors() error = %v", err)
	}
	if inspected, err := io.ReadAll(evidence); err != nil || len(inspected) != 0 {
		t.Fatalf("empty evidence = %q, error = %v", inspected, err)
	}
	if err := evidence.Close(); err != nil {
		t.Fatalf("close empty evidence: %v", err)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	var typedNil *fakeSession
	for name, mutate := range map[string]func(*Config){
		"run": func(config *Config) {
			config.RunID = "invalid"
		},
		"runtime": func(config *Config) {
			config.RuntimeName = "Invalid Runtime"
		},
		"session": func(config *Config) {
			config.Session = typedNil
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := validConfig(newFakeSession("stderr"))
			mutate(&config)
			if _, err := New(config); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("New() error = %v", err)
			}
		})
	}
}

func TestSourcesFailClosedOnLifecycleOrBindingDrift(t *testing.T) {
	t.Parallel()

	for name, exercise := range map[string]func(*Sources) error{
		"open before runtime": func(sources *Sources) error {
			_, err := sources.OpenRuntimeErrors(context.Background(), validRequest())
			return err
		},
		"run drift": func(sources *Sources) error {
			stream := sources.Errors()
			_, _ = io.Copy(io.Discard, stream)
			_ = stream.Close()
			request := validRequest()
			request.RunID = "fedcba9876543210fedcba9876543210"
			_, err := sources.OpenRuntimeErrors(context.Background(), request)
			return err
		},
		"surface drift": func(sources *Sources) error {
			stream := sources.Errors()
			_, _ = io.Copy(io.Discard, stream)
			_ = stream.Close()
			request := validRequest()
			request.Surface = "gateway-logs"
			_, err := sources.OpenRuntimeErrors(context.Background(), request)
			return err
		},
		"resource drift": func(sources *Sources) error {
			stream := sources.Errors()
			_, _ = io.Copy(io.Discard, stream)
			_ = stream.Close()
			request := validRequest()
			request.ResourceName = "other-runtime"
			_, err := sources.OpenRuntimeErrors(context.Background(), request)
			return err
		},
	} {
		name, exercise := name, exercise
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sources := validSources(t, newFakeSession("stderr"))
			if err := exercise(sources); !errors.Is(err, ErrCredentialSource) {
				t.Fatalf("exercise error = %v", err)
			}
			if _, err := sources.OpenRuntimeErrors(context.Background(), validRequest()); !errors.Is(err, ErrCredentialSource) {
				t.Fatalf("retry error = %v", err)
			}
		})
	}
}

func TestSourcesRejectDuplicateRuntimeErrorConsumers(t *testing.T) {
	t.Parallel()

	sources := validSources(t, newFakeSession("stderr"))
	first := sources.Errors()
	if _, err := io.Copy(io.Discard, first); err != nil {
		t.Fatalf("read first stream: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first stream: %v", err)
	}
	second := sources.Errors()
	if _, err := second.Read(make([]byte, 1)); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("duplicate Errors() read = %v", err)
	}
	if _, err := sources.OpenRuntimeErrors(context.Background(), validRequest()); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("OpenRuntimeErrors() error = %v", err)
	}
}

func TestSourcesDrainOversizedRuntimeErrorsButRejectEvidence(t *testing.T) {
	t.Parallel()

	session := newFakeSession("12345")
	sources, err := newWithLimit(validConfig(session), 4)
	if err != nil {
		t.Fatalf("newWithLimit() error = %v", err)
	}
	stream := sources.Errors()
	capturedByRuntime, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read runtime stderr: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close runtime stderr: %v", err)
	}
	if string(capturedByRuntime) != "12345" {
		t.Fatalf("runtime stderr was not completely drained: %q", capturedByRuntime)
	}
	if _, err := sources.OpenRuntimeErrors(context.Background(), validRequest()); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("OpenRuntimeErrors() error = %v", err)
	}
}

func TestSourcesRejectIncompleteRuntimeErrorDrain(t *testing.T) {
	t.Parallel()

	sources := validSources(t, newFakeSession("stderr"))
	stream := sources.Errors()
	if _, err := stream.Read(make([]byte, 1)); err != nil {
		t.Fatalf("read prefix: %v", err)
	}
	if err := stream.Close(); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("incomplete close error = %v", err)
	}
	if _, err := sources.OpenRuntimeErrors(context.Background(), validRequest()); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("OpenRuntimeErrors() error = %v", err)
	}
}

func TestSourcesConsumeCancelledEvidenceWait(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	session := newFakeSession("")
	session.errors = reader
	sources := validSources(t, session)
	stream := sources.Errors()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sources.OpenRuntimeErrors(ctx, validRequest()); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("OpenRuntimeErrors() error = %v", err)
	}
	_ = writer.Close()
	_, _ = io.Copy(io.Discard, stream)
	_ = stream.Close()
	if _, err := sources.OpenRuntimeErrors(context.Background(), validRequest()); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("retry error = %v", err)
	}
}

func TestSourcesDiscardUnusedCapture(t *testing.T) {
	t.Parallel()

	sources := validSources(t, newFakeSession("stderr"))
	stream := sources.Errors()
	_, _ = io.Copy(io.Discard, stream)
	_ = stream.Close()
	sources.Discard()
	sources.Discard()

	sources.state.mu.Lock()
	captured := len(sources.state.capture)
	sources.state.mu.Unlock()
	if captured != 0 {
		t.Fatalf("discard retained %d bytes", captured)
	}
	if _, err := sources.OpenRuntimeErrors(context.Background(), validRequest()); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("OpenRuntimeErrors() after discard = %v", err)
	}
}

func TestSourcesAbortWaitingHandoffOnCompetingOpen(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	session := newFakeSession("")
	session.errors = reader
	sources := validSources(t, session)
	stream := sources.Errors()
	result := make(chan error, 1)
	go func() {
		_, err := sources.OpenRuntimeErrors(context.Background(), validRequest())
		result <- err
	}()

	deadline := time.After(time.Second)
	for {
		sources.state.mu.Lock()
		started := sources.state.sourceOpened
		sources.state.mu.Unlock()
		if started {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first handoff did not start")
		default:
			runtime.Gosched()
		}
	}
	if _, err := sources.OpenRuntimeErrors(context.Background(), validRequest()); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("competing OpenRuntimeErrors() error = %v", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrCredentialSource) {
			t.Fatalf("waiting OpenRuntimeErrors() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting handoff was not aborted")
	}
	_ = writer.Close()
	_, _ = io.Copy(io.Discard, stream)
	_ = stream.Close()
}

func TestEvidenceStreamRequiresCompleteReadAndClearsContent(t *testing.T) {
	t.Parallel()

	sources := validSources(t, newFakeSession("stderr"))
	stream := sources.Errors()
	_, _ = io.Copy(io.Discard, stream)
	_ = stream.Close()
	evidence, err := sources.OpenRuntimeErrors(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("OpenRuntimeErrors() error = %v", err)
	}
	if _, err := evidence.Read(make([]byte, 1)); err != nil {
		t.Fatalf("read evidence prefix: %v", err)
	}
	if err := evidence.Close(); !errors.Is(err, ErrCredentialSource) {
		t.Fatalf("incomplete evidence close error = %v", err)
	}
	reader := evidence.(*sensitiveReader)
	if reader.content != nil || reader.reader.Len() != 0 {
		t.Fatal("evidence content remained after close")
	}
}

func validSources(t *testing.T, session *fakeSession) *Sources {
	t.Helper()

	sources, err := New(validConfig(session))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return sources
}

func validConfig(session *fakeSession) Config {
	return Config{
		RunID:       testRunID,
		RuntimeName: "runtime-invocation",
		Session:     session,
	}
}

func validRequest() canarysource.Request {
	return canarysource.Request{
		RunID:        testRunID,
		Surface:      "runtime-errors",
		ResourceName: "runtime-invocation",
	}
}

type fakeSession struct {
	input  io.WriteCloser
	output io.ReadCloser
	errors io.ReadCloser
	waits  int
	closes int
}

func newFakeSession(stderr string) *fakeSession {
	return &fakeSession{
		input:  nopWriteCloser{Writer: io.Discard},
		output: io.NopCloser(strings.NewReader("")),
		errors: io.NopCloser(bytes.NewBufferString(stderr)),
	}
}

func (session *fakeSession) Input() io.WriteCloser { return session.input }
func (session *fakeSession) Output() io.ReadCloser { return session.output }
func (session *fakeSession) Errors() io.ReadCloser { return session.errors }
func (session *fakeSession) Wait() error {
	session.waits++
	return nil
}
func (session *fakeSession) Close() error {
	session.closes++
	return errors.Join(session.input.Close(), session.output.Close(), session.errors.Close())
}

type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }
