package reconcile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
)

type exportControllerFixture struct {
	expireObjectRead                                                          bool
	exporter                                                                  *InvocationWorkspaceExporter
	claim                                                                     persistence.OperationClaim
	effect                                                                    persistence.EffectRecord
	ref                                                                       execution.ExecutionRef
	cleanup                                                                   persistence.InvocationWorkspaceQuiescenceEntry
	base                                                                      *workspace.Snapshot
	changes                                                                   *workspace.ChangeSet
	entry                                                                     workspace.ChangeExportEntry
	object                                                                    []byte
	expires                                                                   time.Time
	authorizations, sourceReads, captures, reads, writes, completions, checks int
	denyAt                                                                    int
	failComplete                                                              bool
	loseWrite                                                                 bool
	denied                                                                    bool
	permitChange                                                              func(*persistence.WorkspaceChangeExportPermit)
	captureChange                                                             func(context.Context)
	sourceChange                                                              func()
	checkFailure                                                              int
	deadline                                                                  time.Time
}

func newExportControllerFixture(t *testing.T) *exportControllerFixture {
	t.Helper()
	p := newPreparationFixture(t)
	config := p.p.config
	config.Scope.ServiceID, config.Scope.RevisionID = identity.New("svc"), identity.New("rev")
	claim := p.claim
	claim.ID = identity.New("op")
	claim.ResourceID = identity.New("inv")
	claim.ActorID = "operator"
	claim.CorrelationID = identity.New("cor")
	claim.StateMachineVersion = 2
	effect := p.effect
	effect.OperationID = claim.ID
	effect.EffectID = identity.Derived("eff", claim.IsolationDomainID+":"+claim.Kind+":"+claim.ID+":run-invocation")
	ref := execution.ExecutionRef{IsolationDomainID: claim.IsolationDomainID, ID: identity.New("exe")}
	changes, err := workspace.Compare(p.store.snapshot, p.store.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	f := &exportControllerFixture{claim: claim, effect: effect, ref: ref, base: p.store.snapshot, changes: changes, expires: time.Now().Add(30 * time.Second)}
	f.cleanup = persistence.InvocationWorkspaceQuiescenceEntry{Confirmed: true, Record: persistence.InvocationWorkspaceQuiescence{SchemaVersion: persistence.WorkspaceQuiescenceContract, Source: config.Source, InvocationID: claim.ResourceID, OperationID: claim.ID, EffectID: effect.EffectID, ExecutionID: ref.ID, PlanDigest: config.PlanDigest, OwnerBinding: "sha256:" + strings.Repeat("d", 64), RuntimeOwner: claim.LeaseOwner, RuntimeToken: claim.FencingToken, ActorID: claim.ActorID, CorrelationID: claim.CorrelationID}}
	f.exporter, err = NewInvocationWorkspaceExporter(f, &preparationObjectsStub{config.Source.StorageProfileDigest}, f, f, config)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *exportControllerFixture) run(ctx context.Context) (workspace.ChangeExportEntry, error) {
	return f.exporter.Export(ctx, f.claim, f.effect, f.ref, f, func(context.Context) error {
		f.checks++
		if f.checks == f.checkFailure {
			return errors.New("image or writer fence unavailable")
		}
		return nil
	})
}
func (f *exportControllerFixture) GetInvocationWorkspaceQuiescence(context.Context, string, string) (persistence.InvocationWorkspaceQuiescenceEntry, error) {
	return f.cleanup, nil
}
func (f *exportControllerFixture) AuthorizeInvocationWorkspaceChangeExport(_ context.Context, c persistence.OperationClaim, e persistence.EffectRecord, q persistence.InvocationWorkspaceQuiescence, phase string, changes workspace.ChangeSetReceipt, eval persistence.WorkspaceChangeExportEvaluator) (persistence.WorkspaceChangeExportPermit, error) {
	f.authorizations++
	if f.authorizations == f.denyAt {
		f.denied = true
		return persistence.WorkspaceChangeExportPermit{}, persistence.ErrWorkspaceExportDenied
	}
	if c != f.claim || !reflect.DeepEqual(e, f.effect) || q != f.cleanup.Record || eval == nil {
		return persistence.WorkspaceChangeExportPermit{}, persistence.ErrWorkspaceExportDenied
	}
	scope := f.exporter.config.Scope
	r := authz.WorkspaceChangeExportRequest{Source: q.Source, ServiceID: scope.ServiceID, RevisionID: scope.RevisionID, InvocationID: q.InvocationID, OperationID: q.OperationID, EffectID: q.EffectID, ExecutionID: q.ExecutionID, ActorID: c.ActorID, CorrelationID: c.CorrelationID, PlanDigest: q.PlanDigest, OwnerBinding: q.OwnerBinding, RuntimeToken: q.RuntimeToken, FencingToken: c.FencingToken, Phase: phase, Changes: changes}
	permit := persistence.WorkspaceChangeExportPermit{DecisionID: identity.New("wed"), ExpiresAt: f.expires, Request: r}
	if f.permitChange != nil {
		f.permitChange(&permit)
	}
	return permit, nil
}
func (f *exportControllerFixture) ReadExportWorkspaceSource(ctx context.Context, _ persistence.OperationClaim, _ persistence.EffectRecord, _ workspace.SourceObjectReader, _ persistence.InvocationWorkspaceQuiescence, _ int64, e persistence.SourceReadEvaluator) (*workspace.Snapshot, time.Time, error) {
	if f.denied || ctx.Err() != nil || e == nil {
		return nil, time.Time{}, workspace.ErrSourceUnauthorized
	}
	f.sourceReads++
	if f.sourceChange != nil {
		f.sourceChange()
	}
	return f.base, f.expires, nil
}
func (f *exportControllerFixture) ExportWorkspaceChanges(ctx context.Context, _ execution.ExecutionRef, _ *workspace.Snapshot) (*workspace.ChangeSet, error) {
	if f.denied {
		return nil, persistence.ErrWorkspaceExportDenied
	}
	f.captures++
	f.deadline, _ = ctx.Deadline()
	if f.captureChange != nil {
		f.captureChange(ctx)
	}
	return f.changes, nil
}
func (f *exportControllerFixture) PrepareWorkspaceChangeExport(_ context.Context, b workspace.ChangeExportBinding) (workspace.ChangeExportEntry, error) {
	if f.denied {
		return workspace.ChangeExportEntry{}, persistence.ErrWorkspaceExportDenied
	}
	if f.entry.Record.SchemaVersion != "" && f.entry.Record != b.Record {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	f.entry.Record = b.Record
	return f.entry, nil
}
func (f *exportControllerFixture) CompleteWorkspaceChangeExport(_ context.Context, b workspace.ChangeExportBinding) (workspace.ChangeExportEntry, error) {
	if f.denied || f.failComplete {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportUnavailable
	}
	if f.entry.Record != b.Record {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	f.completions++
	f.entry.Ready = true
	return f.entry, nil
}
func (f *exportControllerFixture) GetWorkspaceChangeExport(context.Context, string, string) (workspace.ChangeExportEntry, error) {
	if f.entry.Record.SchemaVersion == "" {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportMissing
	}
	return f.entry, nil
}
func (f *exportControllerFixture) OpenInvocationArtifactObject(ctx context.Context, _ string) (io.ReadCloser, error) {
	if f.expireObjectRead {
		<-ctx.Done()
		return nil, artifact.ErrInvocationArtifactObjectMissing
	}
	if f.denied {
		return nil, persistence.ErrWorkspaceExportDenied
	}
	f.reads++
	if f.object == nil {
		return nil, artifact.ErrInvocationArtifactObjectMissing
	}
	return io.NopCloser(bytes.NewReader(f.object)), nil
}
func (f *exportControllerFixture) PutInvocationArtifactObjectIfAbsent(_ context.Context, _ string, r io.Reader, _ int64, _, _ string) error {
	if f.denied {
		return persistence.ErrWorkspaceExportDenied
	}
	f.writes++
	if f.object != nil {
		return artifact.ErrInvocationArtifactObjectConflict
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.object = b
	if f.loseWrite {
		return errors.New("lost acknowledgement")
	}
	return nil
}
func TestWorkspaceExportControllerRetainsAndRecoversWithoutAnotherCapture(t *testing.T) {
	f := newExportControllerFixture(t)
	f.failComplete = true
	f.loseWrite = true
	if _, err := f.run(context.Background()); err == nil || f.entry.Ready || f.writes != 1 || f.captures != 1 {
		t.Fatal("pending retention", err)
	}
	f.failComplete = false
	f.claim.FencingToken++
	f.claim.LeaseOwner = "replacement"
	completed, err := f.run(context.Background())
	if err != nil || !completed.Ready || f.writes != 1 || f.captures != 1 {
		t.Fatal("recovery", err)
	}
	if _, err := f.run(context.Background()); err != nil || f.writes != 1 || f.captures != 1 {
		t.Fatal("historical object replay", err)
	}
	f.object = nil
	if _, err := f.run(context.Background()); err == nil || f.writes != 1 || f.captures != 1 {
		t.Fatal("completed object recreated", err)
	}
}
func TestWorkspaceExportControllerRequiresEveryFreshAuthorityCheck(t *testing.T) {
	success := newExportControllerFixture(t)
	if _, err := success.run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= success.authorizations; i++ {
		t.Run("decision-"+strconv.Itoa(i), func(t *testing.T) {
			f := newExportControllerFixture(t)
			f.denyAt = i
			if _, err := f.run(context.Background()); err == nil {
				t.Fatal("authorization denial returned success")
			}
			if f.authorizations != i {
				t.Fatal("continued authority checks after denial", f.authorizations, i)
			}
		})
	}
	for i := 1; i <= success.checks; i++ {
		f := newExportControllerFixture(t)
		f.checkFailure = i
		if _, err := f.run(context.Background()); err == nil {
			t.Fatal("accepted image or writer fence failure ignored", i)
		}
	}
}
func TestWorkspaceExportControllerRejectsSubstitutionAndAmbiguity(t *testing.T) {
	for name, change := range map[string]func(*exportControllerFixture){
		"pending cleanup":   func(f *exportControllerFixture) { f.cleanup.Confirmed = false },
		"cleanup execution": func(f *exportControllerFixture) { f.cleanup.Record.ExecutionID = identity.New("exe") },
		"cleanup source":    func(f *exportControllerFixture) { f.cleanup.Record.Source.ID = identity.New("src") },
		"cleanup plan":      func(f *exportControllerFixture) { f.cleanup.Record.PlanDigest = "sha256:" + strings.Repeat("f", 64) },
		"capture nil":       func(f *exportControllerFixture) { f.changes = nil },
		"source nil":        func(f *exportControllerFixture) { f.base = nil },
		"expired":           func(f *exportControllerFixture) { f.expires = time.Now().Add(-time.Second) },
		"permit scope": func(f *exportControllerFixture) {
			f.permitChange = func(p *persistence.WorkspaceChangeExportPermit) {
				p.Request.Source.IsolationDomainID = identity.New("iso")
			}
		},
		"permit receipt": func(f *exportControllerFixture) {
			f.permitChange = func(p *persistence.WorkspaceChangeExportPermit) {
				p.Request.Changes.Digest = "sha256:" + strings.Repeat("f", 64)
			}
		},
		"permit owner": func(f *exportControllerFixture) {
			f.permitChange = func(p *persistence.WorkspaceChangeExportPermit) {
				p.Request.OwnerBinding = "sha256:" + strings.Repeat("f", 64)
			}
		},
		"permit token": func(f *exportControllerFixture) {
			f.permitChange = func(p *persistence.WorkspaceChangeExportPermit) { p.Request.FencingToken++ }
		},
		"missing decision": func(f *exportControllerFixture) {
			f.permitChange = func(p *persistence.WorkspaceChangeExportPermit) { p.DecisionID = "" }
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newExportControllerFixture(t)
			change(f)
			if _, err := f.run(context.Background()); err == nil || f.entry.Ready {
				t.Fatal("unsafe export succeeded", err)
			}
			if f.writes != 0 {
				t.Fatal("unsafe export wrote object")
			}
		})
	}
}
func TestWorkspaceExportControllerBoundsCaptureByInitialReadWindow(t *testing.T) {
	f := newExportControllerFixture(t)
	initial := f.expires
	f.sourceChange = func() { f.expires = f.expires.Add(time.Hour) }
	if _, err := f.run(context.Background()); err != nil || !f.deadline.Equal(initial) {
		t.Fatal("renewal widened prior authority", f.deadline, initial, err)
	}
	f = newExportControllerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.captureChange = func(context.Context) { cancel() }
	if _, err := f.run(ctx); err == nil || f.writes != 0 {
		t.Fatal("capture cancellation retained output", err)
	}
}
func TestWorkspaceExportControllerRejectsPendingDriftAndCorruption(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		f := newExportControllerFixture(t)
		f.failComplete = true
		if _, err := f.run(context.Background()); err == nil {
			t.Fatal("expected pending completion")
		}
		f.failComplete = false
		if corrupt {
			f.object[0] ^= 1
		} else {
			f.object = nil
			f.changes = nil
		}
		if _, err := f.run(context.Background()); err == nil || f.writes != 1 || f.entry.Ready {
			t.Fatal("ambiguous pending output retained", err)
		}
	}
}

func TestWorkspaceExportControllerDoesNotRecoverPastObjectReadPermitExpiry(t *testing.T) {
	f := newExportControllerFixture(t)
	f.expireObjectRead = true
	f.permitChange = func(p *persistence.WorkspaceChangeExportPermit) {
		if p.Request.Phase == "retain" {
			p.ExpiresAt = time.Now().Add(10 * time.Millisecond)
		}
	}
	if _, err := f.run(context.Background()); err == nil || f.writes != 0 || f.entry.Ready {
		t.Fatal("expired object observation permitted a later write", err)
	}
}
