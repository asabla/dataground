package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestHarnessOwnsCompleteRuntimeEvidenceChain(t *testing.T) {
	t.Parallel()

	providerFixture := newOpenShellProbeFixture()
	runtimeFixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
	provider := &harnessTestProvider{
		probeProvider:      providerFixture.provider,
		codexProbeProvider: runtimeFixture.provider,
	}
	var cleanupMu sync.Mutex
	var cleanupKinds []string
	cleanup := func(_ context.Context, request CleanupRequest) error {
		cleanupMu.Lock()
		defer cleanupMu.Unlock()
		cleanupKinds = append(cleanupKinds, request.ResourceKind)
		return nil
	}
	harness, err := newHarness(HarnessConfig{
		RunID:       testRunID,
		Provenance:  Provenance{SourceCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WorkflowRunID: 42},
		ExecutionID: providerFixture.store.execution.Execution.ID,
		Store:       providerFixture.store,
		Provider:    provider,
		Cleanup: Cleanup{
			Sandbox:         cleanup,
			ProviderBinding: cleanup,
			Workspace:       cleanup,
		},
	}, providerFixture.clock.now)
	if err != nil {
		t.Fatalf("newHarness() error = %v", err)
	}

	result, err := harness.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	var record struct {
		Result string `json:"result"`
		Checks []struct {
			Name CheckName `json:"name"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(encoded, &record); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v", err)
	}
	if record.Result != statusPassed || len(record.Checks) != len(requiredChecks) {
		t.Fatalf("record = %#v", record)
	}
	for index, check := range record.Checks {
		if check.Name != requiredChecks[index] {
			t.Fatalf("check %d = %q, want %q", index, check.Name, requiredChecks[index])
		}
	}
	if runtimeFixture.provider.opened != len(codexProbeOrder) {
		t.Fatalf("opened runtime sessions = %d, want %d", runtimeFixture.provider.opened, len(codexProbeOrder))
	}
	if !providerFixture.provider.terminated {
		t.Fatal("provider probes did not terminate the sandbox")
	}
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	if got := len(cleanupKinds); got != 3 ||
		cleanupKinds[0] != "sandbox" ||
		cleanupKinds[1] != "provider" ||
		cleanupKinds[2] != "workspace" {
		t.Fatalf("cleanup order = %v", cleanupKinds)
	}
}

func TestHarnessSanitizesFailureAndCompletesCleanup(t *testing.T) {
	t.Parallel()

	providerFixture := newOpenShellProbeFixture()
	runtimeFixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
	privateErr := errors.New("private native endpoint failed")
	runtimeFixture.provider.openErr = privateErr
	provider := &harnessTestProvider{
		probeProvider:      providerFixture.provider,
		codexProbeProvider: runtimeFixture.provider,
	}
	var cleanupKinds []string
	cleanup := func(_ context.Context, request CleanupRequest) error {
		cleanupKinds = append(cleanupKinds, request.ResourceKind)
		return nil
	}
	harness, err := newHarness(HarnessConfig{
		RunID:       testRunID,
		Provenance:  Provenance{SourceCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WorkflowRunID: 42},
		ExecutionID: providerFixture.store.execution.Execution.ID,
		Store:       providerFixture.store,
		Provider:    provider,
		Cleanup: Cleanup{
			Sandbox:         cleanup,
			ProviderBinding: cleanup,
			Workspace:       cleanup,
		},
	}, providerFixture.clock.now)
	if err != nil {
		t.Fatalf("newHarness() error = %v", err)
	}

	result, err := harness.Run(context.Background())
	if !errors.Is(err, ErrHarnessRun) ||
		!errors.Is(err, ErrRunIncomplete) ||
		errors.Is(err, privateErr) ||
		strings.Contains(err.Error(), "private") {
		t.Fatalf("Run() error = %v", err)
	}
	if got := len(cleanupKinds); got != 3 ||
		cleanupKinds[0] != "sandbox" ||
		cleanupKinds[1] != "provider" ||
		cleanupKinds[2] != "workspace" {
		t.Fatalf("cleanup order = %v", cleanupKinds)
	}
	if _, marshalErr := json.Marshal(result); !errors.Is(marshalErr, ErrRunIncomplete) {
		t.Fatalf("json.Marshal(incomplete result) error = %v", marshalErr)
	}
}

func TestHarnessIsClosedAndSingleUse(t *testing.T) {
	t.Parallel()

	providerFixture := newOpenShellProbeFixture()
	runtimeFixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
	provider := &harnessTestProvider{
		probeProvider:      providerFixture.provider,
		codexProbeProvider: runtimeFixture.provider,
	}
	cleanup := func(context.Context, CleanupRequest) error { return nil }
	config := HarnessConfig{
		RunID:       testRunID,
		Provenance:  Provenance{SourceCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WorkflowRunID: 42},
		ExecutionID: providerFixture.store.execution.Execution.ID,
		Store:       providerFixture.store,
		Provider:    provider,
		Cleanup: Cleanup{
			Sandbox:         cleanup,
			ProviderBinding: cleanup,
			Workspace:       cleanup,
		},
	}
	harness, err := newHarness(config, providerFixture.clock.now)
	if err != nil {
		t.Fatalf("newHarness() error = %v", err)
	}
	if _, err := json.Marshal(config); !errors.Is(err, ErrSerialization) {
		t.Fatalf("json.Marshal(config) error = %v", err)
	}
	if _, err := json.Marshal(harness); !errors.Is(err, ErrSerialization) {
		t.Fatalf("json.Marshal(harness) error = %v", err)
	}
	if _, err := harness.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	copyOfHarness := *harness
	if _, err := copyOfHarness.Run(context.Background()); !errors.Is(err, ErrHarnessRun) ||
		!errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Run() error = %v", err)
	}
}

func TestNewHarnessRejectsInvalidComposition(t *testing.T) {
	t.Parallel()

	providerFixture := newOpenShellProbeFixture()
	runtimeFixture := newCodexProbeFixture(codexProbeScripts(testRunID)...)
	provider := &harnessTestProvider{
		probeProvider:      providerFixture.provider,
		codexProbeProvider: runtimeFixture.provider,
	}
	cleanup := func(context.Context, CleanupRequest) error { return nil }
	base := HarnessConfig{
		RunID:       testRunID,
		Provenance:  Provenance{SourceCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", WorkflowRunID: 42},
		ExecutionID: providerFixture.store.execution.Execution.ID,
		Store:       providerFixture.store,
		Provider:    provider,
		Cleanup: Cleanup{
			Sandbox:         cleanup,
			ProviderBinding: cleanup,
			Workspace:       cleanup,
		},
	}
	var typedNilProvider *harnessTestProvider
	for name, mutate := range map[string]func(*HarnessConfig){
		"run":                func(config *HarnessConfig) { config.RunID = "invalid" },
		"provenance":         func(config *HarnessConfig) { config.Provenance.SourceCommit = "invalid" },
		"workflow":           func(config *HarnessConfig) { config.Provenance.WorkflowRunID = 0 },
		"execution":          func(config *HarnessConfig) { config.ExecutionID = "" },
		"store":              func(config *HarnessConfig) { config.Store = nil },
		"provider":           func(config *HarnessConfig) { config.Provider = nil },
		"typed nil provider": func(config *HarnessConfig) { config.Provider = typedNilProvider },
		"cleanup":            func(config *HarnessConfig) { config.Cleanup.Workspace = nil },
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := base
			mutate(&config)
			if _, err := NewHarness(config); !errors.Is(err, ErrHarnessConfiguration) {
				t.Fatalf("NewHarness() error = %v", err)
			}
		})
	}
}

type harnessTestProvider struct {
	*probeProvider
	*codexProbeProvider
}
