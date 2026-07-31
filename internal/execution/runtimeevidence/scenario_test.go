package runtimeevidence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestConcreteScenarioRunsThroughLiveCases(t *testing.T) {
	t.Parallel()

	probes := &scenarioProbes{base: time.Date(2026, time.July, 30, 13, 0, 0, 0, time.UTC)}
	scenario, err := NewConcreteScenario(ScenarioConfig{
		RunID:    testRunID,
		Provider: probes,
		Runtime:  probes,
	})
	if err != nil {
		t.Fatalf("NewConcreteScenario() error = %v", err)
	}
	cases, err := NewLiveCases(testRunID, scenario)
	if err != nil {
		t.Fatalf("NewLiveCases() error = %v", err)
	}
	for _, name := range requiredChecks {
		if _, err := cases.Run(context.Background(), testLiveRequest(name)); err != nil {
			t.Fatalf("Run(%q) error = %v", name, err)
		}
	}
	if err := cases.FinalizeBinding(testRunID, namesForRun(testRunID)); err != nil {
		t.Fatalf("FinalizeBinding() error = %v", err)
	}
	if len(probes.calls) != len(requiredChecks) {
		t.Fatalf("probe calls = %d, want %d", len(probes.calls), len(requiredChecks))
	}
}

func TestConcreteScenarioFailsClosed(t *testing.T) {
	t.Parallel()

	t.Run("wrong assertion", func(t *testing.T) {
		probes := &scenarioProbes{wrongAssertion: true}
		scenario := newTestConcreteScenario(t, probes)
		_, err := scenario.GatewayReady(context.Background(), testLiveBinding())
		if !errors.Is(err, ErrScenarioAssertion) {
			t.Fatalf("GatewayReady() error = %v", err)
		}
		if _, err := scenario.SandboxReady(context.Background(), testLiveBinding()); !errors.Is(err, ErrScenarioOrder) {
			t.Fatalf("SandboxReady() after rejection error = %v", err)
		}
	})

	t.Run("private error is sanitized", func(t *testing.T) {
		privateErr := errors.New("native endpoint and response")
		probes := &scenarioProbes{fail: privateErr}
		scenario := newTestConcreteScenario(t, probes)
		_, err := scenario.GatewayReady(context.Background(), testLiveBinding())
		if !errors.Is(err, ErrScenarioProbe) || errors.Is(err, privateErr) {
			t.Fatalf("GatewayReady() error = %v", err)
		}
	})

	t.Run("exposure is rejected", func(t *testing.T) {
		probes := &scenarioProbes{exposed: true}
		scenario := newTestConcreteScenario(t, probes)
		_, err := scenario.GatewayReady(context.Background(), testLiveBinding())
		if !errors.Is(err, ErrScenarioAssertion) {
			t.Fatalf("GatewayReady() error = %v", err)
		}
	})

	t.Run("replay is rejected", func(t *testing.T) {
		probes := &scenarioProbes{replay: true}
		scenario := newTestConcreteScenario(t, probes)
		if _, err := scenario.GatewayReady(context.Background(), testLiveBinding()); err != nil {
			t.Fatalf("GatewayReady() error = %v", err)
		}
		_, err := scenario.SandboxReady(context.Background(), testLiveBinding())
		if !errors.Is(err, ErrScenarioReplay) {
			t.Fatalf("SandboxReady() error = %v", err)
		}
	})

	t.Run("binding drift is rejected", func(t *testing.T) {
		scenario := newTestConcreteScenario(t, &scenarioProbes{})
		binding := testLiveBinding()
		binding.Resources.Runtime = "replacement"
		if err := scenario.ValidateBinding(binding); !errors.Is(err, ErrScenarioBinding) {
			t.Fatalf("ValidateBinding() error = %v", err)
		}
		if _, err := scenario.GatewayReady(context.Background(), testLiveBinding()); !errors.Is(err, ErrScenarioOrder) {
			t.Fatalf("GatewayReady() after binding drift error = %v", err)
		}
	})

	t.Run("configuration cannot be serialized", func(t *testing.T) {
		scenario := newTestConcreteScenario(t, &scenarioProbes{})
		if _, err := json.Marshal(scenario); !errors.Is(err, ErrSerialization) {
			t.Fatalf("json.Marshal() error = %v", err)
		}
	})
}

type scenarioProbes struct {
	base           time.Time
	calls          []CheckName
	fail           error
	wrongAssertion bool
	exposed        bool
	replay         bool
}

func (probes *scenarioProbes) GatewayReady(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	return probes.run(ctx, request, CheckGatewayReady)
}

func (probes *scenarioProbes) SandboxReady(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	return probes.run(ctx, request, CheckSandboxReady)
}

func (probes *scenarioProbes) Initialize(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	return probes.run(ctx, request, CheckInitialize)
}

func (probes *scenarioProbes) TurnSuccess(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	return probes.run(ctx, request, CheckTurnSuccess)
}

func (probes *scenarioProbes) TurnFailure(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	return probes.run(ctx, request, CheckTurnFailure)
}

func (probes *scenarioProbes) EventNormalization(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	return probes.run(ctx, request, CheckEventNormalization)
}

func (probes *scenarioProbes) Interrupt(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	return probes.run(ctx, request, CheckInterrupt)
}

func (probes *scenarioProbes) Cancellation(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	return probes.run(ctx, request, CheckCancellation)
}

func (probes *scenarioProbes) CommandApproval(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	return probes.run(ctx, request, CheckCommandApproval)
}

func (probes *scenarioProbes) FileChangeApproval(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	return probes.run(ctx, request, CheckFileChangeApproval)
}

func (probes *scenarioProbes) ArtifactExport(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	return probes.run(ctx, request, CheckArtifactExport)
}

func (probes *scenarioProbes) SandboxTeardown(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	return probes.run(ctx, request, CheckSandboxTeardown)
}

func (probes *scenarioProbes) run(
	ctx context.Context,
	request ProbeRequest,
	name CheckName,
) (ProbeResult, error) {
	if err := ctx.Err(); err != nil {
		return ProbeResult{}, err
	}
	if request.RunID != testRunID || request.Resources != namesForRun(testRunID) {
		return ProbeResult{}, ErrScenarioBinding
	}
	probes.calls = append(probes.calls, name)
	if probes.fail != nil {
		return ProbeResult{}, probes.fail
	}
	startedAt := probes.base.Add(time.Duration(len(probes.calls)) * time.Second)
	if probes.base.IsZero() {
		startedAt = time.Date(2026, time.July, 30, 13, 0, len(probes.calls), 0, time.UTC)
	}
	proof := []byte(fmt.Sprintf("%s:%d", name, len(probes.calls)))
	if probes.replay {
		proof = []byte("same")
	}
	assertions := assertionFor(name)
	if probes.wrongAssertion {
		assertions = assertionFor(CheckSandboxReady)
	}
	assertions.NativeProtocolExposed = probes.exposed
	return ProbeResult{
		StartedAt:         startedAt,
		FinishedAt:        startedAt.Add(time.Millisecond),
		ObservationSHA256: sha256.Sum256(proof),
		Assertions:        assertions,
	}, nil
}

func assertionFor(name CheckName) Assertions {
	assertions := Assertions{ExposureChecked: true}
	switch name {
	case CheckGatewayReady:
		assertions.GatewayReady = true
	case CheckSandboxReady:
		assertions.SandboxReady = true
	case CheckInitialize:
		assertions.Initialized = true
	case CheckTurnSuccess:
		assertions.TurnCompleted = true
	case CheckTurnFailure:
		assertions.DeterministicFailure = true
	case CheckEventNormalization:
		assertions.EventsNormalized = true
	case CheckInterrupt:
		assertions.Interrupted = true
	case CheckCancellation:
		assertions.Cancelled = true
	case CheckCommandApproval:
		assertions.CommandApprovalHandled = true
	case CheckFileChangeApproval:
		assertions.FileApprovalHandled = true
	case CheckArtifactExport:
		assertions.ArtifactExported = true
	case CheckSandboxTeardown:
		assertions.SandboxRemoved = true
	}
	return assertions
}

func newTestConcreteScenario(t *testing.T, probes *scenarioProbes) *ConcreteScenario {
	t.Helper()
	scenario, err := NewConcreteScenario(ScenarioConfig{
		RunID:    testRunID,
		Provider: probes,
		Runtime:  probes,
	})
	if err != nil {
		t.Fatalf("NewConcreteScenario() error = %v", err)
	}
	return scenario
}

func testLiveBinding() LiveBinding {
	return LiveBinding{RunID: testRunID, Resources: namesForRun(testRunID)}
}
