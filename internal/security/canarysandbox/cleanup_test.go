package canarysandbox

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/security/canaryevidence"
)

const testRunID = "0123456789abcdef0123456789abcdef"

func TestAdapterOwnsExactSandboxCleanup(t *testing.T) {
	provider := &providerStub{observation: terminatedObservation()}
	adapter, err := New(Config{RunID: testRunID, Execution: testExecution()}, provider)
	if err != nil {
		t.Fatal(err)
	}
	if adapter.Name() != testExecution().ID {
		t.Fatalf("Name() = %q", adapter.Name())
	}
	request := cleanupRequest()
	if err := adapter.Cleanup(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := provider.callNames(); !equalStrings(got, []string{"terminate", "observe"}) {
		t.Fatalf("provider calls = %v", got)
	}
	if provider.terminateRef != provider.observeRef ||
		provider.terminateRef != (execution.ExecutionRef{
			IsolationDomainID: testExecution().IsolationDomainID,
			ID:                testExecution().ID,
		}) {
		t.Fatalf("provider refs = %#v, %#v", provider.terminateRef, provider.observeRef)
	}
}

func TestAdapterRejectsUnboundCleanupRequests(t *testing.T) {
	tests := []canaryevidence.CleanupRequest{
		{RunID: "fedcba9876543210fedcba9876543210", ResourceKind: "sandbox", ResourceName: testExecution().ID},
		{RunID: testRunID, ResourceKind: "provider", ResourceName: testExecution().ID},
		{RunID: testRunID, ResourceKind: "sandbox", ResourceName: "exe_other"},
	}
	for _, request := range tests {
		provider := &providerStub{observation: terminatedObservation()}
		adapter, err := New(Config{RunID: testRunID, Execution: testExecution()}, provider)
		if err != nil {
			t.Fatal(err)
		}
		if err := adapter.Cleanup(context.Background(), request); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("Cleanup(%#v) error = %v", request, err)
		}
		if got := provider.callNames(); len(got) != 0 {
			t.Fatalf("provider calls = %v", got)
		}
	}
}

func TestAdapterRejectsInvalidConfiguration(t *testing.T) {
	valid := Config{RunID: testRunID, Execution: testExecution()}
	tests := []struct {
		config   Config
		provider Provider
	}{
		{config: valid},
		{config: Config{RunID: "not-a-run", Execution: testExecution()}, provider: &providerStub{}},
		{config: Config{RunID: testRunID, Execution: execution.Execution{}}, provider: &providerStub{}},
		{config: Config{
			RunID: testRunID,
			Execution: execution.Execution{
				IsolationDomainID: "iso-test",
				ID:                "invalid/resource",
				GatewayID:         "gateway-test",
			},
		}, provider: &providerStub{}},
	}
	for _, test := range tests {
		if _, err := New(test.config, test.provider); !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("New(%#v) error = %v", test.config, err)
		}
	}
}

func TestAdapterPreservesCancellationWithoutProviderAccess(t *testing.T) {
	provider := &providerStub{observation: terminatedObservation()}
	adapter, err := New(Config{RunID: testRunID, Execution: testExecution()}, provider)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = adapter.Cleanup(ctx, cleanupRequest())
	if !errors.Is(err, ErrCleanupFailure) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if got := provider.callNames(); len(got) != 0 {
		t.Fatalf("provider calls = %v", got)
	}
}

func TestAdapterSanitizesProviderFailures(t *testing.T) {
	privateErr := errors.New("private provider failure")
	tests := []struct {
		name        string
		provider    *providerStub
		want        error
		wantObserve bool
	}{
		{
			name:     "terminate",
			provider: &providerStub{terminateErr: privateErr},
			want:     ErrCleanupFailure,
		},
		{
			name:        "observe",
			provider:    &providerStub{observeErr: privateErr},
			want:        ErrCleanupFailure,
			wantObserve: true,
		},
		{
			name: "mismatched observation",
			provider: &providerStub{
				observation: execution.Observation{
					IsolationDomainID: testExecution().IsolationDomainID,
					ExecutionID:       "exe_other",
					State:             "terminated",
				},
			},
			want:        ErrCleanupUncertain,
			wantObserve: true,
		},
		{
			name: "nonterminal observation",
			provider: &providerStub{
				observation: execution.Observation{
					IsolationDomainID: testExecution().IsolationDomainID,
					ExecutionID:       testExecution().ID,
					State:             "running",
				},
			},
			want:        ErrCleanupUncertain,
			wantObserve: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, err := New(Config{RunID: testRunID, Execution: testExecution()}, test.provider)
			if err != nil {
				t.Fatal(err)
			}
			err = adapter.Cleanup(context.Background(), cleanupRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("Cleanup() error = %v", err)
			}
			if errors.Is(err, privateErr) {
				t.Fatalf("Cleanup() exposed provider error: %v", err)
			}
			calls := test.provider.callNames()
			wantCalls := []string{"terminate"}
			if test.wantObserve {
				wantCalls = append(wantCalls, "observe")
			}
			if !equalStrings(calls, wantCalls) {
				t.Fatalf("provider calls = %v", calls)
			}
		})
	}
}

func TestAdapterCleanupIsConcurrentAndIdempotent(t *testing.T) {
	provider := &providerStub{observation: terminatedObservation()}
	adapter, err := New(Config{RunID: testRunID, Execution: testExecution()}, provider)
	if err != nil {
		t.Fatal(err)
	}
	const callers = 16
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- adapter.Cleanup(context.Background(), cleanupRequest())
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Cleanup() error = %v", err)
		}
	}
	if got := provider.callNames(); !equalStrings(got, []string{"terminate", "observe"}) {
		t.Fatalf("provider calls = %v", got)
	}
}

func TestAdapterRefusesSerialization(t *testing.T) {
	adapter, err := New(
		Config{RunID: testRunID, Execution: testExecution()},
		&providerStub{observation: terminatedObservation()},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(adapter); !errors.Is(err, ErrSerialization) {
		t.Fatalf("json.Marshal() error = %v", err)
	}
}

type providerStub struct {
	mu           sync.Mutex
	calls        []string
	terminateRef execution.ExecutionRef
	observeRef   execution.ExecutionRef
	terminateErr error
	observation  execution.Observation
	observeErr   error
}

func (provider *providerStub) Terminate(_ context.Context, ref execution.ExecutionRef) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls = append(provider.calls, "terminate")
	provider.terminateRef = ref
	return provider.terminateErr
}

func (provider *providerStub) Observe(_ context.Context, ref execution.ExecutionRef) (execution.Observation, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	provider.calls = append(provider.calls, "observe")
	provider.observeRef = ref
	return provider.observation, provider.observeErr
}

func (provider *providerStub) callNames() []string {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]string(nil), provider.calls...)
}

func testExecution() execution.Execution {
	return execution.Execution{
		IsolationDomainID: "iso-test",
		ID:                "exe_0123456789abcdef",
		GatewayID:         "gateway-test",
		State:             "running",
	}
}

func terminatedObservation() execution.Observation {
	return execution.Observation{
		IsolationDomainID: testExecution().IsolationDomainID,
		ExecutionID:       testExecution().ID,
		State:             "terminated",
	}
}

func cleanupRequest() canaryevidence.CleanupRequest {
	return canaryevidence.CleanupRequest{
		RunID:        testRunID,
		ResourceKind: "sandbox",
		ResourceName: testExecution().ID,
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
