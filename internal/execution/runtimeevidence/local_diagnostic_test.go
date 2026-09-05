package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/asabla/dataground/internal/execution"
	"strings"
	"testing"
	"time"
)

func TestLocalDiagnosticCannotSerializeAsCertificationEvidence(t *testing.T) {
	base := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
	config := testConfig(&caseRunner{base: base}, successfulCleanup)
	config.Provenance.WorkflowRunID = 0
	config.diagnosticModel = "selected-model.v1"
	run, err := newEvidenceRun(config, clock(base, base.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := run.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(result); !errors.Is(err, ErrSerialization) {
		t.Fatalf("local result escaped as CI evidence: %v", err)
	}
	encoded, err := json.Marshal(LocalDiagnosticResult{result: result})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if document["schemaVersion"] != LocalDiagnosticSchemaVersion || document["certificationEligible"] != false || document["result"] != "passed" {
		t.Fatal("incorrect diagnostic identity")
	}
	value := document["run"].(map[string]any)
	if value["origin"] != "local" || value["model"] != config.diagnosticModel || value["sourceCommit"] != config.Provenance.SourceCommit {
		t.Fatal("missing exact local binding")
	}
	for _, field := range []string{"workflow", "workflowRunID", "artifactName", "capabilities"} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("local report contains certification field %s", field)
		}
	}
	if len(document["checks"].([]any)) != len(requiredChecks) {
		t.Fatal("missing live checks")
	}
	if _, err := run.Execute(context.Background()); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatal("local replay did not fail closed")
	}
	result.record.Run.Provenance.WorkflowRunID = 42
	if _, err := json.Marshal(LocalDiagnosticResult{result: result}); !errors.Is(err, ErrRunIncomplete) {
		t.Fatal("mixed provenance serialized")
	}
	if _, err := json.Marshal(LocalDiagnosticResult{}); !errors.Is(err, ErrRunIncomplete) {
		t.Fatal("incomplete report serialized")
	}
}

func TestLocalDiagnosticRejectsInvalidSelectionBeforeEffects(t *testing.T) {
	for _, model := range []string{"", " with-space", "provider/model", "--flag", "$(secret)", strings.Repeat("a", 129)} {
		fixture := newLauncherFixture()
		config := LocalDiagnosticConfig{RepositoryRoot: "/repository", WorkspaceRoot: "/workspace", CredentialDirectory: "/credentials", SourceCommit: fixture.config().Provenance.SourceCommit, Model: model}
		if _, err := launchLocalDiagnostic(context.Background(), config, fixture.dependencies()); !errors.Is(err, ErrLauncherConfiguration) {
			t.Fatalf("invalid selection accepted: %v", err)
		}
		if len(fixture.events) != 0 {
			t.Fatal("invalid selection caused effects")
		}
	}
	fixture := newLauncherFixture()
	config := fixture.config()
	config.Provenance.WorkflowRunID = 0
	if _, err := launch(context.Background(), config, fixture.dependencies()); !errors.Is(err, ErrLauncherConfiguration) {
		t.Fatal("CI run accepted absent workflow")
	}
	config.Provenance.WorkflowRunID = 42
	config.diagnosticModel = "selected-model"
	if _, err := launch(context.Background(), config, fixture.dependencies()); !errors.Is(err, ErrLauncherConfiguration) {
		t.Fatal("local run accepted workflow identity")
	}
	if len(fixture.events) != 0 {
		t.Fatal("invalid provenance caused effects")
	}
}

func TestLocalDiagnosticFailureStillCleansAndReleasesNoReport(t *testing.T) {
	fixture := newLauncherFixture()
	fixture.ports.checkErr = errors.New("private native error")
	config := LocalDiagnosticConfig{RepositoryRoot: "/repository", WorkspaceRoot: "/workspace", CredentialDirectory: "/credentials", SourceCommit: fixture.config().Provenance.SourceCommit, Model: "selected-model"}
	result, err := launchLocalDiagnostic(context.Background(), config, fixture.dependencies())
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe failure: %v", err)
	}
	if len(fixture.events) == 0 || fixture.events[len(fixture.events)-1] != "workspace-cleanup" {
		t.Fatal("local failure skipped owned cleanup")
	}
	if _, err := json.Marshal(result); !errors.Is(err, ErrRunIncomplete) {
		t.Fatal("failed local run released report")
	}
}

func TestLocalDiagnosticLauncherPropagatesBindingAndWithholdsReportOnCleanupFailure(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		fixture := newLauncherFixture()
		config := LocalDiagnosticConfig{RepositoryRoot: "/repository", WorkspaceRoot: "/workspace", CredentialDirectory: "/credentials", SourceCommit: fixture.config().Provenance.SourceCommit, Model: "selected-model"}
		dependencies := fixture.dependencies()
		dependencies.newHarness = func(received LauncherConfig, runID string, _ execution.Execution, _ launcherPorts, _ launcherProviderBinding, _ launcherExecutionCreator, _ launcherWorkspace) (launcherHarness, error) {
			if received.diagnosticModel != config.Model || received.Provenance.SourceCommit != config.SourceCommit || received.Provenance.WorkflowRunID != 0 {
				t.Fatal("local launcher changed binding")
			}
			base := time.Date(2026, time.September, 5, 12, 0, 0, 0, time.UTC)
			evidenceConfig := testConfig(&caseRunner{base: base}, successfulCleanup)
			evidenceConfig.RunID = runID
			evidenceConfig.Provenance = received.Provenance
			evidenceConfig.diagnosticModel = received.diagnosticModel
			run, err := newEvidenceRun(evidenceConfig, clock(base, base.Add(time.Minute)))
			return localTestHarness{run: run, t: t}, err
		}
		if cleanupFails {
			fixture.topology.cleanupErr = errors.New("private cleanup contents")
		}
		result, err := launchLocalDiagnostic(context.Background(), config, dependencies)
		encoded, encodeErr := json.Marshal(result)
		if cleanupFails {
			if err == nil || err.Error() != "local runtime diagnostic failed at cleanup" || !errors.Is(encodeErr, ErrRunIncomplete) {
				t.Fatalf("cleanup did not withhold report safely: %v, %v", err, encodeErr)
			}
		} else if err != nil || encodeErr != nil || !strings.Contains(string(encoded), LocalDiagnosticSchemaVersion) {
			t.Fatalf("local run failed: %v, %v", err, encodeErr)
		}
		if fixture.events[len(fixture.events)-1] != "topology-cleanup" {
			t.Fatal("topology cleanup was not observed")
		}
	}
}

type localTestHarness struct {
	run *EvidenceRun
	t   *testing.T
}

func (harness localTestHarness) Run(ctx context.Context) (Result, error) {
	deadline, bounded := ctx.Deadline()
	if !bounded || time.Until(deadline) > localDiagnosticTimeout {
		harness.t.Fatal("local run has no bounded execution budget")
	}
	return harness.run.Execute(ctx)
}
