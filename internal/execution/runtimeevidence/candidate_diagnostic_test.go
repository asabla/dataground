package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

const candidateRuntimeTestPolicy = "version: 1\n\n# Proxy-mode baseline paths omit the null device used for child standard I/O.\n# Grant this device explicitly; do not grant the containing /dev directory.\nfilesystem_policy:\n  read_write:\n    - /dev/null\n"

func TestCandidateDiagnosticRejectsMixedProvenanceAndMutableImages(t *testing.T) {
	for _, image := range []string{"candidate:latest", "sha256:" + strings.Repeat("a", 63)} {
		fixture := newLauncherFixture()
		config := LocalDiagnosticConfig{Model: "selected-model", CandidateImage: image}
		if _, err := launchLocalDiagnostic(context.Background(), config, fixture.dependencies()); !errors.Is(err, ErrLauncherConfiguration) || len(fixture.events) != 0 {
			t.Fatalf("mutable candidate caused effects: %v", err)
		}
	}
	fixture := newLauncherFixture()
	config := fixture.config()
	config.candidateImage = "sha256:" + strings.Repeat("a", 64)
	if _, err := launch(context.Background(), config, fixture.dependencies()); !errors.Is(err, ErrLauncherConfiguration) || len(fixture.events) != 0 {
		t.Fatalf("candidate entered CI evidence mode: %v", err)
	}
}

func TestCandidateDiagnosticRequiresCredentialScanBeforeOpeningLocalCredentials(t *testing.T) {
	for _, available := range []bool{false, true} {
		fixture := newLauncherFixture()
		config := LocalDiagnosticConfig{RepositoryRoot: "/repository", WorkspaceRoot: "/workspace", CredentialDirectory: "/credentials", SourceCommit: fixture.config().Provenance.SourceCommit, Model: "selected-model", CandidateImage: "sha256:" + strings.Repeat("a", 64)}
		dependencies := fixture.dependencies()
		want := []string{"run-id"}
		if available {
			dependencies.checkCandidate = func(context.Context, LauncherConfig) error {
				fixture.events = append(fixture.events, "candidate-check")
				return errors.New("private scan failure")
			}
			want = append(want, "candidate-check")
		}
		result, err := launchLocalDiagnostic(context.Background(), config, dependencies)
		if err == nil || err.Error() != "local runtime diagnostic failed at candidate-credential-check" || !reflect.DeepEqual(fixture.events, want) {
			t.Fatalf("failed scan reached credential or runtime effects: %v, %v", err, fixture.events)
		}
		if _, err := json.Marshal(result); !errors.Is(err, ErrRunIncomplete) {
			t.Fatal("failed scan released a report")
		}
	}
}

func TestCandidateDiagnosticBindsActualImageWithoutStockCredentialEvidence(t *testing.T) {
	fixture := newLauncherFixture()
	config := LocalDiagnosticConfig{RepositoryRoot: "/repository", WorkspaceRoot: "/workspace", CredentialDirectory: "/credentials", SourceCommit: fixture.config().Provenance.SourceCommit, Model: "selected-model", CandidateImage: "sha256:" + strings.Repeat("a", 64)}
	dependencies := fixture.dependencies()
	dependencies.checkCandidate = func(_ context.Context, received LauncherConfig) error {
		if received.candidateImage != config.CandidateImage || !reflect.DeepEqual(fixture.events, []string{"run-id"}) {
			t.Fatal("candidate scan was reordered or substituted")
		}
		fixture.events = append(fixture.events, "candidate-check")
		return nil
	}
	create := dependencies.newCreator
	dependencies.newCreator = func(received LauncherConfig, runID string, policy []byte, ports launcherPorts) (launcherExecutionCreator, error) {
		if received.candidateImage != config.CandidateImage {
			t.Fatal("candidate creation lost exact image")
		}
		return create(received, runID, policy, ports)
	}
	dependencies.newHarness = func(received LauncherConfig, runID string, _ execution.Execution, _ launcherPorts, _ launcherProviderBinding, _ launcherExecutionCreator, _ launcherWorkspace) (launcherHarness, error) {
		base := time.Date(2026, time.September, 6, 1, 0, 0, 0, time.UTC)
		selection := testConfig(&caseRunner{base: base}, successfulCleanup)
		selection.RunID, selection.Provenance = runID, received.Provenance
		selection.diagnosticModel, selection.candidateImage = received.diagnosticModel, received.candidateImage
		run, err := newEvidenceRun(selection, clock(base, base.Add(time.Minute)))
		return localTestHarness{run: run, t: t}, err
	}
	result, err := launchLocalDiagnostic(context.Background(), config, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(encoded, &record); err != nil {
		t.Fatal(err)
	}
	if record["schemaVersion"] != CandidateDiagnosticSchemaVersion || record["certificationEligible"] != false || record["candidateCredentialCheck"] != "passed" || record["profile"].(map[string]any)["sandboxImage"] != config.CandidateImage {
		t.Fatal("candidate report misidentified the checked image")
	}
	for _, field := range []string{"credentialEvidenceSHA256", "workflowRunID", "capabilities"} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("candidate report inherited %s", field)
		}
	}
	if len(record["checks"].([]any)) != len(requiredChecks) {
		t.Fatal("candidate skipped live cases")
	}
	if _, err := json.Marshal(result.result); !errors.Is(err, ErrSerialization) {
		t.Fatal("candidate result serialized as CI evidence")
	}
	for _, digest := range []string{"", strings.Repeat("0", 64), runtimePolicySHA256} {
		changed := result
		changed.result.record.Profile.RuntimePolicySHA256 = digest
		if _, err := json.Marshal(changed); !errors.Is(err, ErrRunIncomplete) {
			t.Fatal("substituted runtime policy was accepted")
		}
	}
	result.result.record.Profile.CredentialEvidenceSHA256 = credentialEvidenceSHA256
	if _, err := json.Marshal(result); !errors.Is(err, ErrRunIncomplete) {
		t.Fatal("stock credential evidence was accepted for candidate")
	}
}

type candidateCreatorProvider struct {
	*executionCreatorProvider
	diagnosticCalls int
}

func (provider *candidateCreatorProvider) CreateLocalDiagnostic(ctx context.Context, request execution.CreateRequest) (execution.Execution, error) {
	provider.diagnosticCalls++
	return provider.executionCreatorProvider.Create(ctx, request)
}

func TestCandidateCreatorUsesDedicatedLocalImageOperation(t *testing.T) {
	fixture := newExecutionCreatorFixture()
	config := fixture.config()
	config.diagnosticImage = "sha256:" + strings.Repeat("a", 64)
	config.Policy = []byte(candidateRuntimeTestPolicy)
	if _, err := newExecutionCreator(config, fixture.poll); !errors.Is(err, ErrExecutionCreationConfiguration) {
		t.Fatal("provider without diagnostic operation was accepted")
	}
	provider := &candidateCreatorProvider{executionCreatorProvider: fixture.provider}
	config.Provider = provider
	creator, err := newExecutionCreator(config, fixture.poll)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.diagnosticCalls != 1 || provider.createRequest.Image != config.diagnosticImage || provider.createRequest.PolicyDigest != "sha256:"+candidateRuntimePolicySHA256 {
		t.Fatal("candidate creation lost its image, operation or policy binding")
	}
	if err := creator.Cleanup(context.Background(), CleanupRequest{RunID: testRunID, ResourceKind: "sandbox", ResourceName: namesForRun(testRunID).Sandbox}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalNormalizationFailuresRemainClosedAndSingleUse(t *testing.T) {
	state := &codexProbeState{diagnosticModel: "selected-model"}
	for _, stage := range []string{"turn-start", "event-stream", "turn-completion", "command-start", "command-completion", "marker", "transport-close"} {
		failure := state.normalizationFailure(stage)
		if failure.Error() != "local runtime diagnostic failed at case-event-normalization-"+stage {
			t.Fatal("unexpected diagnostic failure")
		}
		cases := newTestLiveCases(t, &liveScenario{fail: failure})
		_, err := cases.Run(context.Background(), testLiveRequest(CheckGatewayReady))
		var local *LocalDiagnosticError
		if !errors.As(err, &local) || local.Error() != failure.Error() {
			t.Fatal("safe local failure was lost")
		}
		if _, err := cases.Run(context.Background(), testLiveRequest(CheckGatewayReady)); !errors.Is(err, ErrLiveCaseOrder) {
			t.Fatal("diagnostic failure allowed live-case replay")
		}
	}
	if err := state.normalizationFailure("private native content"); err != ErrCodexProbeObservation {
		t.Fatal("unknown diagnostic content was exposed")
	}
	state.diagnosticModel = ""
	if err := state.normalizationFailure("command-start"); err != ErrCodexProbeObservation {
		t.Fatal("CI mode emitted local diagnostic details")
	}
}

func TestLocalNormalizationFailureSurvivesCompleteEvidenceComposition(t *testing.T) {
	failure := (&codexProbeState{diagnosticModel: "selected-model"}).normalizationFailure("command-start")
	scenario := newTestConcreteScenario(t, &scenarioProbes{fail: failure})
	cases, err := NewLiveCases(testRunID, scenario)
	if err != nil {
		t.Fatal(err)
	}
	cleanups := 0
	remove := func(context.Context, CleanupRequest) error { cleanups++; return nil }
	run, err := New(Config{RunID: testRunID, diagnosticModel: "selected-model", Provenance: Provenance{SourceCommit: strings.Repeat("1", 40)}, Cases: cases, Cleanup: Cleanup{Sandbox: remove, ProviderBinding: remove, Workspace: remove}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := run.Execute(context.Background())
	var diagnostic *LocalDiagnosticError
	if !errors.As(err, &diagnostic) || diagnostic.Error() != failure.Error() || cleanups != 3 {
		t.Fatalf("closed diagnostic or cleanup lost: %v, cleanups %d", err, cleanups)
	}
	if _, err := json.Marshal(result); !errors.Is(err, ErrRunIncomplete) {
		t.Fatal("failed diagnostic released evidence")
	}
}

func TestCandidatePolicySelectionAndDrift(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []bool{false, true} {
		config := LauncherConfig{RepositoryRoot: root}
		want := executionCreatorTestPolicy
		if candidate {
			config.candidateImage = "sha256:" + strings.Repeat("a", 64)
			want = candidateRuntimeTestPolicy
		}
		policy, err := readRuntimeLauncherPolicy(config)
		if err != nil || string(policy) != want {
			t.Fatal("launcher selected a different policy")
		}
	}
	fixture := newExecutionCreatorFixture()
	config := fixture.config()
	config.Provider = &candidateCreatorProvider{executionCreatorProvider: fixture.provider}
	config.diagnosticImage = "sha256:" + strings.Repeat("a", 64)
	for _, policy := range []string{executionCreatorTestPolicy, strings.ReplaceAll(candidateRuntimeTestPolicy, "/dev/null", "/dev"), candidateRuntimeTestPolicy + "\n"} {
		config.Policy = []byte(policy)
		if _, err := newExecutionCreator(config, fixture.poll); !errors.Is(err, ErrExecutionCreationConfiguration) {
			t.Fatal("candidate accepted policy drift")
		}
	}
	config.diagnosticImage = ""
	config.Policy = []byte(candidateRuntimeTestPolicy)
	if _, err := newExecutionCreator(config, fixture.poll); !errors.Is(err, ErrExecutionCreationConfiguration) {
		t.Fatal("default execution accepted the candidate policy")
	}
}
