package runtimeevidence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

func rosettaPolicyBytes(t *testing.T) []byte {
	t.Helper()
	value, err := os.ReadFile("../../../" + rosettaRuntimePolicyPath)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestRosettaDiagnosticRequiresExplicitLocalCandidate(t *testing.T) {
	for _, config := range []LocalDiagnosticConfig{
		{Model: "selected-model", PolicyProfile: RosettaRuntimePolicyProfile},
		{CandidateImage: "sha256:" + strings.Repeat("a", 64), PolicyProfile: RosettaRuntimePolicyProfile},
		{Model: "selected-model", CandidateImage: "sha256:" + strings.Repeat("a", 64), PolicyProfile: "other-profile"},
	} {
		fixture := newLauncherFixture()
		if _, err := launchLocalDiagnostic(context.Background(), config, fixture.dependencies()); !errors.Is(err, ErrLauncherConfiguration) || len(fixture.events) != 0 {
			t.Fatal("invalid policy selection reached effects")
		}
	}
}

func TestRosettaCandidateCreationBindsExactCompilerOutput(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := readRuntimeLauncherPolicy(LauncherConfig{RepositoryRoot: root, candidateImage: "sha256:" + strings.Repeat("a", 64), diagnosticModel: "selected-model", policyProfile: RosettaRuntimePolicyProfile})
	if err != nil || !reflect.DeepEqual(policy, rosettaPolicyBytes(t)) {
		t.Fatal("launcher lost exact Rosetta artifact")
	}
	fixture := newExecutionCreatorFixture()
	config := fixture.config()
	config.diagnosticImage, config.policyProfile = "sha256:"+strings.Repeat("a", 64), RosettaRuntimePolicyProfile
	config.Policy = policy
	provider := &candidateCreatorProvider{executionCreatorProvider: fixture.provider}
	config.Provider = provider
	creator, err := newExecutionCreator(config, fixture.poll)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.createRequest.PolicyDigest != "sha256:"+rosettaRuntimePolicySHA256 || provider.createRequest.Image != config.diagnosticImage {
		t.Fatal("Rosetta creation used a different policy or image")
	}
	if err := creator.Cleanup(context.Background(), CleanupRequest{RunID: testRunID, ResourceKind: "sandbox", ResourceName: namesForRun(testRunID).Sandbox}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{[]byte(candidateRuntimeTestPolicy), append(append([]byte{}, policy...), '\n')} {
		config.Policy = bad
		if _, err := newExecutionCreator(config, fixture.poll); !errors.Is(err, ErrExecutionCreationConfiguration) {
			t.Fatal("substituted Rosetta policy accepted")
		}
	}
}

func TestRosettaDiagnosticKeepsSeparateSchemaAndCompilerIdentity(t *testing.T) {
	fixture := newLauncherFixture()
	config := LocalDiagnosticConfig{RepositoryRoot: "/repository", WorkspaceRoot: "/workspace", CredentialDirectory: "/credentials", SourceCommit: fixture.config().Provenance.SourceCommit, Model: "selected-model", CandidateImage: "sha256:" + strings.Repeat("a", 64), PolicyProfile: RosettaRuntimePolicyProfile}
	dependencies := fixture.dependencies()
	dependencies.checkCandidate = func(_ context.Context, received LauncherConfig) error {
		if received.policyProfile != config.PolicyProfile || !reflect.DeepEqual(fixture.events, []string{"run-id"}) {
			t.Fatal("scan order or profile changed")
		}
		fixture.events = append(fixture.events, "candidate-check")
		return nil
	}
	dependencies.readPolicy = func(received LauncherConfig) ([]byte, error) {
		if received.policyProfile != config.PolicyProfile {
			t.Fatal("policy selection lost")
		}
		return rosettaPolicyBytes(t), nil
	}
	dependencies.newHarness = func(received LauncherConfig, runID string, _ execution.Execution, _ launcherPorts, _ launcherProviderBinding, _ launcherExecutionCreator, _ launcherWorkspace) (launcherHarness, error) {
		base := time.Date(2026, time.September, 6, 1, 0, 0, 0, time.UTC)
		selection := testConfig(&caseRunner{base: base}, successfulCleanup)
		selection.RunID, selection.Provenance = runID, received.Provenance
		selection.diagnosticModel, selection.candidateImage, selection.policyProfile = received.diagnosticModel, received.candidateImage, received.policyProfile
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
	var record localDiagnosticRecord
	if json.Unmarshal(encoded, &record) != nil || record.SchemaVersion != rosettaDiagnosticSchemaVersion || record.Profile.RuntimePolicySHA256 != rosettaRuntimePolicySHA256 || record.PolicySource == nil || record.PolicySource.CompilerSourceCommit != rosettaRuntimeSourceCommit || record.PolicySource.InputSHA256 != rosettaRuntimeInputSHA256 || record.CertificationEligible {
		t.Fatal("Rosetta diagnostic lost provenance or limits")
	}
	if _, err := json.Marshal(result.result); !errors.Is(err, ErrSerialization) {
		t.Fatal("Rosetta diagnostic became CI evidence")
	}
	result.result.policyProfile = ""
	if _, err := json.Marshal(result); !errors.Is(err, ErrRunIncomplete) {
		t.Fatal("Rosetta diagnostic became legacy fixture evidence")
	}
}
