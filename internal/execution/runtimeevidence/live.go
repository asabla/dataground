package runtimeevidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

var (
	ErrCaseBinding = errors.New("runtime evidence case binding is invalid")
	ErrCaseReplay  = errors.New("runtime evidence case receipt was replayed")
)

type CaseReceipt struct {
	StartedAt               time.Time
	FinishedAt              time.Time
	PayloadSHA256           string
	NativeProtocolExposed   bool
	UpstreamEndpointExposed bool
}

type LiveCases interface {
	ValidateBinding(string, Resources) error
	GatewayReady(context.Context) (CaseReceipt, error)
	SandboxReady(context.Context) (CaseReceipt, error)
	Initialize(context.Context) (CaseReceipt, error)
	TurnSuccess(context.Context) (CaseReceipt, error)
	TurnFailure(context.Context) (CaseReceipt, error)
	EventNormalization(context.Context) (CaseReceipt, error)
	Interrupt(context.Context) (CaseReceipt, error)
	Cancellation(context.Context) (CaseReceipt, error)
	CommandApproval(context.Context) (CaseReceipt, error)
	FileChangeApproval(context.Context) (CaseReceipt, error)
	ArtifactExport(context.Context) (CaseReceipt, error)
	SandboxTeardown(context.Context) (CaseReceipt, error)
}

type LiveCaseRunner struct {
	mu        sync.Mutex
	runID     string
	resources Resources
	cases     LiveCases
	started   bool
	next      int
	receipts  map[string]struct{}
}

func NewLiveCaseRunner(runID string, resources Resources, cases LiveCases) (*LiveCaseRunner, error) {
	if !runIDPattern.MatchString(runID) || resources != namesForRun(runID) || cases == nil {
		return nil, ErrCaseBinding
	}
	if err := cases.ValidateBinding(runID, resources); err != nil {
		return nil, ErrCaseBinding
	}
	return &LiveCaseRunner{
		runID: runID, resources: resources, cases: cases,
		receipts: make(map[string]struct{}, len(requiredChecks)),
	}, nil
}

func (runner *LiveCaseRunner) ValidateBinding(runID string, resources Resources) error {
	if runner == nil {
		return ErrCaseBinding
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.started || runID != runner.runID || resources != runner.resources {
		return ErrCaseBinding
	}
	runner.started = true
	return nil
}

func (runner *LiveCaseRunner) Run(ctx context.Context, request CheckRequest) (Observation, error) {
	if runner == nil || ctx == nil {
		return Observation{}, ErrCaseBinding
	}
	runner.mu.Lock()
	if !runner.started || request.RunID != runner.runID || request.Resources != runner.resources ||
		runner.next >= len(requiredChecks) || request.Name != requiredChecks[runner.next] {
		runner.mu.Unlock()
		return Observation{}, ErrCaseBinding
	}
	runner.next++
	runner.mu.Unlock()

	receipt, err := runner.run(ctx, request.Name)
	if err != nil {
		return Observation{}, ErrCase
	}
	if ctx.Err() != nil {
		return Observation{}, ErrCase
	}
	if err := validateCaseReceipt(receipt); err != nil {
		return Observation{}, err
	}
	commitment, err := commitCaseReceipt(request, receipt)
	if err != nil {
		return Observation{}, ErrObservation
	}
	runner.mu.Lock()
	if _, exists := runner.receipts[receipt.PayloadSHA256]; exists {
		runner.mu.Unlock()
		return Observation{}, ErrCaseReplay
	}
	runner.receipts[receipt.PayloadSHA256] = struct{}{}
	runner.mu.Unlock()
	return Observation{
		StartedAt: receipt.StartedAt, FinishedAt: receipt.FinishedAt,
		Commitment: commitment,
		NativeProtocolExposed: receipt.NativeProtocolExposed,
		UpstreamEndpointExposed: receipt.UpstreamEndpointExposed,
	}, nil
}

func (runner *LiveCaseRunner) run(ctx context.Context, name CheckName) (CaseReceipt, error) {
	switch name {
	case CheckGatewayReady:
		return runner.cases.GatewayReady(ctx)
	case CheckSandboxReady:
		return runner.cases.SandboxReady(ctx)
	case CheckInitialize:
		return runner.cases.Initialize(ctx)
	case CheckTurnSuccess:
		return runner.cases.TurnSuccess(ctx)
	case CheckTurnFailure:
		return runner.cases.TurnFailure(ctx)
	case CheckEventNormalization:
		return runner.cases.EventNormalization(ctx)
	case CheckInterrupt:
		return runner.cases.Interrupt(ctx)
	case CheckCancellation:
		return runner.cases.Cancellation(ctx)
	case CheckCommandApproval:
		return runner.cases.CommandApproval(ctx)
	case CheckFileChangeApproval:
		return runner.cases.FileChangeApproval(ctx)
	case CheckArtifactExport:
		return runner.cases.ArtifactExport(ctx)
	case CheckSandboxTeardown:
		return runner.cases.SandboxTeardown(ctx)
	default:
		return CaseReceipt{}, ErrCaseBinding
	}
}

func validateCaseReceipt(receipt CaseReceipt) error {
	if receipt.StartedAt.IsZero() || receipt.FinishedAt.IsZero() ||
		receipt.FinishedAt.Before(receipt.StartedAt) ||
		!commitmentPattern.MatchString(receipt.PayloadSHA256) ||
		receipt.NativeProtocolExposed || receipt.UpstreamEndpointExposed {
		return ErrObservation
	}
	return nil
}

func commitCaseReceipt(request CheckRequest, receipt CaseReceipt) (string, error) {
	canonical, err := json.Marshal(struct {
		Domain        string    `json:"domain"`
		RunID         string    `json:"runID"`
		Name          CheckName `json:"name"`
		Resources     Resources `json:"resources"`
		StartedAt     string    `json:"startedAt"`
		FinishedAt    string    `json:"finishedAt"`
		PayloadSHA256 string    `json:"payloadSHA256"`
	}{
		Domain: "dataground.dev/openshell-runtime-case/v1",
		RunID: request.RunID, Name: request.Name, Resources: request.Resources,
		StartedAt: receipt.StartedAt.UTC().Format(time.RFC3339Nano),
		FinishedAt: receipt.FinishedAt.UTC().Format(time.RFC3339Nano),
		PayloadSHA256: receipt.PayloadSHA256,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

var _ CaseRunner = (*LiveCaseRunner)(nil)
