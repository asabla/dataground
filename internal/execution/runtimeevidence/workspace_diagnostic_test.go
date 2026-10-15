package runtimeevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/workspace"
)

type workspaceProbeProvider struct {
	calls   int
	fail    string
	session *workspaceProbeSession
}

func (p *workspaceProbeProvider) MaterializeWorkspace(ctx context.Context, ref execution.ExecutionRef, source *workspace.Snapshot) (workspace.Receipt, error) {
	p.calls++
	if ctx.Err() != nil {
		return workspace.Receipt{}, ctx.Err()
	}
	if p.calls == 1 || p.calls == 4 {
		if (p.calls == 1 && p.fail == "scope") || (p.calls == 4 && p.fail == "substitution") {
			return source.Receipt(), nil
		}
		return workspace.Receipt{}, errors.New("private provider denial")
	}
	receipt := source.Receipt()
	if p.fail == "receipt" || (p.fail == "retained" && p.calls == 5) {
		receipt.ArchiveDigest = "sha256:" + strings.Repeat("f", 64)
	}
	return receipt, nil
}
func (p *workspaceProbeProvider) StartRuntime(context.Context, execution.ExecutionRef) (execution.RuntimeSession, error) {
	if p.fail == "startup" {
		return nil, errors.New("private startup error")
	}
	return p.session, nil
}

type workspaceProbeInput struct{ bytes.Buffer }

func (*workspaceProbeInput) Close() error { return nil }

type workspaceProbeSession struct {
	input       workspaceProbeInput
	output      io.ReadCloser
	diagnostics io.ReadCloser
	closeErr    error
	closed      bool
	once        sync.Once
}

func (s *workspaceProbeSession) Input() io.WriteCloser { return &s.input }
func (s *workspaceProbeSession) Output() io.ReadCloser { return s.output }
func (s *workspaceProbeSession) Errors() io.ReadCloser { return s.diagnostics }
func (s *workspaceProbeSession) Wait() error           { return nil }
func (s *workspaceProbeSession) Close() error {
	s.once.Do(func() { s.closed = true; _ = s.output.Close(); _ = s.diagnostics.Close() })
	return s.closeErr
}
func workspaceProbeResponses() []map[string]any {
	content := `{"content":"00ff0a2a","fileMode":384,"executableMode":448,"link":"src/data.bin","linkedContent":"00ff0a2a"}`
	return []map[string]any{
		{"id": 1, "result": map[string]any{"userAgent": "fixture"}},
		{"id": 2, "result": map[string]any{"exitCode": 0, "stdout": content, "stderr": ""}},
		{"id": 3, "result": map[string]any{"exitCode": 0, "stdout": "DATAGROUND_SOURCE_EXECUTABLE_OK\n", "stderr": ""}},
	}
}
func workspaceProbeFixture(responses []map[string]any) *workspaceProbeProvider {
	var output bytes.Buffer
	for _, response := range responses {
		_ = json.NewEncoder(&output).Encode(response)
	}
	return &workspaceProbeProvider{session: &workspaceProbeSession{output: io.NopCloser(&output), diagnostics: io.NopCloser(strings.NewReader(""))}}
}
func TestWorkspaceDiagnosticRequiresExactTransferAndClosedNativeEvidence(t *testing.T) {
	runID := strings.Repeat("a", 32)
	ref := execution.ExecutionRef{IsolationDomainID: runtimeIsolationDomain(runID), ID: runtimeExecutionID(runID)}
	for _, failure := range []string{"", "scope", "receipt", "substitution", "retained", "startup", "close", "diagnostic-overflow", "missing-exit", "exit", "stderr", "content", "executable", "id", "result", "oversized", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			responses := workspaceProbeResponses()
			command := responses[1]["result"].(map[string]any)
			switch failure {
			case "missing-exit":
				delete(command, "exitCode")
			case "exit":
				command["exitCode"] = 1
			case "stderr":
				command["stderr"] = "private stderr"
			case "content":
				command["stdout"] = "{}"
			case "executable":
				responses[2]["result"].(map[string]any)["stdout"] = "different"
			case "id":
				responses[1]["id"] = 9
			case "result":
				delete(responses[0], "result")
			case "oversized":
				command["stdout"] = strings.Repeat("x", 17<<10)
			}
			provider := workspaceProbeFixture(responses)
			provider.fail = failure
			if failure == "close" {
				provider.session.closeErr = errors.New("private close error")
			}
			if failure == "diagnostic-overflow" {
				provider.session.diagnostics = io.NopCloser(strings.NewReader(strings.Repeat("x", (64<<10)+1)))
			}
			if failure == "malformed" {
				provider.session.output = io.NopCloser(strings.NewReader("not-json\n"))
			}
			value, err := checkWorkspaceDiagnostic(context.Background(), runID, provider, ref)
			if failure != "" {
				if !errors.Is(err, ErrCase) || value != nil {
					t.Fatalf("failure released evidence: %v", err)
				}
				return
			}
			if err != nil || !validWorkspaceDiagnostic(value, runID) || provider.calls != 5 || !provider.session.closed {
				t.Fatalf("missing evidence or cleanup: %v", err)
			}
			if value.ArchiveDigest != "sha256:923eacd2ade97fcaa909d0427e23a1a868f5e7ee1c15f82c476d2207bfc7c613" {
				t.Fatal("source fixture no longer matches the versioned diagnostic contract")
			}
			requests := strings.Split(strings.TrimSpace(provider.session.input.String()), "\n")
			if len(requests) != 3 {
				t.Fatal("unexpected native requests")
			}
			for _, raw := range requests[1:] {
				var request struct {
					Params struct {
						Cwd    string `json:"cwd"`
						Policy struct {
							Type    string `json:"type"`
							Network bool   `json:"networkAccess"`
						} `json:"sandboxPolicy"`
					} `json:"params"`
				}
				if json.Unmarshal([]byte(raw), &request) != nil || request.Params.Cwd != "/sandbox/workspace" || request.Params.Policy.Type != "readOnly" || request.Params.Policy.Network {
					t.Fatal("native probe broadened authority")
				}
			}
			value.RunID = strings.Repeat("b", 32)
			if validWorkspaceDiagnostic(value, runID) {
				t.Fatal("cross-run evidence accepted")
			}
		})
	}
}
func TestWorkspaceDiagnosticCancellationClosesBlockedNativeRead(t *testing.T) {
	runID := strings.Repeat("a", 32)
	provider := workspaceProbeFixture(nil)
	reader, writer := io.Pipe()
	defer writer.Close()
	provider.session.output = reader
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	value, err := checkWorkspaceDiagnostic(ctx, runID, provider, execution.ExecutionRef{IsolationDomainID: runtimeIsolationDomain(runID), ID: runtimeExecutionID(runID)})
	if !errors.Is(err, ErrCase) || value != nil || !provider.session.closed {
		t.Fatal("cancelled probe released evidence or retained runtime")
	}
}

func TestWorkspaceDiagnosticLauncherBindsSourceBeforeHarnessAndWithholdsIncompleteEvidence(t *testing.T) {
	for _, failure := range []string{"", "probe", "missing", "scope", "chronology", "cleanup"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newLauncherFixture()
			config := LocalDiagnosticConfig{WorkspaceSource: true, RepositoryRoot: "/repository", WorkspaceRoot: "/workspace", CredentialDirectory: "/credentials", SourceCommit: fixture.config().Provenance.SourceCommit, Model: "selected-model", CandidateImage: "sha256:" + strings.Repeat("a", 64), PolicyProfile: RosettaRuntimePolicyProfile, SupervisorCandidateImage: "sha256:" + strings.Repeat("b", 64)}
			deps := fixture.dependencies()
			deps.checkCandidate = func(context.Context, LauncherConfig) error { return nil }
			deps.readPolicy = func(LauncherConfig) ([]byte, error) { return rosettaPolicyBytes(t), nil }
			binding := candidateTopologyBinding{image: config.SupervisorCandidateImage, gatewaySHA256: strings.Repeat("c", 64)}
			deps.openTopology = func(DockerTopologyConfig) (launcherTopology, error) {
				return candidateTestTopology{launcherTopology: fixture.topology, binding: binding}, nil
			}
			base := time.Date(2026, time.October, 15, 12, 0, 0, 0, time.UTC)
			checked := false
			harnessCalled := false
			deps.checkWorkspace = func(_ context.Context, runID string, _ execution.Execution, _ launcherPorts) (*diagnosticWorkspaceSource, error) {
				checked = true
				if harnessCalled {
					t.Fatal("source check followed harness startup")
				}
				if failure == "probe" {
					return nil, errors.New("private source contents")
				}
				if failure == "missing" {
					return nil, nil
				}
				source, _ := workspaceDiagnosticSource(false)
				begin := base.Add(-time.Second)
				end := base.Add(-time.Millisecond)
				if failure == "chronology" {
					begin = base.Add(time.Second)
					end = base.Add(2 * time.Second)
				}
				value := &diagnosticWorkspaceSource{Profile: workspaceDiagnosticProfile, RunID: runID, StartedAt: begin.Format(time.RFC3339Nano), FinishedAt: end.Format(time.RFC3339Nano), ArchiveDigest: source.Receipt().ArchiveDigest, Checks: workspaceDiagnosticChecks}
				if failure == "scope" {
					value.RunID = strings.Repeat("f", 32)
				}
				return value, nil
			}
			deps.newHarness = func(received LauncherConfig, runID string, _ execution.Execution, _ launcherPorts, _ launcherProviderBinding, _ launcherExecutionCreator, _ launcherWorkspace) (launcherHarness, error) {
				harnessCalled = true
				if !checked || !received.workspaceSource {
					t.Fatal("source binding missing before model cases")
				}
				selection := testConfig(&caseRunner{base: base}, successfulCleanup)
				selection.RunID, selection.Provenance = runID, received.Provenance
				selection.diagnosticModel, selection.candidateImage, selection.policyProfile = received.diagnosticModel, received.candidateImage, received.policyProfile
				selection.supervisorCandidateImage, selection.candidateGatewaySHA256 = received.supervisorCandidateImage, received.candidateGatewaySHA256
				run, err := newEvidenceRun(selection, clock(base, base.Add(time.Minute)))
				return localTestHarness{run: run, t: t}, err
			}
			if failure == "cleanup" {
				fixture.topology.cleanupErr = errors.New("private cleanup contents")
			}
			result, err := launchLocalDiagnostic(context.Background(), config, deps)
			encoded, encodeErr := json.Marshal(result)
			if failure != "" {
				if encodeErr == nil {
					t.Fatal("incomplete source evidence serialized")
				}
				if failure != "chronology" && err == nil {
					t.Fatal("failed source or cleanup accepted")
				}
				if (failure == "probe" || failure == "scope" || failure == "missing") && harnessCalled {
					t.Fatal("failed source reached model cases")
				}
				return
			}
			if err != nil || encodeErr != nil || !strings.Contains(string(encoded), workspaceDiagnosticSchemaVersion) {
				t.Fatalf("source diagnostic failed: %v %v", err, encodeErr)
			}
			result.result.workspaceSource = nil
			if _, err := json.Marshal(result); !errors.Is(err, ErrRunIncomplete) {
				t.Fatal("source evidence downgraded")
			}
		})
	}
	fixture := newLauncherFixture()
	config := fixture.config()
	config.workspaceSource = true
	if _, err := launch(context.Background(), config, fixture.dependencies()); !errors.Is(err, ErrLauncherConfiguration) || len(fixture.events) != 0 {
		t.Fatal("source diagnostic reached effects without strict local profile")
	}
	config.diagnosticModel = "selected-model"
	config.Provenance.WorkflowRunID = 0
	config.candidateImage = "sha256:" + strings.Repeat("a", 64)
	config.supervisorCandidateImage = "sha256:" + strings.Repeat("b", 64)
	config.policyProfile = RosettaRuntimePolicyProfile
	if _, err := launch(context.Background(), config, fixture.dependencies()); !errors.Is(err, ErrLauncherConfiguration) || len(fixture.events) != 0 {
		t.Fatal("missing source probe reached external effects")
	}
}
