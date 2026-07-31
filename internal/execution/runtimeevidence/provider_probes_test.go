package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

func TestOpenShellProbesOwnProviderObservations(t *testing.T) {
	t.Parallel()

	fixture := newOpenShellProbeFixture()
	probes := fixture.open(t)
	request := ProbeRequest{RunID: testRunID, Resources: namesForRun(testRunID)}

	gateway, err := probes.GatewayReady(context.Background(), request)
	if err != nil || !gateway.Assertions.GatewayReady {
		t.Fatalf("GatewayReady() = %#v, %v", gateway, err)
	}
	sandbox, err := probes.SandboxReady(context.Background(), request)
	if err != nil || !sandbox.Assertions.SandboxReady {
		t.Fatalf("SandboxReady() = %#v, %v", sandbox, err)
	}
	artifact, err := probes.ArtifactExport(context.Background(), request)
	if err != nil || !artifact.Assertions.ArtifactExported {
		t.Fatalf("ArtifactExport() = %#v, %v", artifact, err)
	}
	if fixture.provider.exportContent[0] != 0 {
		t.Fatal("ArtifactExport() retained exported content")
	}
	teardown, err := probes.SandboxTeardown(context.Background(), request)
	if err != nil || !teardown.Assertions.SandboxRemoved {
		t.Fatalf("SandboxTeardown() = %#v, %v", teardown, err)
	}
	if !fixture.provider.terminated {
		t.Fatal("SandboxTeardown() did not terminate the execution")
	}
	if gateway.ObservationSHA256 == sandbox.ObservationSHA256 ||
		sandbox.ObservationSHA256 == artifact.ObservationSHA256 ||
		artifact.ObservationSHA256 == teardown.ObservationSHA256 {
		t.Fatal("provider probe commitments were replayed")
	}
}

func TestOpenShellProbesRunThroughConcreteScenario(t *testing.T) {
	t.Parallel()

	fixture := newOpenShellProbeFixture()
	probes := fixture.open(t)
	runtimeProbes := &scenarioProbes{
		base: time.Date(2026, time.July, 31, 12, 1, 0, 0, time.UTC),
	}
	scenario, err := NewConcreteScenario(ScenarioConfig{
		RunID:    testRunID,
		Provider: probes,
		Runtime:  runtimeProbes,
	})
	if err != nil {
		t.Fatalf("NewConcreteScenario() error = %v", err)
	}
	binding := testLiveBinding()
	for _, name := range requiredChecks {
		if _, err := dispatchLiveCase(context.Background(), scenario, binding, name); err != nil {
			t.Fatalf("dispatchLiveCase(%q) error = %v", name, err)
		}
	}
}

func TestOpenShellProbesFailClosed(t *testing.T) {
	t.Parallel()

	t.Run("gateway binding drift is terminal", func(t *testing.T) {
		fixture := newOpenShellProbeFixture()
		fixture.store.gateway.Endpoint = "http://127.0.0.1:8082"
		probes := fixture.open(t)
		request := testProbeRequest()
		if _, err := probes.GatewayReady(context.Background(), request); !errors.Is(err, ErrOpenShellProbeObservation) {
			t.Fatalf("GatewayReady() error = %v", err)
		}
		fixture.store.gateway.Endpoint = gatewayEndpoint
		if _, err := probes.GatewayReady(context.Background(), request); !errors.Is(err, ErrOpenShellProbeOrder) {
			t.Fatalf("GatewayReady() after rejection error = %v", err)
		}
	})

	t.Run("portable binding drift is terminal", func(t *testing.T) {
		fixture := newOpenShellProbeFixture()
		probes := fixture.open(t)
		request := testProbeRequest()
		request.Resources.Provider = "replacement"
		if _, err := probes.GatewayReady(context.Background(), request); !errors.Is(err, ErrOpenShellProbeOrder) {
			t.Fatalf("GatewayReady() error = %v", err)
		}
		if _, err := probes.GatewayReady(context.Background(), testProbeRequest()); !errors.Is(err, ErrOpenShellProbeOrder) {
			t.Fatalf("GatewayReady() after binding drift error = %v", err)
		}
	})

	t.Run("out of order use is terminal", func(t *testing.T) {
		fixture := newOpenShellProbeFixture()
		probes := fixture.open(t)
		if _, err := probes.SandboxReady(context.Background(), testProbeRequest()); !errors.Is(err, ErrOpenShellProbeOrder) {
			t.Fatalf("SandboxReady() error = %v", err)
		}
		if _, err := probes.GatewayReady(context.Background(), testProbeRequest()); !errors.Is(err, ErrOpenShellProbeOrder) {
			t.Fatalf("GatewayReady() after order drift error = %v", err)
		}
	})

	t.Run("private errors are sanitized", func(t *testing.T) {
		privateErr := errors.New("native endpoint and sandbox")
		fixture := newOpenShellProbeFixture()
		fixture.store.gatewayErr = privateErr
		probes := fixture.open(t)
		_, err := probes.GatewayReady(context.Background(), testProbeRequest())
		if !errors.Is(err, ErrOpenShellProbeObservation) || errors.Is(err, privateErr) {
			t.Fatalf("GatewayReady() error = %v", err)
		}
	})

	t.Run("artifact substitution is rejected and cleared", func(t *testing.T) {
		fixture := newOpenShellProbeFixture()
		probes := fixture.open(t)
		request := testProbeRequest()
		if _, err := probes.GatewayReady(context.Background(), request); err != nil {
			t.Fatalf("GatewayReady() error = %v", err)
		}
		if _, err := probes.SandboxReady(context.Background(), request); err != nil {
			t.Fatalf("SandboxReady() error = %v", err)
		}
		fixture.provider.exportContent = []byte("replacement")
		if _, err := probes.ArtifactExport(context.Background(), request); !errors.Is(err, ErrOpenShellProbeObservation) {
			t.Fatalf("ArtifactExport() error = %v", err)
		}
		if fixture.provider.exportContent[0] != 0 {
			t.Fatal("ArtifactExport() retained rejected content")
		}
	})

	t.Run("uncertain teardown is rejected", func(t *testing.T) {
		fixture := newOpenShellProbeFixture()
		probes := fixture.open(t)
		request := testProbeRequest()
		if _, err := probes.GatewayReady(context.Background(), request); err != nil {
			t.Fatalf("GatewayReady() error = %v", err)
		}
		if _, err := probes.SandboxReady(context.Background(), request); err != nil {
			t.Fatalf("SandboxReady() error = %v", err)
		}
		if _, err := probes.ArtifactExport(context.Background(), request); err != nil {
			t.Fatalf("ArtifactExport() error = %v", err)
		}
		fixture.provider.terminateErr = errors.New("lost acknowledgement")
		if _, err := probes.SandboxTeardown(context.Background(), request); !errors.Is(err, ErrOpenShellProbeObservation) {
			t.Fatalf("SandboxTeardown() error = %v", err)
		}
	})

	t.Run("clock regression is rejected", func(t *testing.T) {
		fixture := newOpenShellProbeFixture()
		fixture.clock.regress = true
		probes := fixture.open(t)
		if _, err := probes.GatewayReady(context.Background(), testProbeRequest()); !errors.Is(err, ErrOpenShellProbeObservation) {
			t.Fatalf("GatewayReady() error = %v", err)
		}
	})

	t.Run("configuration cannot be serialized", func(t *testing.T) {
		fixture := newOpenShellProbeFixture()
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

type openShellProbeFixture struct {
	store    *probeStore
	provider *probeProvider
	clock    *probeClock
}

func newOpenShellProbeFixture() *openShellProbeFixture {
	resources := namesForRun(testRunID)
	domain := runtimeIsolationDomain(testRunID)
	executionID := "exe-runtime-conformance"
	clock := &probeClock{
		value: time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC),
	}
	store := &probeStore{
		gateway: execution.GatewayRecord{
			Gateway: execution.Gateway{
				IsolationDomainID: domain,
				ID:                resources.Gateway,
				Driver:            driver,
				State:             execution.GatewayActive,
				Capabilities:      []string{openShellRuntimeCapability},
			},
			Endpoint: gatewayEndpoint,
		},
		execution: execution.ExecutionRecord{
			Execution: execution.Execution{
				IsolationDomainID: domain,
				ID:                executionID,
				GatewayID:         resources.Gateway,
				State:             "ready",
			},
			PlacementID: "plc-runtime-conformance",
			OperationID: runtimeOperationID(testRunID),
			SandboxName: "native-sandbox",
		},
	}
	provider := &probeProvider{
		store:         store,
		clock:         clock,
		exportContent: runtimeArtifactContent(testRunID),
	}
	return &openShellProbeFixture{
		store:    store,
		provider: provider,
		clock:    clock,
	}
}

func (fixture *openShellProbeFixture) config() OpenShellProbeConfig {
	return OpenShellProbeConfig{
		RunID:       testRunID,
		ExecutionID: fixture.store.execution.Execution.ID,
		Store:       fixture.store,
		Provider:    fixture.provider,
		Now:         fixture.clock.now,
	}
}

func (fixture *openShellProbeFixture) open(t *testing.T) *OpenShellProbes {
	t.Helper()
	probes, err := NewOpenShellProbes(fixture.config())
	if err != nil {
		t.Fatalf("NewOpenShellProbes() error = %v", err)
	}
	return probes
}

type probeStore struct {
	gateway      execution.GatewayRecord
	execution    execution.ExecutionRecord
	gatewayErr   error
	executionErr error
}

func (store *probeStore) GetGateway(
	context.Context,
	string,
	string,
) (execution.GatewayRecord, error) {
	return store.gateway, store.gatewayErr
}

func (store *probeStore) GetExecution(
	context.Context,
	execution.ExecutionRef,
) (execution.ExecutionRecord, error) {
	return store.execution, store.executionErr
}

type probeProvider struct {
	store         *probeStore
	clock         *probeClock
	exportContent []byte
	observeErr    error
	exportErr     error
	terminateErr  error
	terminated    bool
}

func (provider *probeProvider) Observe(
	context.Context,
	execution.ExecutionRef,
) (execution.Observation, error) {
	if provider.observeErr != nil {
		return execution.Observation{}, provider.observeErr
	}
	state := "ready"
	if provider.terminated {
		state = "terminated"
	}
	return execution.Observation{
		IsolationDomainID: provider.store.execution.Execution.IsolationDomainID,
		ExecutionID:       provider.store.execution.Execution.ID,
		State:             state,
		ObservedAt:        provider.clock.now(),
	}, nil
}

func (provider *probeProvider) Export(
	context.Context,
	execution.ExportRequest,
) (execution.ExportResult, error) {
	return execution.ExportResult{
		IsolationDomainID: provider.store.execution.Execution.IsolationDomainID,
		ExecutionID:       provider.store.execution.Execution.ID,
		Content:           provider.exportContent,
	}, provider.exportErr
}

func (provider *probeProvider) Terminate(
	context.Context,
	execution.ExecutionRef,
) error {
	if provider.terminateErr != nil {
		return provider.terminateErr
	}
	provider.terminated = true
	provider.store.execution.Execution.State = "terminated"
	return nil
}

type probeClock struct {
	value   time.Time
	regress bool
}

func (clock *probeClock) now() time.Time {
	if clock.regress {
		clock.value = clock.value.Add(-time.Millisecond)
		return clock.value
	}
	clock.value = clock.value.Add(time.Millisecond)
	return clock.value
}

func testProbeRequest() ProbeRequest {
	return ProbeRequest{RunID: testRunID, Resources: namesForRun(testRunID)}
}
