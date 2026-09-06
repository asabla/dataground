package runtimeevidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
)

type candidateTestTopology struct {
	launcherTopology
	binding candidateTopologyBinding
}

func (topology candidateTestTopology) candidateProfile() (candidateTopologyBinding, error) {
	return topology.binding, nil
}

func TestSupervisorTopologyBindingRequiresExactStartedConfiguration(t *testing.T) {
	content := []byte("frozen gateway")
	digest := sha256.Sum256(content)
	path := filepath.Join(t.TempDir(), "gateway.toml")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	binding := candidateTopologyBinding{image: "sha256:" + strings.Repeat("a", 64), gatewaySHA256: hex.EncodeToString(digest[:])}
	topology := &DockerTopology{state: &dockerTopologyState{candidate: binding, workspace: &runtimeTopologyWorkspace{gatewayPath: path}}}
	if _, err := topology.candidateProfile(); err == nil {
		t.Fatal("unstarted topology supplied provenance")
	}
	topology.state.started, topology.state.active = true, true
	if got, err := topology.candidateProfile(); err != nil || got != binding {
		t.Fatal("exact topology binding failed")
	}
	if err := os.WriteFile(path, []byte("changed gateway"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := topology.candidateProfile(); err == nil {
		t.Fatal("changed topology supplied provenance")
	}
}

func TestSupervisorDiagnosticCarriesExactTopologyAndCannotDowngrade(t *testing.T) {
	for _, substituted := range []bool{false, true} {
		fixture := newLauncherFixture()
		config := LocalDiagnosticConfig{RepositoryRoot: "/repository", WorkspaceRoot: "/workspace", CredentialDirectory: "/credentials", SourceCommit: fixture.config().Provenance.SourceCommit, Model: "selected-model", CandidateImage: "sha256:" + strings.Repeat("a", 64), PolicyProfile: RosettaRuntimePolicyProfile, SupervisorCandidateImage: "sha256:" + strings.Repeat("b", 64)}
		dependencies := fixture.dependencies()
		dependencies.checkCandidate = func(_ context.Context, received LauncherConfig) error {
			if len(fixture.events) != 1 || fixture.events[0] != "run-id" || received.supervisorCandidateImage != config.SupervisorCandidateImage {
				t.Fatal("candidate scan order or selection changed")
			}
			return nil
		}
		dependencies.readPolicy = func(LauncherConfig) ([]byte, error) { return rosettaPolicyBytes(t), nil }
		binding := candidateTopologyBinding{image: config.SupervisorCandidateImage, gatewaySHA256: strings.Repeat("c", 64)}
		if substituted {
			binding.image = "sha256:" + strings.Repeat("d", 64)
		}
		dependencies.openTopology = func(received DockerTopologyConfig) (launcherTopology, error) {
			if received.supervisorCandidateImage != config.SupervisorCandidateImage {
				t.Fatal("topology lost candidate selection")
			}
			return candidateTestTopology{launcherTopology: fixture.topology, binding: binding}, nil
		}
		dependencies.newHarness = func(received LauncherConfig, runID string, _ execution.Execution, _ launcherPorts, _ launcherProviderBinding, _ launcherExecutionCreator, _ launcherWorkspace) (launcherHarness, error) {
			if received.candidateGatewaySHA256 != binding.gatewaySHA256 {
				t.Fatal("harness lost realized gateway digest")
			}
			base := time.Date(2026, time.September, 6, 1, 0, 0, 0, time.UTC)
			selection := testConfig(&caseRunner{base: base}, successfulCleanup)
			selection.RunID, selection.Provenance = runID, received.Provenance
			selection.diagnosticModel, selection.candidateImage, selection.policyProfile = received.diagnosticModel, received.candidateImage, received.policyProfile
			selection.supervisorCandidateImage, selection.candidateGatewaySHA256 = received.supervisorCandidateImage, received.candidateGatewaySHA256
			run, err := newEvidenceRun(selection, clock(base, base.Add(time.Minute)))
			return localTestHarness{run: run, t: t}, err
		}
		result, err := launchLocalDiagnostic(context.Background(), config, dependencies)
		if substituted {
			if err == nil || slices.Contains(fixture.events, "source-open") || slices.Contains(fixture.events, "provider-provision") {
				t.Fatal("substituted topology reached credentials")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var record localDiagnosticRecord
		if json.Unmarshal(encoded, &record) != nil || record.SchemaVersion != supervisorDiagnosticSchemaVersion || record.SupervisorCandidate == nil || record.Profile.SupervisorImage != binding.image || record.Profile.GatewayConfigSHA256 != binding.gatewaySHA256 || record.CertificationEligible {
			t.Fatal("diagnostic lost the exact candidate binding")
		}
		if _, err := json.Marshal(result.result); !errors.Is(err, ErrSerialization) {
			t.Fatal("candidate became CI evidence")
		}
		result.result.supervisorCandidateImage, result.result.candidateGatewaySHA256 = "", ""
		if _, err := json.Marshal(result); !errors.Is(err, ErrRunIncomplete) {
			t.Fatal("candidate became stock supervisor evidence")
		}
	}
}

func TestSupervisorDiagnosticRejectsIncompleteSelectionBeforeEffects(t *testing.T) {
	for _, config := range []LocalDiagnosticConfig{
		{Model: "selected-model", SupervisorCandidateImage: "sha256:" + strings.Repeat("b", 64)},
		{Model: "selected-model", CandidateImage: "sha256:" + strings.Repeat("a", 64), SupervisorCandidateImage: "sha256:" + strings.Repeat("b", 64)},
		{Model: "selected-model", CandidateImage: "sha256:" + strings.Repeat("a", 64), PolicyProfile: RosettaRuntimePolicyProfile, SupervisorCandidateImage: "candidate:latest"},
	} {
		fixture := newLauncherFixture()
		if _, err := launchLocalDiagnostic(context.Background(), config, fixture.dependencies()); !errors.Is(err, ErrLauncherConfiguration) || len(fixture.events) != 0 {
			t.Fatal("invalid supervisor selection reached effects")
		}
	}
}
