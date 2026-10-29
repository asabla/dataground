package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/nativehost"
	"github.com/asabla/dataground/internal/workspace"
)

type completionStoreStub struct {
	*exportControllerFixture
	runtime                *runtimeStoreStub
	failIntent, failCommit bool
	intents                int
}

type completionRuntimeStore struct {
	*runtimeStoreStub
	exports             *exportControllerFixture
	failCompletionLease bool
}

func (s *completionRuntimeStore) RenewLease(ctx context.Context, claim persistence.OperationClaim, duration time.Duration) (persistence.OperationClaim, error) {
	if s.failCompletionLease && s.attempt.Status == "succeeded" {
		return persistence.OperationClaim{}, errors.New("lease unavailable")
	}
	renewed, err := s.runtimeStoreStub.RenewLease(ctx, claim, duration)
	s.exports.claim = renewed
	return renewed, err
}

func (s *completionStoreStub) BeginInvocationWorkspaceQuiescence(_ context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, ref execution.ExecutionRef, binding string) (persistence.InvocationWorkspaceQuiescenceEntry, error) {
	s.intents++
	if s.failIntent || (s.runtime.attempt.Status != "succeeded" && s.runtime.attempt.Status != "failed") {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	q := s.cleanup.Record
	q.OwnerBinding = binding
	if claim.ID != q.OperationID || effect.EffectID != q.EffectID || ref.ID != q.ExecutionID {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	s.cleanup.Record = q
	s.cleanup.ExpiresAt = time.Now().Add(time.Minute)
	return s.cleanup, nil
}
func (s *completionStoreStub) CompleteInvocationWorkspaceQuiescence(_ context.Context, _ persistence.OperationClaim, _ persistence.EffectRecord, q persistence.InvocationWorkspaceQuiescence) (persistence.InvocationWorkspaceQuiescenceEntry, error) {
	if q != s.cleanup.Record {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	s.cleanup.Confirmed = true
	if s.failCommit {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	return s.cleanup, nil
}

type completionProviderStub struct {
	*preparationProviderStub
	exports     *exportControllerFixture
	t           *testing.T
	runtime     *runtimeStoreStub
	acknowledge bool
	starts      int
	raw         *cleanupTransport
}

func (p *completionProviderStub) StartOwnedRuntime(ctx context.Context, ref execution.ExecutionRef) (*nativehost.Session, error) {
	if p.runtime.attempt.Status != "reserved" || ref != p.exports.ref {
		p.t.Fatal("owned startup did not follow exact reservation")
	}
	p.starts++
	session, raw := newCleanupSession(p.t, ctx, p.acknowledge)
	p.raw = raw
	return session, nil
}
func (p *completionProviderStub) ExportWorkspaceChanges(ctx context.Context, ref execution.ExecutionRef, base *workspace.Snapshot) (*workspace.ChangeSet, error) {
	return p.exports.ExportWorkspaceChanges(ctx, ref, base)
}

type completionFixture struct {
	driver   *InvocationRuntimeDriver
	p        *preparationFixture
	e        *exportControllerFixture
	store    *completionStoreStub
	runtime  *runtimeStoreStub
	provider *completionProviderStub
	adapter  *runtimeAdapterStub
}

func newCompletionFixture(t *testing.T, failed bool) *completionFixture {
	t.Helper()
	e := newExportControllerFixture(t)
	p := newPreparationFixture(t)
	p.claim, p.effect, p.ref = e.claim, e.effect, e.ref
	p.target.IsolationDomainID = e.claim.IsolationDomainID
	p.target.OperationID, p.target.InvocationID = e.claim.ID, e.claim.ResourceID
	p.target.ServiceID, p.target.RevisionID = e.exporter.config.Scope.ServiceID, e.exporter.config.Scope.RevisionID
	p.target.ActorID, p.target.CorrelationID = e.claim.ActorID, e.claim.CorrelationID
	p.p.config = e.exporter.config
	p.store.preparation.Execution = e.ref
	p.store.preparation.Source = e.exporter.config.Source
	p.store.preparation.PlanDigest = e.exporter.config.PlanDigest
	p.provider.observation = execution.Observation{IsolationDomainID: e.ref.IsolationDomainID, ExecutionID: e.ref.ID, State: "ready"}
	runtime := &runtimeStoreStub{target: p.target, sourcePrepared: true, attemptErr: persistence.ErrInvocationRuntimeAttemptMissing}
	e.cleanup.Confirmed = false
	store := &completionStoreStub{exportControllerFixture: e, runtime: runtime}
	provider := &completionProviderStub{preparationProviderStub: p.provider, exports: e, t: t, runtime: runtime, acknowledge: true}
	turn := &runtimeTurnStub{events: runtimeEvents(dgruntime.Event{Sequence: 1, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": "completed output", "phase": "final"}}, dgruntime.Event{Sequence: 2, Type: "lifecycle.succeeded"})}
	if failed {
		turn.events = runtimeEvents(dgruntime.Event{Sequence: 1, Type: "lifecycle.failed"})
		turn.waitErr = dgruntime.ErrTurnFailed
	}
	adapter := &runtimeAdapterStub{turn: turn}
	completion, err := NewInvocationWorkspaceCompletion(store, e.exporter)
	if err != nil {
		t.Fatal(err)
	}
	base := newRuntimeDriverForTest(t, runtime, &runtimeAuthorizerStub{}, &runtimeExecutionSourceStub{value: execution.Execution{IsolationDomainID: e.ref.IsolationDomainID, ID: e.ref.ID, State: "ready"}}, provider, &runtimeAdapterFactoryStub{adapter: adapter})
	driver, err := NewInvocationRuntimeDriver(&completionRuntimeStore{runtimeStoreStub: runtime, exports: e}, base.authorizer, base.requests, base.executions, provider, base.adapters, base.artifacts, InvocationRuntimeDriverConfig{WorkspacePreparer: p.p, WorkspaceCompletion: completion, Readiness: func(context.Context) error { return nil }, LeaseDuration: time.Minute, RenewInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return &completionFixture{driver: driver, p: p, e: e, store: store, runtime: runtime, provider: provider, adapter: adapter}
}

func TestOwnedInvocationRetainsOutcomeBeforeCleanupAndExport(t *testing.T) {
	for _, failed := range []bool{false, true} {
		f := newCompletionFixture(t, failed)
		_, err := f.driver.ApplyClaimed(context.Background(), f.p.claim, f.p.effect)
		if failed {
			if !errors.Is(err, ErrEffectTerminal) {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if f.provider.starts != 1 || f.provider.startCalls != 0 || !f.e.entry.Ready || !f.e.cleanup.Confirmed || f.provider.raw.requests.Load() != 1 {
			t.Fatal("incomplete owned lifecycle")
		}
		if f.runtime.beginCalls != 1 || f.adapter.startCalls != 1 {
			t.Fatal("repeated native execution")
		}
		originalCaptures := f.e.captures
		_, found, err := f.driver.ObserveClaimed(context.Background(), f.p.claim, f.p.effect)
		if failed {
			if found || !errors.Is(err, ErrEffectTerminal) {
				t.Fatal("failure recovery", err)
			}
		} else if !found || err != nil {
			t.Fatal("success recovery", err)
		}
		if f.provider.starts != 1 || f.e.captures != originalCaptures {
			t.Fatal("completed replay started a native process or recaptured")
		}
	}
}

func TestOwnedInvocationRetainsWorkspaceOnUnconfirmedCleanupOrExport(t *testing.T) {
	for _, failure := range []string{"intent", "acknowledgement", "cleanup-commit", "export-commit", "authority", "ready", "lease"} {
		t.Run(failure, func(t *testing.T) {
			f := newCompletionFixture(t, false)
			switch failure {
			case "intent":
				f.store.failIntent = true
			case "acknowledgement":
				f.provider.acknowledge = false
			case "cleanup-commit":
				f.store.failCommit = true
			case "export-commit":
				f.e.failComplete = true
			case "authority":
				f.e.denyAt = 1
			case "ready":
				f.driver.readiness = func(context.Context) error {
					if f.runtime.attempt.Status == "succeeded" {
						return errors.New("acceptance expired")
					}
					return nil
				}
			case "lease":
				f.driver.store.(*completionRuntimeStore).failCompletionLease = true
			}
			_, err := f.driver.ApplyClaimed(context.Background(), f.p.claim, f.p.effect)
			if !errors.Is(err, ErrAmbiguousEffect) || errors.Is(err, ErrEffectTerminal) || f.runtime.attempt.Status != "succeeded" {
				t.Fatalf("outcome lost or prematurely completed: %v", err)
			}
			f.store.failIntent = false
			f.store.failCommit = false
			f.e.failComplete = false
			f.e.denyAt = 0
			f.e.denied = false
			f.driver.readiness = func(context.Context) error { return nil }
			f.driver.store.(*completionRuntimeStore).failCompletionLease = false
			// A replacement has no reference to the original owner's live session.
			replacement := *f.driver
			provider := *f.provider
			provider.starts, provider.raw = 0, nil
			replacement.provider = &provider
			claim := f.p.claim
			claim.LeaseOwner, claim.FencingToken = "replacement", claim.FencingToken+1
			_, found, err := replacement.ObserveClaimed(context.Background(), claim, f.p.effect)
			if failure == "intent" || failure == "acknowledgement" || failure == "ready" || failure == "lease" {
				if found || !errors.Is(err, ErrAmbiguousEffect) || f.e.captures != 0 {
					t.Fatal("unconfirmed owner recovered", err)
				}
			} else if !found || err != nil || !f.e.entry.Ready {
				t.Fatal("retention could not recover", err)
			}
			if f.provider.starts != 1 || provider.starts != 0 || f.adapter.startCalls != 1 {
				t.Fatal("recovery repeated native execution")
			}
		})
	}
}

func TestOwnedInvocationRejectsUnmatchedCompletionConfiguration(t *testing.T) {
	f := newCompletionFixture(t, false)
	for _, change := range []string{"preparer", "provider", "source", "store"} {
		completion := *f.driver.workspaceCompletion
		exporter := *completion.exporter
		completion.exporter = &exporter
		config := InvocationRuntimeDriverConfig{WorkspaceCompletion: &completion, WorkspacePreparer: f.p.p, Readiness: func(context.Context) error { return nil }, LeaseDuration: time.Minute, RenewInterval: time.Second}
		var provider invocationRuntimeProvider = f.provider
		switch change {
		case "preparer":
			config.WorkspacePreparer = nil
		case "provider":
			provider = f.p.provider
		case "source":
			exporter.config.Source.ID = "src_bbbbbbbbbbbbbbbbbbbb"
		case "store":
			completion.store = nil
		}
		if _, err := NewInvocationRuntimeDriver(f.runtime, f.driver.authorizer, f.driver.requests, f.driver.executions, provider, f.driver.adapters, f.driver.artifacts, config); err == nil {
			t.Fatal("incomplete configuration accepted", change)
		}
	}
}

type completionArtifactFinalizer struct {
	store *runtimeStoreStub
	calls int
}

func (f *completionArtifactFinalizer) Finalize(_ context.Context, value artifact.Finalization) (artifact.Record, error) {
	f.calls++
	// The durable artifact catalog only accepts ordinary artifact publication
	// under the original reserved attempt, before its result is committed.
	if f.store.attempt.Status != "reserved" || value.Binding.LeaseOwner != f.store.attempt.LeaseOwner || value.Binding.FencingToken != f.store.attempt.FencingToken {
		return artifact.Record{}, errors.New("artifact publication lost its original reservation")
	}
	return value.Binding.Record, nil
}

func TestOwnedInvocationRetainsDeclaredArtifactsBeforeOutcomeAndDoesNotRepublish(t *testing.T) {
	f := newCompletionFixture(t, false)
	builder := f.driver.requests
	f.driver.requests = InvocationRuntimeRequestBuilderFunc(func(target persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error) {
		request, err := builder.BuildInvocationRuntimeRequest(target)
		request.SandboxMode = dgruntime.SandboxWorkspaceWrite
		request.Artifacts = []dgruntime.ArtifactDeclaration{{ID: "report", Name: "Report", SandboxPath: "/workspace/report.json", MediaType: "application/json", Kind: "file"}}
		return request, err
	})
	finalizer := &completionArtifactFinalizer{store: f.runtime}
	f.driver.artifacts = finalizer
	f.e.failComplete = true
	_, err := f.driver.ApplyClaimed(context.Background(), f.p.claim, f.p.effect)
	if !errors.Is(err, ErrAmbiguousEffect) || f.runtime.attempt.Status != "succeeded" || finalizer.calls != 1 || len(f.provider.runtimeProviderStub.exports) != 1 {
		t.Fatalf("declared artifact or outcome was not retained before export failure: %v", err)
	}
	f.e.failComplete = false
	claim := f.p.claim
	claim.LeaseOwner, claim.FencingToken = "replacement", claim.FencingToken+1
	_, found, err := f.driver.ObserveClaimed(context.Background(), claim, f.p.effect)
	if err != nil || !found || finalizer.calls != 1 || f.provider.starts != 1 || len(f.provider.runtimeProviderStub.exports) != 1 {
		t.Fatalf("replacement repeated artifact publication or native execution: %v", err)
	}
}

func TestOwnedInvocationRetainsInvalidOutputBeforeWorkspaceExport(t *testing.T) {
	f := newCompletionFixture(t, false)
	f.runtime.target.OutputSchema = map[string]any{"type": "object", "required": []any{"answer"}}
	_, err := f.driver.ApplyClaimed(context.Background(), f.p.claim, f.p.effect)
	if !errors.Is(err, ErrEffectTerminal) || !errors.Is(err, ErrInvocationRuntimeOutputInvalid) || !f.e.entry.Ready || !f.e.cleanup.Confirmed {
		t.Fatalf("invalid output did not retain workspace: %v", err)
	}
	finalizer := f.driver.artifacts.(*runtimeArtifactFinalizerStub)
	if len(finalizer.values) != 1 || string(finalizer.values[0].Content) != "completed output" {
		t.Fatal("invalid output content was not retained")
	}
}

func TestOwnedInvocationRecoveryRejectsForeignExecutionAndCancelledContext(t *testing.T) {
	for _, failure := range []string{"scope", "execution", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			f := newCompletionFixture(t, false)
			f.e.failComplete = true
			if _, err := f.driver.ApplyClaimed(context.Background(), f.p.claim, f.p.effect); !errors.Is(err, ErrAmbiguousEffect) {
				t.Fatal(err)
			}
			f.e.failComplete = false
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			source := f.driver.executions.(*runtimeExecutionSourceStub)
			switch failure {
			case "scope":
				source.value.IsolationDomainID = "iso_foreign"
			case "execution":
				source.value.ID = "exe_foreign"
			case "cancelled":
				cancel()
			}
			captures := f.e.captures
			_, found, err := f.driver.ObserveClaimed(ctx, f.p.claim, f.p.effect)
			want := ErrAmbiguousEffect
			if failure == "cancelled" {
				want = context.Canceled
			}
			if found || !errors.Is(err, want) || f.e.entry.Ready || f.e.captures != captures || f.provider.starts != 1 {
				t.Fatalf("unsafe recovery completed: %v", err)
			}
		})
	}
}
