package runtimeevidence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/workspace"
)

type ownedProbeSession struct {
	*workspaceProbeSession
	binding, confirmed string
	proofErr           error
}

func (s *ownedProbeSession) OwnerBinding() (string, error) { return s.binding, nil }
func (s *ownedProbeSession) ConfirmedOwnerBinding() (string, error) {
	if !s.closed {
		return "", ErrCase
	}
	return s.confirmed, s.proofErr
}

type ownedProbeBackend struct {
	ref                                        execution.ExecutionRef
	changes, other                             *workspace.ChangeSet
	failure                                    string
	captures, unowned, artifacts, terminations int
	onCapture                                  func()
}

func (b *ownedProbeBackend) StartRuntime(context.Context, execution.ExecutionRef) (execution.RuntimeSession, error) {
	b.unowned++
	return nil, ErrCase
}
func (b *ownedProbeBackend) Observe(context.Context, execution.ExecutionRef) (execution.Observation, error) {
	return execution.Observation{}, nil
}
func (b *ownedProbeBackend) MaterializeWorkspace(context.Context, execution.ExecutionRef, *workspace.Snapshot) (workspace.Receipt, error) {
	return workspace.Receipt{}, nil
}
func (b *ownedProbeBackend) Terminate(context.Context, execution.ExecutionRef) error {
	b.terminations++
	return nil
}
func (b *ownedProbeBackend) Export(context.Context, execution.ExportRequest) (execution.ExportResult, error) {
	b.artifacts++
	if b.failure == "artifact" {
		return execution.ExportResult{}, ErrCase
	}
	return execution.ExportResult{IsolationDomainID: b.ref.IsolationDomainID, ExecutionID: b.ref.ID, Content: []byte("fixture")}, nil
}
func (b *ownedProbeBackend) ExportWorkspaceChanges(ctx context.Context, ref execution.ExecutionRef, base *workspace.Snapshot) (*workspace.ChangeSet, error) {
	if b.onCapture != nil {
		b.onCapture()
	}
	if ref != b.ref {
		if b.failure == "scope" {
			return b.changes, nil
		}
		return nil, ErrCase
	}
	b.captures++
	if ctx.Err() != nil || b.failure == "capture" {
		return nil, ErrCase
	}
	if b.failure == "changed" && b.captures == 2 || b.failure == "tree" {
		return b.other, nil
	}
	return b.changes, nil
}
func ownedProbeChanges(t *testing.T, altered bool) *workspace.ChangeSet {
	t.Helper()
	ctx := context.Background()
	base, err := workspaceDiagnosticSource(false)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if _, err := base.Materialize(ctx, parent); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"src/data.bin": {255, 0, 42, 10}, "new.bin": {0, 128, 255}, "writer.bin": {0, 0, 0, 0, 0, 0, 0, 2}} {
		if altered && name == "new.bin" {
			data = []byte{0, 128, 254}
		}
		if err := os.WriteFile(filepath.Join(parent, "workspace", name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(parent, "workspace", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(parent, "workspace", "run.sh")); err != nil {
		t.Fatal(err)
	}
	final, err := workspace.Capture(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := workspace.Compare(base, final)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}
func newOwnedProbe(t *testing.T, failure string) (*ownedDiagnosticProvider, *ownedProbeBackend, *[]*ownedProbeSession) {
	t.Helper()
	runID := strings.Repeat("a", 32)
	ref := execution.ExecutionRef{IsolationDomainID: runtimeIsolationDomain(runID), ID: runtimeExecutionID(runID)}
	backend := &ownedProbeBackend{ref: ref, changes: ownedProbeChanges(t, false), other: ownedProbeChanges(t, true), failure: failure}
	sessions := []*ownedProbeSession{}
	p := &ownedDiagnosticProvider{backend: backend, ref: ref, runID: runID}
	p.open = func(_ context.Context, _ execution.ExecutionRef) (ownedDiagnosticSession, error) {
		index := len(sessions) + 1
		if failure == "startup" && index == ownedDiagnosticSessionCount {
			return nil, ErrCase
		}
		responses := []map[string]any{{"id": 1, "result": map[string]any{"userAgent": "fixture"}}, {"id": 2, "result": map[string]any{"exitCode": 0, "stdout": "", "stderr": ""}}, {"id": 3, "result": map[string]any{"exitCode": 0, "stdout": "", "stderr": ""}}}
		if index == ownedDiagnosticSessionCount && failure == "writer" {
			responses[1]["result"].(map[string]any)["exitCode"] = 1
		}
		if index == ownedDiagnosticSessionCount && failure == "observation" {
			responses[2]["result"].(map[string]any)["exitCode"] = 1
		}
		raw := workspaceProbeFixture(responses).session
		binding := fmt.Sprintf("sha256:%064x", index)
		s := &ownedProbeSession{workspaceProbeSession: raw, binding: binding, confirmed: binding}
		if index == ownedDiagnosticSessionCount {
			switch failure {
			case "cleanup":
				s.closeErr = ErrCase
			case "proof":
				s.proofErr = ErrCase
			case "substituted":
				s.confirmed = "sha256:" + strings.Repeat("f", 64)
			case "binding":
				s.binding = "invalid"
			case "duplicate":
				s.binding = fmt.Sprintf("sha256:%064x", 1)
				s.confirmed = s.binding
			}
		}
		sessions = append(sessions, s)
		return s, nil
	}
	return p, backend, &sessions
}
func closeOwnedProbeSessions(t *testing.T, p *ownedDiagnosticProvider, count int) {
	t.Helper()
	for range count {
		s, err := p.StartRuntime(context.Background(), p.ref)
		if err != nil {
			t.Fatal(err)
		}
		if s.Close() != nil || s.Close() != nil {
			t.Fatal("owner close replay failed")
		}
	}
}
func ownedProbeRequest(p *ownedDiagnosticProvider) execution.ExportRequest {
	return execution.ExportRequest{IsolationDomainID: p.ref.IsolationDomainID, ExecutionID: p.ref.ID, SandboxPath: runtimeArtifactPath(p.runID)}
}
func TestOwnedDiagnosticBindsAllNativeSessionsAndTerminalBinaryExport(t *testing.T) {
	for _, failure := range []string{"", "startup", "writer", "observation", "cleanup", "proof", "substituted", "binding", "duplicate", "scope", "capture", "changed", "tree", "artifact"} {
		t.Run(failure, func(t *testing.T) {
			p, b, sessions := newOwnedProbe(t, failure)
			ctx := context.Background()
			if p.Terminate(ctx, p.ref) == nil {
				t.Fatal("teardown before export")
			}
			if _, err := p.Export(ctx, ownedProbeRequest(p)); err == nil {
				t.Fatal("missing model sessions accepted")
			}
			closeOwnedProbeSessions(t, p, ownedDiagnosticSessionCount-1)
			b.onCapture = func() {
				if _, err := p.StartRuntime(ctx, p.ref); err == nil {
					t.Fatal("native start during capture")
				}
				base, _ := workspaceDiagnosticSource(false)
				if _, err := p.MaterializeWorkspace(ctx, p.ref, base); err == nil {
					t.Fatal("source write during capture")
				}
			}
			_, err := p.Export(ctx, ownedProbeRequest(p))
			if b.unowned != 0 {
				t.Fatal("unowned transport reached")
			}
			if failure != "" {
				if err == nil || p.Evidence() != nil || p.Terminate(ctx, p.ref) == nil {
					t.Fatal("failed proof released evidence or teardown")
				}
				if _, err := p.StartRuntime(ctx, p.ref); err == nil {
					t.Fatal("failed proof allowed new native turn")
				}
				return
			}
			proof := p.Evidence()
			if err != nil || !validOwnedWorkspaceDiagnostic(proof, p.runID) || b.captures != 2 || b.artifacts != 1 || len(*sessions) != ownedDiagnosticSessionCount {
				t.Fatalf("incomplete export: %v %#v", err, proof)
			}
			for _, session := range *sessions {
				if !session.closed {
					t.Fatal("native session remained open")
				}
			}
			proof.RunID = "changed"
			if p.Evidence().RunID != p.runID {
				t.Fatal("mutable evidence alias")
			}
			if _, err := p.StartRuntime(ctx, p.ref); err == nil {
				t.Fatal("native turn after final export")
			}
			if _, err := p.Export(ctx, ownedProbeRequest(p)); err == nil {
				t.Fatal("repeated export restarts probe")
			}
			if p.Terminate(ctx, p.ref) != nil || b.terminations != 1 {
				t.Fatal("successful export did not permit fixture cleanup")
			}
			var request map[string]any
			lines := bytes.Split(bytes.TrimSpace((*sessions)[ownedDiagnosticSessionCount-1].input.Bytes()), []byte("\n"))
			if len(lines) != 3 || json.Unmarshal(lines[1], &request) != nil || request["method"] != "command/exec" {
				t.Fatal("missing fixed native probe")
			}
		})
	}
}
func TestOwnedDiagnosticRejectsConcurrentStartsAndForeignTargets(t *testing.T) {
	p, b, _ := newOwnedProbe(t, "")
	ctx := context.Background()
	wrong := p.ref
	wrong.IsolationDomainID = runtimeIsolationDomain(p.runID + ":other")
	if _, err := p.StartRuntime(ctx, wrong); err == nil {
		t.Fatal("foreign native target")
	}
	session, err := p.StartRuntime(ctx, p.ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.StartRuntime(ctx, p.ref); err == nil {
		t.Fatal("overlapping native sessions")
	}
	if _, err := p.Export(ctx, ownedProbeRequest(p)); err == nil {
		t.Fatal("live writer exported")
	}
	if session.Close() != nil {
		t.Fatal("close failed")
	}
	closeOwnedProbeSessions(t, p, ownedDiagnosticSessionCount-2)
	request := ownedProbeRequest(p)
	request.SandboxPath = "/sandbox/other"
	if _, err := p.Export(ctx, request); err == nil || b.captures != 0 {
		t.Fatal("foreign artifact path reached probe")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.Export(cancelled, ownedProbeRequest(p)); err == nil || b.captures != 0 {
		t.Fatal("cancelled export reached capture")
	}
}
func TestOwnedDiagnosticEvidenceRejectsScopeWindowAndReceiptSubstitution(t *testing.T) {
	p, _, _ := newOwnedProbe(t, "")
	closeOwnedProbeSessions(t, p, 10)
	if _, err := p.Export(context.Background(), ownedProbeRequest(p)); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*diagnosticWorkspaceExport){
		func(v *diagnosticWorkspaceExport) { v.RunID = strings.Repeat("b", 32) },
		func(v *diagnosticWorkspaceExport) { v.OwnedSessions-- },
		func(v *diagnosticWorkspaceExport) { v.Checks[0] = "omitted" },
		func(v *diagnosticWorkspaceExport) { v.FinishedAt = v.StartedAt },
		func(v *diagnosticWorkspaceExport) {
			start, _ := time.Parse(time.RFC3339Nano, v.StartedAt)
			v.FinishedAt = start.Add(2*time.Minute + time.Nanosecond).Format(time.RFC3339Nano)
		},
		func(v *diagnosticWorkspaceExport) { v.Changes.BaseArchiveDigest = v.Changes.ResultArchiveDigest },
		func(v *diagnosticWorkspaceExport) { v.Changes.SizeBytes = 65537 },
		func(v *diagnosticWorkspaceExport) { v.Changes.SourceBytes++ },
	} {
		v := p.Evidence()
		change(v)
		if validOwnedWorkspaceDiagnostic(v, p.runID) {
			t.Fatal("substituted evidence accepted")
		}
	}
}

func TestOwnedDiagnosticSerializationBindsExportToArtifactCase(t *testing.T) {
	baseTime := time.Date(2026, time.October, 29, 12, 0, 0, 0, time.UTC)
	config := testConfig(&caseRunner{base: baseTime}, successfulCleanup)
	config.Provenance.WorkflowRunID = 0
	config.diagnosticModel = "selected-model"
	config.candidateImage = "sha256:" + strings.Repeat("a", 64)
	config.policyProfile = RosettaRuntimePolicyProfile
	config.supervisorCandidateImage = "sha256:" + strings.Repeat("b", 64)
	config.candidateGatewaySHA256 = strings.Repeat("c", 64)
	run, err := newEvidenceRun(config, clock(baseTime, baseTime.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	result, err := run.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source, _ := workspaceDiagnosticSource(false)
	result.workspaceSourceRequired = true
	result.workspaceSource = &diagnosticWorkspaceSource{Profile: workspaceDiagnosticProfile, RunID: config.RunID, StartedAt: baseTime.Add(-time.Second).Format(time.RFC3339Nano), FinishedAt: baseTime.Add(-time.Millisecond).Format(time.RFC3339Nano), ArchiveDigest: source.Receipt().ArchiveDigest, Checks: workspaceDiagnosticChecks}
	result.workspaceExportRequired = true
	proof := diagnosticWorkspaceExport{Profile: ownedWorkspaceDiagnosticProfile, RunID: config.RunID, OwnedSessions: ownedDiagnosticSessionCount, Changes: ownedProbeChanges(t, false).Receipt(), Checks: ownedWorkspaceChecks}
	for _, check := range result.record.Checks {
		if check.Name == CheckArtifactExport {
			proof.StartedAt = check.StartedAt
			proof.FinishedAt = check.FinishedAt
		}
	}
	result.workspaceExport = &proof
	data, err := json.Marshal(LocalDiagnosticResult{result: result})
	if err != nil || !strings.Contains(string(data), ownedWorkspaceDiagnosticSchema) {
		t.Fatalf("owned evidence missing: %v", err)
	}
	if _, err := json.Marshal(result); err == nil {
		t.Fatal("local evidence serialized as certification")
	}
	for _, mutate := range []func(*Result){
		func(v *Result) { v.workspaceExportRequired = false },
		func(v *Result) { v.workspaceSourceRequired = false; v.workspaceSource = nil },
		func(v *Result) { v.workspaceExport = nil },
		func(v *Result) { v.workspaceExport.StartedAt = v.record.Run.StartedAt },
		func(v *Result) { v.workspaceExport.FinishedAt = v.record.Run.FinishedAt },
		func(v *Result) { v.workspaceExport.OwnedSessions-- },
	} {
		copy := result
		copyProof := proof
		copy.workspaceExport = &copyProof
		mutate(&copy)
		if _, err := json.Marshal(LocalDiagnosticResult{result: copy}); err == nil {
			t.Fatal("incomplete or misplaced export proof serialized")
		}
	}
}
func TestOwnedDiagnosticSelectionRejectsMissingSourceOrOwnerBeforeEffects(t *testing.T) {
	f := newLauncherFixture()
	config := f.config()
	config.workspaceExport = true
	if _, err := launch(context.Background(), config, f.dependencies()); err == nil || len(f.events) != 0 {
		t.Fatal("export reached effects without source and strict profile")
	}
	config.workspaceSource = true
	config.diagnosticModel = "selected-model"
	config.Provenance.WorkflowRunID = 0
	config.candidateImage = "sha256:" + strings.Repeat("a", 64)
	config.supervisorCandidateImage = "sha256:" + strings.Repeat("b", 64)
	config.policyProfile = RosettaRuntimePolicyProfile
	deps := f.dependencies()
	deps.checkWorkspace = func(context.Context, string, execution.Execution, launcherPorts) (*diagnosticWorkspaceSource, error) {
		t.Fatal("missing owner reached source")
		return nil, ErrCase
	}
	if _, err := launch(context.Background(), config, deps); err == nil || len(f.events) != 0 {
		t.Fatal("export reached effects without owner composition")
	}
	if _, err := prepareLauncherOwnedWorkspace(strings.Repeat("a", 32), execution.Execution{}, f.ports); err == nil {
		t.Fatal("unbound ports accepted")
	}
}
