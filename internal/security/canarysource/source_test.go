package canarysource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/asabla/dataground/internal/security/canarycollect"
)

const testRunID = "0123456789abcdef0123456789abcdef"

func TestAdapterCollectsExactSourcesOnce(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{}
	adapter := validAdapter(t, backend)
	copied := *adapter
	collection, err := adapter.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}

	backend.mu.Lock()
	order := append([]string(nil), backend.order...)
	requests := append([]Request(nil), backend.requests...)
	backend.mu.Unlock()
	expectedOrder := []string{
		"sandbox-process",
		"sandbox-environment",
		"sandbox-filesystem",
		"provider-arguments",
		"gateway-logs",
		"sandbox-logs",
		"runtime-errors",
	}
	if strings.Join(order, ",") != strings.Join(expectedOrder, ",") {
		t.Fatalf("acquisition order = %v", order)
	}
	resources := validResources()
	for index, request := range requests {
		if request.RunID != testRunID ||
			request.Surface != expectedOrder[index] ||
			request.ResourceName != resourceName(resources, request.Surface) {
			t.Fatalf("request %d = %+v", index, request)
		}
	}

	encoded, err := json.Marshal(collection)
	if err != nil {
		t.Fatalf("marshal collection: %v", err)
	}
	if bytes.Contains(encoded, []byte("source content")) {
		t.Fatalf("collection retained source content: %s", encoded)
	}
	if _, err := adapter.Collect(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Collect() error = %v", err)
	}
	if _, err := copied.Collect(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("copied Collect() error = %v", err)
	}
	if _, err := json.Marshal(adapter); !errors.Is(err, ErrSerialization) {
		t.Fatalf("marshal adapter error = %v", err)
	}
}

func TestNewRejectsInvalidPlanBeforeAcquisition(t *testing.T) {
	t.Parallel()

	var typedNil *fakeBackend
	for name, mutate := range map[string]func(*Config){
		"run": func(config *Config) {
			config.RunID = ""
		},
		"commitment": func(config *Config) {
			config.CanaryCommitment = "sha256:invalid"
		},
		"resource": func(config *Config) {
			config.Resources.Sandbox = "Invalid Sandbox"
		},
		"openshell": func(config *Config) {
			config.OpenShell = nil
		},
		"docker": func(config *Config) {
			config.Docker = typedNil
		},
		"runtime": func(config *Config) {
			config.Runtime = nil
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			backend := &fakeBackend{}
			config := validAdapterConfig(backend)
			mutate(&config)
			if _, err := New(config); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("New() error = %v", err)
			}
			backend.mu.Lock()
			acquisitions := len(backend.order)
			backend.mu.Unlock()
			if acquisitions != 0 {
				t.Fatalf("New() acquired %d sources", acquisitions)
			}
		})
	}
}

func TestAdapterRejectsBindingDriftBeforeAcquisition(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*Config){
		"run": func(config *Config) {
			config.RunID = "fedcba9876543210fedcba9876543210"
		},
		"commitment": func(config *Config) {
			config.CanaryCommitment = "sha256:" + strings.Repeat("0", 64)
		},
		"resources": func(config *Config) {
			config.Resources.Runtime = "other-runtime"
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			backend := &fakeBackend{}
			config := validAdapterConfig(backend)
			adapter, err := New(config)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			mutate(&config)
			if err := adapter.ValidateBinding(
				config.RunID,
				config.CanaryCommitment,
				config.Resources,
			); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("ValidateBinding() error = %v", err)
			}
			backend.mu.Lock()
			acquisitions := len(backend.order)
			backend.mu.Unlock()
			if acquisitions != 0 {
				t.Fatalf("ValidateBinding() acquired %d sources", acquisitions)
			}
		})
	}
}

func TestAdapterSanitizesBackendFailureAndClosesAmbiguousSource(t *testing.T) {
	t.Parallel()

	ambiguousCloses := 0
	backend := &fakeBackend{
		open: func(request Request) (io.ReadCloser, error) {
			if request.Surface == "sandbox-environment" {
				return &trackedReadCloser{
					Reader: strings.NewReader("unread"),
					close:  func() { ambiguousCloses++ },
				}, errors.New("sensitive backend payload")
			}
			return io.NopCloser(strings.NewReader("safe")), nil
		},
	}
	adapter := validAdapter(t, backend)
	collection, err := adapter.Collect(context.Background())
	if !errors.Is(err, canarycollect.ErrAcquisition) {
		t.Fatalf("Collect() error = %v", err)
	}
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Collect() leaked backend failure: %v", err)
	}
	if ambiguousCloses != 1 {
		t.Fatalf("ambiguous source closes = %d", ambiguousCloses)
	}
	if _, err := json.Marshal(collection); !errors.Is(err, canarycollect.ErrCollectionIncomplete) {
		t.Fatalf("marshal partial collection error = %v", err)
	}
}

func TestAdapterConsumesCancelledAttemptWithoutOpeningSources(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{}
	adapter := validAdapter(t, backend)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := adapter.Collect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect() error = %v", err)
	}
	backend.mu.Lock()
	acquisitions := len(backend.order)
	backend.mu.Unlock()
	if acquisitions != 0 {
		t.Fatalf("cancelled Collect() acquired %d sources", acquisitions)
	}
	if _, err := adapter.Collect(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("retry Collect() error = %v", err)
	}
}

func TestAdapterPermitsOnlyOneConcurrentCollection(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{}
	adapter := validAdapter(t, backend)
	const contenders = 8
	start := make(chan struct{})
	results := make(chan error, contenders)
	var group sync.WaitGroup
	for range contenders {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := adapter.Collect(context.Background())
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)

	successes := 0
	rejected := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrAlreadyStarted):
			rejected++
		default:
			t.Fatalf("concurrent Collect() error = %v", err)
		}
	}
	if successes != 1 || rejected != contenders-1 {
		t.Fatalf("concurrent results: successes=%d rejected=%d", successes, rejected)
	}
	backend.mu.Lock()
	acquisitions := len(backend.order)
	backend.mu.Unlock()
	if acquisitions != 7 {
		t.Fatalf("concurrent acquisition count = %d", acquisitions)
	}
}

func validAdapter(t *testing.T, backend *fakeBackend) *Adapter {
	t.Helper()

	adapter, err := New(validAdapterConfig(backend))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return adapter
}

func validAdapterConfig(backend *fakeBackend) Config {
	digest := sha256.Sum256([]byte("test canary"))
	return Config{
		RunID:            testRunID,
		CanaryCommitment: "sha256:" + hex.EncodeToString(digest[:]),
		Resources:        validResources(),
		OpenShell:        backend,
		Docker:           backend,
		Runtime:          backend,
	}
}

func validResources() ResourceNames {
	return ResourceNames{
		Gateway:  "dataground-gateway",
		Sandbox:  "sandbox-credential-check",
		Provider: "dg-canary-provider-" + testRunID,
		Runtime:  "runtime-invocation",
	}
}

func resourceName(resources ResourceNames, surface string) string {
	switch surface {
	case "provider-arguments":
		return resources.Provider
	case "gateway-logs":
		return resources.Gateway
	case "runtime-errors":
		return resources.Runtime
	default:
		return resources.Sandbox
	}
}

type fakeBackend struct {
	mu       sync.Mutex
	order    []string
	requests []Request
	open     func(Request) (io.ReadCloser, error)
}

func (backend *fakeBackend) acquire(request Request) (io.ReadCloser, error) {
	backend.mu.Lock()
	backend.order = append(backend.order, request.Surface)
	backend.requests = append(backend.requests, request)
	open := backend.open
	backend.mu.Unlock()
	if open != nil {
		return open(request)
	}
	return io.NopCloser(strings.NewReader("source content for " + request.Surface)), nil
}

func (backend *fakeBackend) OpenSandboxProcess(_ context.Context, request Request) (io.ReadCloser, error) {
	return backend.acquire(request)
}

func (backend *fakeBackend) OpenSandboxEnvironment(_ context.Context, request Request) (io.ReadCloser, error) {
	return backend.acquire(request)
}

func (backend *fakeBackend) OpenSandboxFilesystem(_ context.Context, request Request) (io.ReadCloser, error) {
	return backend.acquire(request)
}

func (backend *fakeBackend) OpenSandboxLogs(_ context.Context, request Request) (io.ReadCloser, error) {
	return backend.acquire(request)
}

func (backend *fakeBackend) OpenProviderArguments(_ context.Context, request Request) (io.ReadCloser, error) {
	return backend.acquire(request)
}

func (backend *fakeBackend) OpenGatewayLogs(_ context.Context, request Request) (io.ReadCloser, error) {
	return backend.acquire(request)
}

func (backend *fakeBackend) OpenRuntimeErrors(_ context.Context, request Request) (io.ReadCloser, error) {
	return backend.acquire(request)
}

type trackedReadCloser struct {
	io.Reader
	close func()
}

func (source *trackedReadCloser) Close() error {
	source.close()
	return nil
}
