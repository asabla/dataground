package runtimeevidence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestLiveCasesOwnCanonicalDispatchAndObservations(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	scenario := &liveScenario{base: base}
	cases, err := NewLiveCases(testRunID, scenario)
	if err != nil {
		t.Fatalf("NewLiveCases() error = %v", err)
	}
	resources := namesForRun(testRunID)
	commitments := make(map[string]struct{}, len(requiredChecks))

	for _, name := range requiredChecks {
		observation, runErr := cases.Run(context.Background(), CheckRequest{
			RunID:     testRunID,
			Name:      name,
			Resources: resources,
		})
		if runErr != nil {
			t.Fatalf("Run(%q) error = %v", name, runErr)
		}
		if !commitmentPattern.MatchString(observation.Commitment) {
			t.Fatalf("Run(%q) commitment = %q", name, observation.Commitment)
		}
		if observation.NativeProtocolExposed || observation.UpstreamEndpointExposed {
			t.Fatalf("Run(%q) exposed a private surface", name)
		}
		if _, exists := commitments[observation.Commitment]; exists {
			t.Fatalf("Run(%q) reused a commitment", name)
		}
		commitments[observation.Commitment] = struct{}{}
	}
	if err := cases.FinalizeBinding(testRunID, resources); err != nil {
		t.Fatalf("FinalizeBinding() error = %v", err)
	}
	if _, err := cases.Run(context.Background(), testLiveRequest(CheckGatewayReady)); !errors.Is(err, ErrLiveCaseOrder) {
		t.Fatalf("Run() after finalization error = %v", err)
	}
	if !reflect.DeepEqual(scenario.calls, requiredChecks[:]) {
		t.Fatalf("scenario calls = %v, want %v", scenario.calls, requiredChecks)
	}
}

func TestEvidenceRunAcceptsCompleteLiveCases(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	cases, err := NewLiveCases(testRunID, &liveScenario{base: base})
	if err != nil {
		t.Fatalf("NewLiveCases() error = %v", err)
	}
	remove := func(context.Context, CleanupRequest) error { return nil }
	run, err := newEvidenceRun(Config{
		RunID: testRunID,
		Provenance: Provenance{
			SourceCommit:  "0123456789abcdef0123456789abcdef01234567",
			WorkflowRunID: 123,
		},
		Cases: cases,
		Cleanup: Cleanup{
			Sandbox:         remove,
			ProviderBinding: remove,
			Workspace:       remove,
		},
	}, clock(base.Add(-time.Second), base.Add(time.Minute)))
	if err != nil {
		t.Fatalf("newEvidenceRun() error = %v", err)
	}
	result, err := run.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatalf("marshal result: %v", err)
	}
}

func TestLiveCasesFailClosed(t *testing.T) {
	t.Parallel()

	t.Run("binding drift", func(t *testing.T) {
		cases := newTestLiveCases(t, &liveScenario{})
		if err := cases.ValidateBinding(testRunID, Resources{}); !errors.Is(err, ErrLiveCaseBinding) {
			t.Fatalf("ValidateBinding() error = %v", err)
		}
	})

	t.Run("out of order is terminal", func(t *testing.T) {
		cases := newTestLiveCases(t, &liveScenario{})
		request := testLiveRequest(CheckSandboxReady)
		if _, err := cases.Run(context.Background(), request); !errors.Is(err, ErrLiveCaseOrder) {
			t.Fatalf("Run() error = %v", err)
		}
		request.Name = CheckGatewayReady
		if _, err := cases.Run(context.Background(), request); !errors.Is(err, ErrLiveCaseOrder) {
			t.Fatalf("Run() after rejection error = %v", err)
		}
	})

	t.Run("backend failure is sanitized", func(t *testing.T) {
		privateErr := errors.New("private native failure")
		cases := newTestLiveCases(t, &liveScenario{fail: privateErr})
		_, err := cases.Run(context.Background(), testLiveRequest(CheckGatewayReady))
		if !errors.Is(err, ErrLiveCaseBackend) || errors.Is(err, privateErr) {
			t.Fatalf("Run() error = %v", err)
		}
	})

	t.Run("cancellation is retained", func(t *testing.T) {
		cases := newTestLiveCases(t, &liveScenario{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := cases.Run(ctx, testLiveRequest(CheckGatewayReady))
		if !errors.Is(err, ErrLiveCaseBackend) || !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v", err)
		}
	})

	t.Run("invalid receipt is terminal", func(t *testing.T) {
		cases := newTestLiveCases(t, &liveScenario{invalid: true})
		if _, err := cases.Run(context.Background(), testLiveRequest(CheckGatewayReady)); !errors.Is(err, ErrLiveCaseReceipt) {
			t.Fatalf("Run() error = %v", err)
		}
		if _, err := cases.Run(context.Background(), testLiveRequest(CheckSandboxReady)); !errors.Is(err, ErrLiveCaseOrder) {
			t.Fatalf("Run() after receipt rejection error = %v", err)
		}
	})

	t.Run("uninspected exposure is rejected", func(t *testing.T) {
		cases := newTestLiveCases(t, &liveScenario{uninspected: true})
		if _, err := cases.Run(context.Background(), testLiveRequest(CheckGatewayReady)); !errors.Is(err, ErrLiveCaseReceipt) {
			t.Fatalf("Run() error = %v", err)
		}
	})

	t.Run("observed exposure is rejected", func(t *testing.T) {
		cases := newTestLiveCases(t, &liveScenario{exposed: true})
		if _, err := cases.Run(context.Background(), testLiveRequest(CheckGatewayReady)); !errors.Is(err, ErrLiveCaseReceipt) {
			t.Fatalf("Run() error = %v", err)
		}
	})

	t.Run("proof replay is terminal", func(t *testing.T) {
		cases := newTestLiveCases(t, &liveScenario{replay: true})
		if _, err := cases.Run(context.Background(), testLiveRequest(CheckGatewayReady)); err != nil {
			t.Fatalf("first Run() error = %v", err)
		}
		if _, err := cases.Run(context.Background(), testLiveRequest(CheckSandboxReady)); !errors.Is(err, ErrLiveCaseReplay) {
			t.Fatalf("second Run() error = %v", err)
		}
	})

	t.Run("overlapping calls poison the run", func(t *testing.T) {
		scenario := &blockingLiveScenario{
			liveScenario: &liveScenario{},
			entered:      make(chan struct{}),
			release:      make(chan struct{}),
		}
		cases := newTestLiveCases(t, scenario)
		firstResult := make(chan error, 1)
		go func() {
			_, err := cases.Run(context.Background(), testLiveRequest(CheckGatewayReady))
			firstResult <- err
		}()
		<-scenario.entered
		if _, err := cases.Run(context.Background(), testLiveRequest(CheckSandboxReady)); !errors.Is(err, ErrLiveCaseOrder) {
			t.Fatalf("overlapping Run() error = %v", err)
		}
		close(scenario.release)
		if err := <-firstResult; !errors.Is(err, ErrLiveCaseOrder) {
			t.Fatalf("first Run() after overlap error = %v", err)
		}
	})

	t.Run("interference before finalization is retained", func(t *testing.T) {
		cases := newTestLiveCases(t, &liveScenario{})
		for _, name := range requiredChecks {
			if _, err := cases.Run(context.Background(), testLiveRequest(name)); err != nil {
				t.Fatalf("Run(%q) error = %v", name, err)
			}
		}
		if _, err := cases.Run(context.Background(), testLiveRequest(CheckGatewayReady)); !errors.Is(err, ErrLiveCaseOrder) {
			t.Fatalf("extra Run() error = %v", err)
		}
		if err := cases.FinalizeBinding(testRunID, namesForRun(testRunID)); !errors.Is(err, ErrLiveCaseBinding) {
			t.Fatalf("FinalizeBinding() error = %v", err)
		}
	})

	t.Run("configuration cannot be serialized", func(t *testing.T) {
		cases := newTestLiveCases(t, &liveScenario{})
		if _, err := json.Marshal(cases); !errors.Is(err, ErrSerialization) {
			t.Fatalf("json.Marshal() error = %v", err)
		}
	})
}

const testRunID = "0123456789abcdef0123456789abcdef"

type liveScenario struct {
	base    time.Time
	calls   []CheckName
	fail    error
	invalid bool
	replay  bool
	uninspected bool
	exposed     bool
}

type blockingLiveScenario struct {
	*liveScenario
	entered chan struct{}
	release chan struct{}
}

func (scenario *blockingLiveScenario) GatewayReady(
	ctx context.Context,
	binding LiveBinding,
) (LiveReceipt, error) {
	close(scenario.entered)
	select {
	case <-ctx.Done():
		return LiveReceipt{}, ctx.Err()
	case <-scenario.release:
	}
	return scenario.liveScenario.GatewayReady(ctx, binding)
}

func (scenario *liveScenario) ValidateBinding(binding LiveBinding) error {
	if binding.RunID != testRunID || binding.Resources != namesForRun(testRunID) {
		return ErrLiveCaseBinding
	}
	return nil
}

func (scenario *liveScenario) GatewayReady(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckGatewayReady)
}

func (scenario *liveScenario) SandboxReady(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckSandboxReady)
}

func (scenario *liveScenario) Initialize(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckInitialize)
}

func (scenario *liveScenario) TurnSuccess(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckTurnSuccess)
}

func (scenario *liveScenario) TurnFailure(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckTurnFailure)
}

func (scenario *liveScenario) EventNormalization(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckEventNormalization)
}

func (scenario *liveScenario) Interrupt(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckInterrupt)
}

func (scenario *liveScenario) Cancellation(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckCancellation)
}

func (scenario *liveScenario) CommandApproval(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckCommandApproval)
}

func (scenario *liveScenario) FileChangeApproval(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckFileChangeApproval)
}

func (scenario *liveScenario) ArtifactExport(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckArtifactExport)
}

func (scenario *liveScenario) SandboxTeardown(ctx context.Context, binding LiveBinding) (LiveReceipt, error) {
	return scenario.run(ctx, binding, CheckSandboxTeardown)
}

func (scenario *liveScenario) run(
	ctx context.Context,
	binding LiveBinding,
	name CheckName,
) (LiveReceipt, error) {
	if err := ctx.Err(); err != nil {
		return LiveReceipt{}, err
	}
	if err := scenario.ValidateBinding(binding); err != nil {
		return LiveReceipt{}, err
	}
	scenario.calls = append(scenario.calls, name)
	if scenario.fail != nil {
		return LiveReceipt{}, scenario.fail
	}
	index := len(scenario.calls)
	startedAt := scenario.base.Add(time.Duration(index) * time.Second)
	finishedAt := startedAt.Add(time.Millisecond)
	proofInput := []byte(fmt.Sprintf("%s:%d", name, index))
	if scenario.replay {
		proofInput = []byte("same-proof")
	}
	receipt := LiveReceipt{
		StartedAt:               startedAt,
		FinishedAt:              finishedAt,
		ObservationSHA256:       sha256.Sum256(proofInput),
		ExposureChecked:         !scenario.uninspected,
		NativeProtocolExposed:   scenario.exposed,
		UpstreamEndpointExposed: false,
	}
	if scenario.invalid {
		receipt.FinishedAt = receipt.StartedAt
	}
	return receipt, nil
}

func newTestLiveCases(t *testing.T, scenario LiveScenario) *LiveCases {
	t.Helper()
	cases, err := NewLiveCases(testRunID, scenario)
	if err != nil {
		t.Fatalf("NewLiveCases() error = %v", err)
	}
	return cases
}

func testLiveRequest(name CheckName) CheckRequest {
	return CheckRequest{
		RunID:     testRunID,
		Name:      name,
		Resources: namesForRun(testRunID),
	}
}
