package runtimeevidence

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type liveCases struct {
	base       time.Time
	bound      bool
	call       int
	failAt     CheckName
	replayAt   CheckName
	exposeAt   CheckName
	bindingErr error
}

func (cases *liveCases) ValidateBinding(runID string, resources Resources) error {
	if cases.bindingErr != nil {
		return cases.bindingErr
	}
	cases.bound = runID == "0123456789abcdef0123456789abcdef" && resources == namesForRun(runID)
	if !cases.bound {
		return errors.New("binding drift")
	}
	return nil
}

func (cases *liveCases) receipt(name CheckName) (CaseReceipt, error) {
	if !cases.bound {
		return CaseReceipt{}, errors.New("not bound")
	}
	if cases.failAt == name {
		return CaseReceipt{}, errors.New("native failure details")
	}
	index := cases.call
	cases.call++
	payload := fmt.Sprintf("sha256:%064x", index+1)
	if cases.replayAt == name {
		payload = fmt.Sprintf("sha256:%064x", 1)
	}
	return CaseReceipt{
		StartedAt: cases.base.Add(time.Duration(index*2+1) * time.Second),
		FinishedAt: cases.base.Add(time.Duration(index*2+2) * time.Second),
		PayloadSHA256: payload,
		NativeProtocolExposed: cases.exposeAt == name,
	}, nil
}

func (cases *liveCases) GatewayReady(context.Context) (CaseReceipt, error) { return cases.receipt(CheckGatewayReady) }
func (cases *liveCases) SandboxReady(context.Context) (CaseReceipt, error) { return cases.receipt(CheckSandboxReady) }
func (cases *liveCases) Initialize(context.Context) (CaseReceipt, error) { return cases.receipt(CheckInitialize) }
func (cases *liveCases) TurnSuccess(context.Context) (CaseReceipt, error) { return cases.receipt(CheckTurnSuccess) }
func (cases *liveCases) TurnFailure(context.Context) (CaseReceipt, error) { return cases.receipt(CheckTurnFailure) }
func (cases *liveCases) EventNormalization(context.Context) (CaseReceipt, error) { return cases.receipt(CheckEventNormalization) }
func (cases *liveCases) Interrupt(context.Context) (CaseReceipt, error) { return cases.receipt(CheckInterrupt) }
func (cases *liveCases) Cancellation(context.Context) (CaseReceipt, error) { return cases.receipt(CheckCancellation) }
func (cases *liveCases) CommandApproval(context.Context) (CaseReceipt, error) { return cases.receipt(CheckCommandApproval) }
func (cases *liveCases) FileChangeApproval(context.Context) (CaseReceipt, error) { return cases.receipt(CheckFileChangeApproval) }
func (cases *liveCases) ArtifactExport(context.Context) (CaseReceipt, error) { return cases.receipt(CheckArtifactExport) }
func (cases *liveCases) SandboxTeardown(context.Context) (CaseReceipt, error) { return cases.receipt(CheckSandboxTeardown) }

func TestLiveCaseRunnerOwnsCanonicalCases(t *testing.T) {
	t.Parallel()
	runID := "0123456789abcdef0123456789abcdef"
	resources := namesForRun(runID)
	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	cases := &liveCases{base: base}
	runner, err := NewLiveCaseRunner(runID, resources, cases)
	if err != nil {
		t.Fatalf("NewLiveCaseRunner() error = %v", err)
	}
	if err := runner.ValidateBinding(runID, resources); err != nil {
		t.Fatalf("ValidateBinding() error = %v", err)
	}
	commitments := map[string]struct{}{}
	for _, name := range requiredChecks {
		observation, err := runner.Run(context.Background(), CheckRequest{RunID: runID, Name: name, Resources: resources})
		if err != nil {
			t.Fatalf("Run(%s) error = %v", name, err)
		}
		if !commitmentPattern.MatchString(observation.Commitment) {
			t.Fatalf("Run(%s) commitment = %q", name, observation.Commitment)
		}
		if _, duplicate := commitments[observation.Commitment]; duplicate {
			t.Fatalf("Run(%s) reused commitment", name)
		}
		commitments[observation.Commitment] = struct{}{}
	}
	if cases.call != len(requiredChecks) {
		t.Fatalf("case calls = %d", cases.call)
	}
}

func TestLiveCaseRunnerFailsClosed(t *testing.T) {
	t.Parallel()
	runID := "0123456789abcdef0123456789abcdef"
	resources := namesForRun(runID)
	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		cases *liveCases
		first CheckName
		want  error
	}{
		{name: "out of order", cases: &liveCases{base: base}, first: CheckSandboxReady, want: ErrCaseBinding},
		{name: "native failure sanitized", cases: &liveCases{base: base, failAt: CheckGatewayReady}, first: CheckGatewayReady, want: ErrCase},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner, err := NewLiveCaseRunner(runID, resources, test.cases)
			if err != nil {
				t.Fatalf("NewLiveCaseRunner() error = %v", err)
			}
			if err := runner.ValidateBinding(runID, resources); err != nil {
				t.Fatalf("ValidateBinding() error = %v", err)
			}
			_, err = runner.Run(context.Background(), CheckRequest{RunID: runID, Name: test.first, Resources: resources})
			if !errors.Is(err, test.want) {
				t.Fatalf("Run() error = %v, want %v", err, test.want)
			}
			if test.want == ErrCase && err.Error() != ErrCase.Error() {
				t.Fatalf("Run() leaked backend error = %q", err)
			}
		})
	}
}

func TestLiveCaseRunnerRejectsReceiptReplay(t *testing.T) {
	t.Parallel()
	runID := "0123456789abcdef0123456789abcdef"
	resources := namesForRun(runID)
	cases := &liveCases{base: time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC), replayAt: CheckSandboxReady}
	runner, err := NewLiveCaseRunner(runID, resources, cases)
	if err != nil {
		t.Fatalf("NewLiveCaseRunner() error = %v", err)
	}
	if err := runner.ValidateBinding(runID, resources); err != nil {
		t.Fatalf("ValidateBinding() error = %v", err)
	}
	if _, err := runner.Run(context.Background(), CheckRequest{RunID: runID, Name: CheckGatewayReady, Resources: resources}); err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	if _, err := runner.Run(context.Background(), CheckRequest{RunID: runID, Name: CheckSandboxReady, Resources: resources}); !errors.Is(err, ErrCaseReplay) {
		t.Fatalf("second Run() error = %v", err)
	}
}

func TestLiveCaseRunnerRejectsBindingReplay(t *testing.T) {
	t.Parallel()
	runID := "0123456789abcdef0123456789abcdef"
	resources := namesForRun(runID)
	runner, err := NewLiveCaseRunner(runID, resources, &liveCases{base: time.Now().UTC()})
	if err != nil {
		t.Fatalf("NewLiveCaseRunner() error = %v", err)
	}
	if err := runner.ValidateBinding(runID, resources); err != nil {
		t.Fatalf("ValidateBinding() error = %v", err)
	}
	if err := runner.ValidateBinding(runID, resources); !errors.Is(err, ErrCaseBinding) {
		t.Fatalf("second ValidateBinding() error = %v", err)
	}
}
