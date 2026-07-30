package runtimeevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestExecuteOwnsCompleteEvidence(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	runner := &caseRunner{base: base}
	var cleanupRequests []CleanupRequest
	config := validConfig(runner, func(_ context.Context, request CleanupRequest) error {
		cleanupRequests = append(cleanupRequests, request)
		return nil
	})
	run, err := newEvidenceRun(config, clock(base, base.Add(time.Minute)))
	if err != nil {
		t.Fatalf("newEvidenceRun() error = %v", err)
	}

	result, err := run.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var evidence map[string]any
	if err := json.Unmarshal(encoded, &evidence); err != nil {
		t.Fatalf("decode result: %v", err)
	}

	if evidence["schemaVersion"] != SchemaVersion || evidence["result"] != statusPassed {
		t.Fatalf("unexpected result = %v", evidence)
	}
	runRecord := evidence["run"].(map[string]any)
	if runRecord["id"] != config.RunID {
		t.Fatalf("run = %v", runRecord)
	}
	if got := runRecord["verifier"].(map[string]any); got["name"] != VerifierName || got["version"] != VerifierVersion {
		t.Fatalf("verifier = %v", got)
	}
	if got := runRecord["provenance"].(map[string]any); got["sourceCommit"] != config.Provenance.SourceCommit ||
		got["workflow"] != Workflow ||
		got["workflowRunID"] != float64(config.Provenance.WorkflowRunID) ||
		got["artifactName"] != ArtifactName {
		t.Fatalf("provenance = %v", got)
	}
	resources := namesForRun(config.RunID)
	if got := runRecord["resources"].(map[string]any); got["gateway"] != resources.Gateway ||
		got["sandbox"] != resources.Sandbox ||
		got["provider"] != resources.Provider ||
		got["runtime"] != resources.Runtime ||
		got["workspace"] != resources.Workspace {
		t.Fatalf("resources = %v", got)
	}
	if got := evidence["profile"].(map[string]any); got["runtimeSchemaCanonicalSHA256"] != runtimeSchemaCanonicalSHA256 ||
		got["credentialEvidenceSHA256"] != credentialEvidenceSHA256 {
		t.Fatalf("profile = %v", got)
	}
	if len(runner.requests) != len(requiredChecks) {
		t.Fatalf("case requests = %v", runner.requests)
	}
	for index, request := range runner.requests {
		if request.Name != requiredChecks[index] || request.RunID != config.RunID || request.Resources != resources {
			t.Fatalf("case request %d = %v", index, request)
		}
	}
	if len(cleanupRequests) != 3 ||
		cleanupRequests[0].ResourceKind != "sandbox" ||
		cleanupRequests[1].ResourceKind != "provider" ||
		cleanupRequests[2].ResourceKind != "workspace" {
		t.Fatalf("cleanup requests = %v", cleanupRequests)
	}
	checks := evidence["checks"].([]any)
	if len(checks) != len(requiredChecks) {
		t.Fatalf("checks = %v", checks)
	}
	capabilities := evidence["capabilities"].([]any)
	if len(capabilities) != 15 {
		t.Fatalf("capabilities = %v", capabilities)
	}
}

func TestExecuteIsSingleUseAndRunCannotBeSerialized(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	run, err := newEvidenceRun(
		validConfig(&caseRunner{base: base}, successfulCleanup),
		clock(base, base.Add(time.Minute)),
	)
	if err != nil {
		t.Fatalf("newEvidenceRun() error = %v", err)
	}
	if _, err := json.Marshal(run); !errors.Is(err, ErrSerialization) {
		t.Fatalf("marshal run error = %v", err)
	}
	if _, err := run.Execute(context.Background()); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if _, err := run.Execute(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("second Execute() error = %v", err)
	}
}

func TestNewRejectsInvalidPlanBeforeCaseExecutionOrCleanup(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(*Config){
		"run ID": func(config *Config) { config.RunID = "invalid" },
		"source commit": func(config *Config) { config.Provenance.SourceCommit = "invalid" },
		"workflow run": func(config *Config) { config.Provenance.WorkflowRunID = 0 },
		"case runner": func(config *Config) { config.Cases = nil },
		"cleanup": func(config *Config) { config.Cleanup.Workspace = nil },
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			runner := &caseRunner{}
			cleanups := 0
			config := validConfig(runner, func(context.Context, CleanupRequest) error {
				cleanups++
				return nil
			})
			mutate(&config)
			if _, err := New(config); !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("New() error = %v", err)
			}
			if len(runner.requests) != 0 || cleanups != 0 {
				t.Fatalf("side effects before validation: requests=%d cleanups=%d", len(runner.requests), cleanups)
			}
		})
	}
}

func TestNewRejectsCaseBindingDrift(t *testing.T) {
	t.Parallel()

	runner := &caseRunner{bindingError: errors.New("sensitive binding detail")}
	_, err := New(validConfig(runner, successfulCleanup))
	if !errors.Is(err, ErrInvalidConfiguration) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestExecuteCleansEveryResourceAfterCaseFailureAndSanitizesError(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	runner := &caseRunner{
		base:       base,
		failAt:     CheckTurnFailure,
		failure:    errors.New("sensitive runtime output"),
	}
	var cleanupKinds []string
	run, err := newEvidenceRun(
		validConfig(runner, func(_ context.Context, request CleanupRequest) error {
			cleanupKinds = append(cleanupKinds, request.ResourceKind)
			return nil
		}),
		clock(base, base.Add(time.Minute)),
	)
	if err != nil {
		t.Fatalf("newEvidenceRun() error = %v", err)
	}

	result, err := run.Execute(context.Background())
	if !errors.Is(err, ErrRunIncomplete) || !errors.Is(err, ErrCase) {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Execute() leaked backend error: %v", err)
	}
	if strings.Join(cleanupKinds, ",") != "sandbox,provider,workspace" {
		t.Fatalf("cleanup order = %v", cleanupKinds)
	}
	if _, marshalErr := json.Marshal(result); !errors.Is(marshalErr, ErrRunIncomplete) {
		t.Fatalf("marshal incomplete result error = %v", marshalErr)
	}
}

func TestExecuteUsesNonCanceledCleanupContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	runner := &caseRunner{base: base, failure: context.Canceled, failAt: CheckGatewayReady}
	cleanups := 0
	run, err := newEvidenceRun(
		validConfig(runner, func(cleanupContext context.Context, _ CleanupRequest) error {
			cleanups++
			if cleanupContext.Err() != nil {
				t.Fatalf("cleanup context error = %v", cleanupContext.Err())
			}
			return nil
		}),
		clock(base, base.Add(time.Minute)),
	)
	if err != nil {
		t.Fatalf("newEvidenceRun() error = %v", err)
	}

	_, err = run.Execute(ctx)
	if !errors.Is(err, ErrRunIncomplete) || !errors.Is(err, ErrCase) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v", err)
	}
	if cleanups != 3 {
		t.Fatalf("cleanup calls = %d", cleanups)
	}
}

func TestExecuteRejectsUncertainCleanup(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	var cleanupKinds []string
	run, err := newEvidenceRun(
		validConfig(&caseRunner{base: base}, func(_ context.Context, request CleanupRequest) error {
			cleanupKinds = append(cleanupKinds, request.ResourceKind)
			if request.ResourceKind == "provider" {
				return errors.New("sensitive cleanup detail")
			}
			return nil
		}),
		clock(base, base.Add(time.Minute)),
	)
	if err != nil {
		t.Fatalf("newEvidenceRun() error = %v", err)
	}

	_, err = run.Execute(context.Background())
	if !errors.Is(err, ErrRunIncomplete) || !errors.Is(err, ErrCleanup) {
		t.Fatalf("Execute() error = %v", err)
	}
	if strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("Execute() leaked cleanup error: %v", err)
	}
	if strings.Join(cleanupKinds, ",") != "sandbox,provider,workspace" {
		t.Fatalf("cleanup order = %v", cleanupKinds)
	}
}

func TestExecuteRejectsInvalidObservations(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*Observation){
		"commitment": func(observation *Observation) {
			observation.Commitment = "invalid"
		},
		"native protocol exposure": func(observation *Observation) {
			observation.NativeProtocolExposed = true
		},
		"endpoint exposure": func(observation *Observation) {
			observation.UpstreamEndpointExposed = true
		},
		"reversed interval": func(observation *Observation) {
			observation.FinishedAt = observation.StartedAt.Add(-time.Nanosecond)
		},
		"overlapping interval": func(observation *Observation) {
			observation.StartedAt = base
		},
	} {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			run, err := newEvidenceRun(
				validConfig(&caseRunner{base: base, mutateAt: CheckTurnSuccess, mutate: mutate}, successfulCleanup),
				clock(base, base.Add(time.Minute)),
			)
			if err != nil {
				t.Fatalf("newEvidenceRun() error = %v", err)
			}
			if _, err := run.Execute(context.Background()); !errors.Is(err, ErrObservation) ||
				!errors.Is(err, ErrRunIncomplete) {
				t.Fatalf("Execute() error = %v", err)
			}
		})
	}
}

func TestExecuteRejectsDuplicateCommitments(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	runner := &caseRunner{base: base, duplicateCommitment: true}
	run, err := newEvidenceRun(
		validConfig(runner, successfulCleanup),
		clock(base, base.Add(time.Minute)),
	)
	if err != nil {
		t.Fatalf("newEvidenceRun() error = %v", err)
	}
	if _, err := run.Execute(context.Background()); !errors.Is(err, ErrObservation) {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestExecuteRejectsClockRegression(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)
	run, err := newEvidenceRun(
		validConfig(&caseRunner{base: base}, successfulCleanup),
		clock(base.Add(time.Minute), base),
	)
	if err != nil {
		t.Fatalf("newEvidenceRun() error = %v", err)
	}
	if _, err := run.Execute(context.Background()); !errors.Is(err, ErrClock) ||
		!errors.Is(err, ErrRunIncomplete) {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestCapabilitiesAreClosedAndDeterministic(t *testing.T) {
	t.Parallel()

	first, err := json.Marshal(capabilities())
	if err != nil {
		t.Fatalf("marshal capabilities: %v", err)
	}
	second, err := json.Marshal(capabilities())
	if err != nil {
		t.Fatalf("marshal capabilities again: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("capabilities are not deterministic:\n%s\n%s", first, second)
	}
	if strings.Contains(string(first), `"classification":"supported","evidence":[]`) {
		t.Fatalf("supported capability lacks evidence: %s", first)
	}
}

func validConfig(runner CaseRunner, cleanup CleanupFunc) Config {
	return Config{
		RunID: "0123456789abcdef0123456789abcdef",
		Provenance: Provenance{
			SourceCommit:  strings.Repeat("a", 40),
			WorkflowRunID: 123,
		},
		Cases: runner,
		Cleanup: Cleanup{
			Sandbox:         cleanup,
			ProviderBinding: cleanup,
			Workspace:       cleanup,
		},
	}
}

func successfulCleanup(context.Context, CleanupRequest) error {
	return nil
}

func clock(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		value := times[index]
		index++
		return value
	}
}

type caseRunner struct {
	base                time.Time
	requests            []CheckRequest
	bindingError        error
	failAt              CheckName
	failure             error
	mutateAt            CheckName
	mutate               func(*Observation)
	duplicateCommitment bool
}

func (runner *caseRunner) ValidateBinding(_ string, _ Resources) error {
	return runner.bindingError
}

func (runner *caseRunner) Run(_ context.Context, request CheckRequest) (Observation, error) {
	runner.requests = append(runner.requests, request)
	if request.Name == runner.failAt {
		return Observation{}, runner.failure
	}
	index := len(runner.requests) - 1
	commitmentIndex := index
	if runner.duplicateCommitment && index == 1 {
		commitmentIndex = 0
	}
	observation := Observation{
		StartedAt:  runner.base.Add(time.Duration(index*2+1) * time.Second),
		FinishedAt: runner.base.Add(time.Duration(index*2+2) * time.Second),
		Commitment: fmt.Sprintf("sha256:%064x", commitmentIndex),
	}
	if request.Name == runner.mutateAt && runner.mutate != nil {
		runner.mutate(&observation)
	}
	return observation, nil
}
