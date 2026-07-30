package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"
)

const (
	statusPassed   = "passed"
	statusRemoved  = "removed"
	maxSafeInteger = int64(1<<53 - 1)
)

var (
	ErrInvalidConfiguration = errors.New("invalid runtime evidence configuration")
	ErrAlreadyStarted       = errors.New("runtime evidence run already started")
	ErrCase                 = errors.New("runtime evidence case failed")
	ErrObservation          = errors.New("runtime evidence observation is invalid")
	ErrCleanup              = errors.New("runtime evidence cleanup failed")
	ErrClock                = errors.New("runtime evidence clock moved backwards")
	ErrRunIncomplete        = errors.New("runtime evidence run is incomplete")
	ErrSerialization        = errors.New("runtime evidence run configuration cannot be serialized")

	runIDPattern      = regexp.MustCompile(`^[a-f0-9]{32}$`)
	commitPattern     = regexp.MustCompile(`^[a-f0-9]{40}$`)
	commitmentPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

type CheckName string

const (
	CheckGatewayReady       CheckName = "gateway-ready"
	CheckSandboxReady       CheckName = "sandbox-ready"
	CheckInitialize         CheckName = "initialize"
	CheckTurnSuccess        CheckName = "turn-success"
	CheckTurnFailure        CheckName = "turn-failure"
	CheckEventNormalization CheckName = "event-normalization"
	CheckInterrupt          CheckName = "interrupt"
	CheckCancellation       CheckName = "cancellation"
	CheckCommandApproval    CheckName = "command-approval"
	CheckFileChangeApproval CheckName = "file-change-approval"
	CheckArtifactExport     CheckName = "artifact-export"
	CheckSandboxTeardown    CheckName = "sandbox-teardown"
)

var requiredChecks = [...]CheckName{
	CheckGatewayReady,
	CheckSandboxReady,
	CheckInitialize,
	CheckTurnSuccess,
	CheckTurnFailure,
	CheckEventNormalization,
	CheckInterrupt,
	CheckCancellation,
	CheckCommandApproval,
	CheckFileChangeApproval,
	CheckArtifactExport,
	CheckSandboxTeardown,
}

type Resources struct {
	Gateway   string `json:"gateway"`
	Sandbox   string `json:"sandbox"`
	Provider  string `json:"provider"`
	Runtime   string `json:"runtime"`
	Workspace string `json:"workspace"`
}

type Provenance struct {
	SourceCommit  string
	WorkflowRunID int64
}

type CheckRequest struct {
	RunID     string
	Name      CheckName
	Resources Resources
}

type Observation struct {
	StartedAt               time.Time
	FinishedAt              time.Time
	Commitment              string
	NativeProtocolExposed   bool
	UpstreamEndpointExposed bool
}

type CaseRunner interface {
	ValidateBinding(runID string, resources Resources) error
	Run(context.Context, CheckRequest) (Observation, error)
}

type CleanupRequest struct {
	RunID        string
	ResourceKind string
	ResourceName string
}

type CleanupFunc func(context.Context, CleanupRequest) error

type Cleanup struct {
	Sandbox         CleanupFunc
	ProviderBinding CleanupFunc
	Workspace       CleanupFunc
}

type Config struct {
	RunID      string
	Provenance Provenance
	Cases      CaseRunner
	Cleanup    Cleanup
}

type EvidenceRun struct {
	state *runState
}

type runState struct {
	mu      sync.Mutex
	started bool
	config  Config
	now     func() time.Time
}

type Result struct {
	record   record
	complete bool
}

type record struct {
	SchemaVersion string       `json:"schemaVersion"`
	Profile       profile      `json:"profile"`
	Run           runRecord    `json:"run"`
	Checks        []check      `json:"checks"`
	Capabilities  []capability `json:"capabilities"`
	Cleanup       cleanup      `json:"cleanup"`
	Result        string       `json:"result"`
}

type runRecord struct {
	ID         string           `json:"id"`
	Resources  Resources        `json:"resources"`
	StartedAt  string           `json:"startedAt"`
	FinishedAt string           `json:"finishedAt"`
	Verifier   verifier         `json:"verifier"`
	Provenance provenanceRecord `json:"provenance"`
}

type verifier struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type provenanceRecord struct {
	SourceCommit  string `json:"sourceCommit"`
	Workflow      string `json:"workflow"`
	WorkflowRunID int64  `json:"workflowRunID"`
	ArtifactName  string `json:"artifactName"`
}

type check struct {
	Name                    CheckName `json:"name"`
	Status                  string    `json:"status"`
	StartedAt               string    `json:"startedAt"`
	FinishedAt              string    `json:"finishedAt"`
	ObservationCommitment   string    `json:"observationCommitment"`
	NativeProtocolExposed   bool      `json:"nativeProtocolExposed"`
	UpstreamEndpointExposed bool      `json:"upstreamEndpointExposed"`
}

type capability struct {
	Name           string      `json:"name"`
	Classification string      `json:"classification"`
	Evidence       []CheckName `json:"evidence"`
	ReasonCode     *string     `json:"reasonCode"`
}

type cleanup struct {
	Sandbox         cleanupReceipt `json:"sandbox"`
	ProviderBinding cleanupReceipt `json:"providerBinding"`
	Workspace       cleanupReceipt `json:"workspace"`
}

type cleanupReceipt struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func New(config Config) (*EvidenceRun, error) {
	return newEvidenceRun(config, time.Now)
}

func newEvidenceRun(config Config, now func() time.Time) (*EvidenceRun, error) {
	if now == nil || !validConfig(config) {
		return nil, ErrInvalidConfiguration
	}
	resources := namesForRun(config.RunID)
	if err := config.Cases.ValidateBinding(config.RunID, resources); err != nil {
		return nil, ErrInvalidConfiguration
	}
	return &EvidenceRun{
		state: &runState{
			config: config,
			now:    now,
		},
	}, nil
}

func (run *EvidenceRun) Execute(ctx context.Context) (Result, error) {
	if run == nil || run.state == nil || ctx == nil {
		return Result{}, ErrInvalidConfiguration
	}
	state := run.state
	state.mu.Lock()
	if state.started {
		state.mu.Unlock()
		return Result{}, ErrAlreadyStarted
	}
	state.started = true
	state.mu.Unlock()

	resources := namesForRun(state.config.RunID)
	startedAt := state.now().UTC()
	checks, checkErr := runCases(ctx, state.config, resources)
	cleanupRecord, cleanupErr := cleanupResources(context.WithoutCancel(ctx), state.config, resources)
	finishedAt := state.now().UTC()

	var outcome error
	if checkErr != nil {
		outcome = errors.Join(outcome, checkErr)
	}
	if err := ctx.Err(); err != nil {
		outcome = errors.Join(outcome, err)
	}
	if cleanupErr != nil {
		outcome = errors.Join(outcome, cleanupErr)
	}
	if finishedAt.Before(startedAt) {
		outcome = errors.Join(outcome, ErrClock)
	}
	if err := validateObservations(checks, startedAt, finishedAt); err != nil {
		outcome = errors.Join(outcome, err)
	}
	if outcome != nil {
		return Result{}, errors.Join(ErrRunIncomplete, outcome)
	}

	return Result{
		record: record{
			SchemaVersion: SchemaVersion,
			Profile:       currentProfile(),
			Run: runRecord{
				ID:         state.config.RunID,
				Resources:  resources,
				StartedAt:  startedAt.Format(time.RFC3339Nano),
				FinishedAt: finishedAt.Format(time.RFC3339Nano),
				Verifier:   verifier{Name: VerifierName, Version: VerifierVersion},
				Provenance: provenanceRecord{
					SourceCommit:  state.config.Provenance.SourceCommit,
					Workflow:      Workflow,
					WorkflowRunID: state.config.Provenance.WorkflowRunID,
					ArtifactName:  ArtifactName,
				},
			},
			Checks:       checks,
			Capabilities: capabilities(),
			Cleanup:      cleanupRecord,
			Result:       statusPassed,
		},
		complete: true,
	}, nil
}

func (EvidenceRun) MarshalJSON() ([]byte, error) {
	return nil, ErrSerialization
}

func (result Result) MarshalJSON() ([]byte, error) {
	if !result.complete {
		return nil, ErrRunIncomplete
	}
	return json.Marshal(result.record)
}

func runCases(ctx context.Context, config Config, resources Resources) ([]check, error) {
	checks := make([]check, 0, len(requiredChecks))
	for _, name := range requiredChecks {
		observation, err := config.Cases.Run(ctx, CheckRequest{
			RunID:     config.RunID,
			Name:      name,
			Resources: resources,
		})
		if err != nil {
			return checks, ErrCase
		}
		checks = append(checks, check{
			Name:                    name,
			Status:                  statusPassed,
			StartedAt:               observation.StartedAt.UTC().Format(time.RFC3339Nano),
			FinishedAt:              observation.FinishedAt.UTC().Format(time.RFC3339Nano),
			ObservationCommitment:   observation.Commitment,
			NativeProtocolExposed:   observation.NativeProtocolExposed,
			UpstreamEndpointExposed: observation.UpstreamEndpointExposed,
		})
	}
	return checks, nil
}

func validateObservations(checks []check, startedAt, finishedAt time.Time) error {
	if len(checks) != len(requiredChecks) {
		return ErrObservation
	}
	commitments := make(map[string]struct{}, len(checks))
	previousFinishedAt := startedAt
	for index, observation := range checks {
		checkStartedAt, startedErr := time.Parse(time.RFC3339Nano, observation.StartedAt)
		checkFinishedAt, finishedErr := time.Parse(time.RFC3339Nano, observation.FinishedAt)
		if startedErr != nil ||
			finishedErr != nil ||
			observation.Name != requiredChecks[index] ||
			observation.NativeProtocolExposed ||
			observation.UpstreamEndpointExposed ||
			!commitmentPattern.MatchString(observation.ObservationCommitment) ||
			checkStartedAt.Before(startedAt) ||
			checkStartedAt.Before(previousFinishedAt) ||
			checkFinishedAt.Before(checkStartedAt) ||
			checkFinishedAt.After(finishedAt) {
			return ErrObservation
		}
		if _, exists := commitments[observation.ObservationCommitment]; exists {
			return ErrObservation
		}
		commitments[observation.ObservationCommitment] = struct{}{}
		previousFinishedAt = checkFinishedAt
	}
	return nil
}

func cleanupResources(ctx context.Context, config Config, resources Resources) (cleanup, error) {
	record := cleanup{
		Sandbox:         cleanupReceipt{Name: resources.Sandbox},
		ProviderBinding: cleanupReceipt{Name: resources.Provider},
		Workspace:       cleanupReceipt{Name: resources.Workspace},
	}
	steps := []struct {
		kind   string
		name   string
		remove CleanupFunc
		status *string
	}{
		{kind: "sandbox", name: resources.Sandbox, remove: config.Cleanup.Sandbox, status: &record.Sandbox.Status},
		{kind: "provider", name: resources.Provider, remove: config.Cleanup.ProviderBinding, status: &record.ProviderBinding.Status},
		{kind: "workspace", name: resources.Workspace, remove: config.Cleanup.Workspace, status: &record.Workspace.Status},
	}
	var outcome error
	for _, step := range steps {
		if err := step.remove(ctx, CleanupRequest{
			RunID:        config.RunID,
			ResourceKind: step.kind,
			ResourceName: step.name,
		}); err != nil {
			outcome = errors.Join(outcome, ErrCleanup)
			continue
		}
		*step.status = statusRemoved
	}
	return record, outcome
}

func validConfig(config Config) bool {
	return runIDPattern.MatchString(config.RunID) &&
		commitPattern.MatchString(config.Provenance.SourceCommit) &&
		config.Provenance.WorkflowRunID > 0 &&
		config.Provenance.WorkflowRunID <= maxSafeInteger &&
		config.Cases != nil &&
		config.Cleanup.Sandbox != nil &&
		config.Cleanup.ProviderBinding != nil &&
		config.Cleanup.Workspace != nil
}

func namesForRun(runID string) Resources {
	return Resources{
		Gateway:   "dg-runtime-gateway-" + runID,
		Sandbox:   "dg-runtime-sandbox-" + runID,
		Provider:  "dg-runtime-provider-" + runID,
		Runtime:   "dg-runtime-session-" + runID,
		Workspace: "dg-runtime-" + runID,
	}
}

func capabilities() []capability {
	unsupported := func(reason string) *string { return &reason }
	return []capability{
		{Name: "text", Classification: "supported", Evidence: []CheckName{CheckTurnSuccess, CheckEventNormalization}},
		{Name: "item-activity", Classification: "supported", Evidence: []CheckName{CheckEventNormalization}},
		{Name: "interrupt", Classification: "supported", Evidence: []CheckName{CheckInterrupt}},
		{Name: "cancellation", Classification: "supported", Evidence: []CheckName{CheckCancellation}},
		{Name: "failure", Classification: "supported", Evidence: []CheckName{CheckTurnFailure}},
		{Name: "command-approval", Classification: "supported", Evidence: []CheckName{CheckCommandApproval}},
		{Name: "file-change-approval", Classification: "supported", Evidence: []CheckName{CheckFileChangeApproval}},
		{Name: "question", Classification: "unsupported", Evidence: []CheckName{}, ReasonCode: unsupported("ADAPTER_UNSUPPORTED")},
		{Name: "permission-escalation", Classification: "unsupported", Evidence: []CheckName{}, ReasonCode: unsupported("ADAPTER_UNSUPPORTED")},
		{Name: "rich-item-delta", Classification: "unsupported", Evidence: []CheckName{}, ReasonCode: unsupported("ADAPTER_UNSUPPORTED")},
		{Name: "usage", Classification: "unsupported", Evidence: []CheckName{}, ReasonCode: unsupported("ADAPTER_UNSUPPORTED")},
		{Name: "resume", Classification: "unsupported", Evidence: []CheckName{}, ReasonCode: unsupported("DURABLE_INTERACTION_UNIMPLEMENTED")},
		{Name: "steer", Classification: "unsupported", Evidence: []CheckName{}, ReasonCode: unsupported("DURABLE_INTERACTION_UNIMPLEMENTED")},
		{Name: "artifact-export", Classification: "supported", Evidence: []CheckName{CheckArtifactExport}},
		{Name: "runtime-artifact-events", Classification: "unsupported", Evidence: []CheckName{}, ReasonCode: unsupported("NATIVE_PROTOCOL_UNCERTIFIED")},
	}
}
