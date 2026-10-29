package runtimeevidence

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"sync"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/workspace"
)

const ownedWorkspaceDiagnosticSchema = "dataground.dev.openshell-runtime-diagnostic/v7"
const ownedWorkspaceDiagnosticProfile = "openshell-owned-workspace-diagnostic/v1"
const ownedDiagnosticSessionCount = 11 // Source, eight model cases, artifact production, and the detached-writer probe.
var ownedWorkspaceChecks = [7]string{"owned-model-sessions", "terminal-owner-acknowledgements", "detached-writer-observed", "binary-change-export", "exact-repeated-export", "restored-tree", "cross-domain-denial"}

type diagnosticWorkspaceExport struct {
	Profile       string                     `json:"profile"`
	RunID         string                     `json:"runId"`
	StartedAt     string                     `json:"startedAt"`
	FinishedAt    string                     `json:"finishedAt"`
	OwnedSessions int                        `json:"ownedSessions"`
	Changes       workspace.ChangeSetReceipt `json:"changes"`
	Checks        [7]string                  `json:"checks"`
}

func validOwnedWorkspaceDiagnostic(value *diagnosticWorkspaceExport, runID string) bool {
	if value == nil || value.Profile != ownedWorkspaceDiagnosticProfile || value.RunID != runID || !runIDPattern.MatchString(runID) || value.OwnedSessions != ownedDiagnosticSessionCount || value.Checks != ownedWorkspaceChecks {
		return false
	}
	start, a := time.Parse(time.RFC3339Nano, value.StartedAt)
	end, b := time.Parse(time.RFC3339Nano, value.FinishedAt)
	base, err := workspaceDiagnosticSource(false)
	r := value.Changes
	return a == nil && b == nil && end.After(start) && end.Sub(start) <= 2*time.Minute && err == nil && r.SchemaVersion == workspace.ChangeSetContract && r.BaseArchiveDigest == base.Receipt().ArchiveDigest && commitmentPattern.MatchString(r.Digest) && commitmentPattern.MatchString(r.ResultArchiveDigest) && r.SizeBytes > 0 && r.SizeBytes <= 64<<10 && r.ChangeCount == 5 && r.EntryCount == 6 && r.SourceBytes == 15
}

type ownedDiagnosticSession interface {
	execution.RuntimeSession
	OwnerBinding() (string, error)
	ConfirmedOwnerBinding() (string, error)
}
type ownedDiagnosticBackend interface {
	HarnessProvider
	MaterializeWorkspace(context.Context, execution.ExecutionRef, *workspace.Snapshot) (workspace.Receipt, error)
	ExportWorkspaceChanges(context.Context, execution.ExecutionRef, *workspace.Snapshot) (*workspace.ChangeSet, error)
}

// This launcher owns a disposable synthetic sandbox. Every native session uses
// the owner transport; unconfirmed cleanup seals the run. This is test ownership,
// not a production writer fence or permission to destroy user workspace data.
type ownedDiagnosticProvider struct {
	backend                   ownedDiagnosticBackend
	open                      func(context.Context, execution.ExecutionRef) (ownedDiagnosticSession, error)
	ref                       execution.ExecutionRef
	runID                     string
	mu                        sync.Mutex
	active, failed, exporting bool
	starts, closed            int
	evidence                  *diagnosticWorkspaceExport
	bindings                  map[string]bool
}

func (p *ownedDiagnosticProvider) StartRuntime(ctx context.Context, ref execution.ExecutionRef) (execution.RuntimeSession, error) {
	return p.start(ctx, ref, false)
}
func (p *ownedDiagnosticProvider) start(ctx context.Context, ref execution.ExecutionRef, exporting bool) (execution.RuntimeSession, error) {
	p.mu.Lock()
	if ctx == nil || ctx.Err() != nil || ref != p.ref || p.failed || p.active || p.evidence != nil || p.exporting != exporting || p.starts >= ownedDiagnosticSessionCount {
		p.mu.Unlock()
		return nil, ErrCase
	}
	p.active = true
	p.starts++
	p.mu.Unlock()
	session, err := p.open(ctx, ref)
	if err != nil || isNilHarnessPort(session) {
		p.fail()
		if !isNilHarnessPort(session) {
			_ = session.Close()
		}
		return nil, ErrCase
	}
	binding, err := session.OwnerBinding()
	if err != nil || !commitmentPattern.MatchString(binding) || ctx.Err() != nil {
		p.fail()
		_ = session.Close()
		return nil, ErrCase
	}
	p.mu.Lock()
	if p.bindings == nil {
		p.bindings = make(map[string]bool)
	}
	repeated := p.bindings[binding]
	p.bindings[binding] = true
	if repeated {
		p.failed = true
	}
	p.mu.Unlock()
	if repeated {
		_ = session.Close()
		return nil, ErrCase
	}
	return &ownedDiagnosticTrackedSession{ownedDiagnosticSession: session, provider: p, binding: binding}, nil
}
func (p *ownedDiagnosticProvider) fail() { p.mu.Lock(); p.failed = true; p.mu.Unlock() }
func (p *ownedDiagnosticProvider) MaterializeWorkspace(ctx context.Context, ref execution.ExecutionRef, base *workspace.Snapshot) (workspace.Receipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed || p.starts != 0 || p.exporting || p.evidence != nil {
		return workspace.Receipt{}, ErrCase
	}
	return p.backend.MaterializeWorkspace(ctx, ref, base)
}
func (p *ownedDiagnosticProvider) Observe(ctx context.Context, ref execution.ExecutionRef) (execution.Observation, error) {
	return p.backend.Observe(ctx, ref)
}
func (p *ownedDiagnosticProvider) Terminate(ctx context.Context, ref execution.ExecutionRef) error {
	p.mu.Lock()
	ready := !p.failed && !p.active && !p.exporting && p.evidence != nil && p.closed == ownedDiagnosticSessionCount && ref == p.ref
	p.mu.Unlock()
	if !ready {
		return ErrCase
	}
	return p.backend.Terminate(ctx, ref)
}
func (p *ownedDiagnosticProvider) Export(ctx context.Context, request execution.ExportRequest) (execution.ExportResult, error) {
	p.mu.Lock()
	if ctx == nil || ctx.Err() != nil || request.IsolationDomainID != p.ref.IsolationDomainID || request.ExecutionID != p.ref.ID || request.SandboxPath != runtimeArtifactPath(p.runID) || p.failed || p.active || p.exporting || p.evidence != nil || p.starts != ownedDiagnosticSessionCount-1 || p.closed != p.starts {
		p.mu.Unlock()
		return execution.ExportResult{}, ErrCase
	}
	p.exporting = true
	p.mu.Unlock()
	evidence, err := p.capture(ctx)
	if err != nil {
		p.fail()
		return execution.ExportResult{}, ErrCase
	}
	result, err := p.backend.Export(ctx, request)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil || ctx.Err() != nil || p.failed || p.active || p.closed != ownedDiagnosticSessionCount {
		p.failed = true
		clear(result.Content)
		return execution.ExportResult{}, ErrCase
	}
	p.evidence = evidence
	p.exporting = false
	return result, nil
}
func (p *ownedDiagnosticProvider) Evidence() *diagnosticWorkspaceExport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failed || p.active || p.exporting || p.evidence == nil {
		return nil
	}
	copied := *p.evidence
	return &copied
}

type ownedDiagnosticTrackedSession struct {
	ownedDiagnosticSession
	provider *ownedDiagnosticProvider
	binding  string
	once     sync.Once
	err      error
}

func (s *ownedDiagnosticTrackedSession) Close() error {
	s.once.Do(func() {
		err := s.ownedDiagnosticSession.Close()
		confirmed, proofErr := s.ConfirmedOwnerBinding()
		p := s.provider
		p.mu.Lock()
		defer p.mu.Unlock()
		if err != nil || proofErr != nil || confirmed != s.binding {
			p.failed = true
			s.err = ErrCase
			return
		}
		p.closed++
		p.active = false
	})
	return s.err
}

const ownedWorkspaceWriter = `import os,time
os.umask(0o077)
with open('src/data.bin','wb') as f: f.write(bytes([255,0,42,10]))
with open('new.bin','xb') as f: f.write(bytes([0,128,255]))
os.mkdir('empty',0o700)
os.unlink('run.sh')
child=os.fork()
if child==0:
 os.setsid()
 null=os.open('/dev/null',os.O_RDWR)
 for target in (0,1,2): os.dup2(null,target)
 if null>2: os.close(null)
 fd=os.open('writer.bin',os.O_CREAT|os.O_EXCL|os.O_RDWR,0o600)
 end=time.monotonic()+30
 counter=1
 while time.monotonic()<end:
  os.pwrite(fd,counter.to_bytes(8,'big'),0)
  counter+=1
  time.sleep(0.005)
 os.close(fd)
 os._exit(0)
end=time.monotonic()+2
while not os.path.exists('writer.bin') and time.monotonic()<end: time.sleep(0.005)
assert os.path.exists('writer.bin')`
const ownedWorkspaceWriterObservation = `import time
with open('writer.bin','rb') as f: before=f.read()
end=time.monotonic()+2
changed=False
while time.monotonic()<end:
 time.sleep(0.01)
 with open('writer.bin','rb') as f: after=f.read()
 if len(after)==8 and after!=before: changed=True;break
assert changed`

func (p *ownedDiagnosticProvider) capture(ctx context.Context) (*diagnosticWorkspaceExport, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	started := time.Now().UTC()
	session, err := p.start(ctx, p.ref, true)
	if err != nil {
		return nil, ErrCase
	}
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	defer session.Close()
	diagnostics := make(chan int64, 1)
	go func() {
		n, _ := io.CopyN(io.Discard, session.Errors(), (64<<10)+1)
		if n > 64<<10 {
			_ = session.Close()
		}
		diagnostics <- n
	}()
	command, err := workspaceNativeCommand(session, map[string]any{"type": "workspaceWrite", "writableRoots": []string{"/sandbox/workspace"}, "networkAccess": false, "excludeTmpdirEnvVar": true, "excludeSlashTmp": true})
	if err != nil {
		return nil, ErrCase
	}
	for _, script := range []string{ownedWorkspaceWriter, ownedWorkspaceWriterObservation} {
		if output, err := command([]string{"python3", "-c", script}); err != nil || output != "" {
			return nil, ErrCase
		}
	}
	if session.Close() != nil || <-diagnostics > 64<<10 || ctx.Err() != nil {
		return nil, ErrCase
	}
	base, err := workspaceDiagnosticSource(false)
	if err != nil {
		return nil, ErrCase
	}
	wrong := p.ref
	wrong.IsolationDomainID = runtimeIsolationDomain(p.runID + ":other")
	if _, err := p.backend.ExportWorkspaceChanges(ctx, wrong, base); err == nil {
		return nil, ErrCase
	}
	changes, err := p.backend.ExportWorkspaceChanges(ctx, p.ref, base)
	if err != nil || changes == nil || changes.Receipt().SizeBytes > 64<<10 {
		return nil, ErrCase
	}
	var first bytes.Buffer
	if changes.Write(ctx, &first) != nil {
		return nil, ErrCase
	}
	defer clear(first.Bytes())
	restored, err := workspace.ApplyChanges(base, first.Bytes(), changes.Receipt().Digest)
	if err != nil || !validOwnedFixtureTree(ctx, restored) {
		return nil, ErrCase
	}
	repeated, err := p.backend.ExportWorkspaceChanges(ctx, p.ref, base)
	if err != nil || repeated == nil || repeated.Receipt() != changes.Receipt() {
		return nil, ErrCase
	}
	var second bytes.Buffer
	if repeated.Write(ctx, &second) != nil {
		return nil, ErrCase
	}
	defer clear(second.Bytes())
	if !bytes.Equal(first.Bytes(), second.Bytes()) || ctx.Err() != nil {
		return nil, ErrCase
	}
	evidence := &diagnosticWorkspaceExport{Profile: ownedWorkspaceDiagnosticProfile, RunID: p.runID, StartedAt: started.Format(time.RFC3339Nano), FinishedAt: time.Now().UTC().Format(time.RFC3339Nano), OwnedSessions: ownedDiagnosticSessionCount, Changes: changes.Receipt(), Checks: ownedWorkspaceChecks}
	if !validOwnedWorkspaceDiagnostic(evidence, p.runID) {
		return nil, ErrCase
	}
	return evidence, nil
}
func validOwnedFixtureTree(ctx context.Context, value *workspace.Snapshot) bool {
	if value == nil || value.Receipt().EntryCount != 6 || value.Receipt().SourceBytes != 15 {
		return false
	}
	var archive bytes.Buffer
	if value.WriteArchive(ctx, &archive) != nil {
		return false
	}
	defer clear(archive.Bytes())
	reader := tar.NewReader(&archive)
	seen := map[string]bool{}
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil || seen[h.Name] {
			return false
		}
		seen[h.Name] = true
		data, err := io.ReadAll(io.LimitReader(reader, 16))
		if err != nil {
			return false
		}
		switch h.Name {
		case "src", "empty":
			if h.Typeflag != tar.TypeDir || len(data) != 0 {
				return false
			}
		case "data-link":
			if h.Typeflag != tar.TypeSymlink || h.Linkname != "src/data.bin" {
				return false
			}
		case "src/data.bin":
			if h.Typeflag != tar.TypeReg || !bytes.Equal(data, []byte{255, 0, 42, 10}) {
				return false
			}
		case "new.bin":
			if h.Typeflag != tar.TypeReg || !bytes.Equal(data, []byte{0, 128, 255}) {
				return false
			}
		case "writer.bin":
			if h.Typeflag != tar.TypeReg || len(data) != 8 || binary.BigEndian.Uint64(data) == 0 {
				return false
			}
		default:
			return false
		}
	}
	return len(seen) == 6 && ctx.Err() == nil
}
