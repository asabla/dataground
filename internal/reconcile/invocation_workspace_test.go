package reconcile

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/workspace"
)

type preparationStoreStub struct {
	preparation                    persistence.InvocationWorkspacePreparation
	snapshot                       *workspace.Snapshot
	expires                        time.Time
	events                         *[]string
	beginErr, readErr, completeErr error
	deliveryErr                    error
	deliveryExpires                time.Time
	afterRead                      func()
}

func (s *preparationStoreStub) BeginInvocationWorkspacePreparation(context.Context, persistence.OperationClaim, persistence.EffectRecord, execution.ExecutionRef, int64) (persistence.InvocationWorkspacePreparation, error) {
	*s.events = append(*s.events, "begin")
	return s.preparation, s.beginErr
}
func (s *preparationStoreStub) ReadPreparingWorkspaceSource(_ context.Context, _ persistence.OperationClaim, _ persistence.EffectRecord, _ workspace.SourceObjectReader, _ persistence.InvocationWorkspacePreparation, e persistence.SourceReadEvaluator) (*workspace.Snapshot, time.Time, error) {
	if _, ok := e.(*SourceCedarEvaluator); !ok {
		panic("non-Cedar source evaluator")
	}
	*s.events = append(*s.events, "read")
	if s.afterRead != nil {
		s.afterRead()
	}
	return s.snapshot, s.expires, s.readErr
}
func (s *preparationStoreStub) AuthorizeInvocationWorkspaceTransfer(_ context.Context, _ persistence.OperationClaim, _ persistence.EffectRecord, _ persistence.InvocationWorkspacePreparation, e persistence.SourceReadEvaluator) (time.Time, error) {
	if _, ok := e.(*SourceCedarEvaluator); !ok {
		panic("non-Cedar delivery evaluator")
	}
	*s.events = append(*s.events, "delivery")
	if s.deliveryExpires.IsZero() {
		return s.expires, s.deliveryErr
	}
	return s.deliveryExpires, s.deliveryErr
}
func (s *preparationStoreStub) CompleteInvocationWorkspacePreparation(ctx context.Context, _ persistence.OperationClaim, _ persistence.EffectRecord, _ persistence.InvocationWorkspacePreparation, _ workspace.Receipt, e persistence.SourceReadEvaluator) error {
	if _, ok := e.(*SourceCedarEvaluator); !ok {
		panic("non-Cedar source evaluator")
	}
	*s.events = append(*s.events, "complete")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return s.completeErr
}

type preparationObjectsStub struct{ digest string }

func (s *preparationObjectsStub) WorkspaceSourceStorageDigest() string { return s.digest }
func (s *preparationObjectsStub) OpenWorkspaceSource(context.Context, string) (io.ReadCloser, error) {
	panic("repository owns source reads")
}

type preparationProviderStub struct {
	*runtimeProviderStub
	events      *[]string
	transferErr error
	receipt     workspace.Receipt
	deadline    time.Time
	transfer    func(context.Context)
}

func (s *preparationProviderStub) Observe(ctx context.Context, ref execution.ExecutionRef) (execution.Observation, error) {
	*s.events = append(*s.events, "observe")
	return s.runtimeProviderStub.Observe(ctx, ref)
}
func (s *preparationProviderStub) MaterializeWorkspace(ctx context.Context, _ execution.ExecutionRef, _ *workspace.Snapshot) (workspace.Receipt, error) {
	*s.events = append(*s.events, "transfer")
	s.deadline, _ = ctx.Deadline()
	if s.transfer != nil {
		s.transfer(ctx)
	}
	return s.receipt, s.transferErr
}

type preparationFixture struct {
	p        *InvocationWorkspacePreparer
	store    *preparationStoreStub
	provider *preparationProviderStub
	claim    persistence.OperationClaim
	effect   persistence.EffectRecord
	target   persistence.InvocationRuntimeTarget
	ref      execution.ExecutionRef
	events   []string
}

func newPreparationFixture(t *testing.T) *preparationFixture {
	t.Helper()
	f := &preparationFixture{}
	f.claim, f.effect, f.target = runtimeDriverFixture()
	scope := "iso_" + strings.Repeat("a", 20)
	f.claim.IsolationDomainID = scope
	f.effect.IsolationDomainID = scope
	f.target.IsolationDomainID = scope
	f.ref = execution.ExecutionRef{IsolationDomainID: scope, ID: "exe_runtime"}
	var data bytes.Buffer
	tw := tar.NewWriter(&data)
	if err := tw.WriteHeader(&tar.Header{Name: "binary", Mode: 0600, Size: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte{0, 255, 1}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data.Bytes()))
	snapshot, err := workspace.Inspect(data.Bytes(), digest)
	if err != nil {
		t.Fatal(err)
	}
	receipt := snapshot.Receipt()
	source := workspace.SourceRecord{SchemaVersion: workspace.SourceRecordV1, IsolationDomainID: scope, WorkspaceID: "wsp_" + strings.Repeat("a", 20), ID: "src_" + strings.Repeat("a", 20), ArchiveDigest: digest, StorageProfileDigest: "sha256:" + strings.Repeat("b", 64), ArchiveBytes: int64(data.Len()), EntryCount: receipt.EntryCount, SourceBytes: receipt.SourceBytes}
	config := InvocationWorkspaceConfig{Scope: InvocationAuthorizationPolicyScope{IsolationDomainID: scope, ServiceID: f.target.ServiceID, RevisionID: f.target.RevisionID}, Source: source, PlanDigest: "sha256:" + strings.Repeat("c", 64), GrantGeneration: 1}
	f.store = &preparationStoreStub{preparation: persistence.InvocationWorkspacePreparation{Execution: f.ref, Source: source, PlanDigest: config.PlanDigest, GrantGeneration: 1, ExpiresAt: time.Now().Add(time.Minute)}, snapshot: snapshot, expires: time.Now().Add(30 * time.Second), events: &f.events}
	f.provider = &preparationProviderStub{runtimeProviderStub: &runtimeProviderStub{observation: execution.Observation{IsolationDomainID: scope, ExecutionID: f.ref.ID, State: "ready"}}, events: &f.events, receipt: receipt}
	f.p, err = NewInvocationWorkspacePreparer(f.store, &preparationObjectsStub{source.StorageProfileDigest}, config)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *preparationFixture) run(ctx context.Context, check func(context.Context) error) error {
	return f.p.prepare(ctx, f.claim, f.effect, f.target, f.ref, f.provider, check)
}
func TestInvocationWorkspacePreparationSequenceAndDeadline(t *testing.T) {
	f := newPreparationFixture(t)
	err := f.run(context.Background(), func(context.Context) error { f.events = append(f.events, "authorize"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.events, []string{"begin", "read", "observe", "authorize", "delivery", "transfer", "authorize", "complete"}) {
		t.Fatal(f.events)
	}
	if !f.provider.deadline.Equal(f.store.expires) {
		t.Fatal("transfer did not use source authorization deadline")
	}
}
func TestInvocationWorkspacePreparationFailsClosed(t *testing.T) {
	for _, name := range []string{"source", "plan", "execution", "generation", "preparation-expired", "read-expired", "read-extended", "snapshot", "begin", "read", "read-cancel", "authority", "ready", "gateway-scope", "gateway-state", "transfer", "receipt", "transfer-cancel", "complete", "after-transfer-authority", "delivery-denied", "delivery-expired"} {
		t.Run(name, func(t *testing.T) {
			f := newPreparationFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fail := errors.New("injected private detail")
			checks := 0
			check := func(context.Context) error {
				checks++
				if name == "authority" || name == "after-transfer-authority" && checks == 2 {
					return fail
				}
				return nil
			}
			switch name {
			case "source":
				f.store.preparation.Source.ID = "src_" + strings.Repeat("b", 20)
			case "plan":
				f.store.preparation.PlanDigest = "sha256:" + strings.Repeat("d", 64)
			case "execution":
				f.store.preparation.Execution.ID = "exe_other"
			case "generation":
				f.store.preparation.GrantGeneration = 2
			case "preparation-expired":
				f.store.preparation.ExpiresAt = time.Now().Add(-time.Second)
			case "read-expired":
				f.store.expires = time.Now().Add(-time.Second)
			case "read-extended":
				f.store.expires = f.store.preparation.ExpiresAt.Add(time.Second)
			case "snapshot":
				f.store.snapshot = nil
			case "begin":
				f.store.beginErr = fail
			case "read":
				f.store.readErr = fail
			case "read-cancel":
				f.store.afterRead = cancel
			case "ready":
				f.provider.observeErr = fail
			case "gateway-scope":
				f.provider.observation.IsolationDomainID = "other"
			case "gateway-state":
				f.provider.observation.State = "terminated"
			case "transfer":
				f.provider.transferErr = fail
			case "receipt":
				f.provider.receipt.EntryCount++
			case "transfer-cancel":
				f.provider.transfer = func(context.Context) { cancel() }
			case "delivery-denied":
				f.store.deliveryErr = fail
			case "delivery-expired":
				f.store.deliveryExpires = time.Now().Add(-time.Second)
			case "complete":
				f.store.completeErr = fail
			}
			if err := f.run(ctx, check); err == nil {
				t.Fatal("failure accepted")
			}
			transferred := false
			completed := false
			for _, event := range f.events {
				if event == "transfer" {
					transferred = true
				}
				if event == "complete" {
					completed = true
				}
			}
			wantTransfer := name == "transfer" || name == "receipt" || name == "transfer-cancel" || name == "complete" || name == "after-transfer-authority"
			if transferred != wantTransfer {
				t.Fatal("unexpected source transfer", f.events)
			}
			if completed != (name == "complete") {
				t.Fatal("unexpected completion", f.events)
			}
		})
	}
}
func TestInvocationWorkspacePreparationRejectsScopeBeforeReading(t *testing.T) {
	for _, name := range []string{"domain", "revision", "service", "effect", "claim", "reference"} {
		t.Run(name, func(t *testing.T) {
			f := newPreparationFixture(t)
			switch name {
			case "domain":
				f.target.IsolationDomainID = "other"
			case "revision":
				f.target.RevisionID = "other"
			case "service":
				f.target.ServiceID = "other"
			case "effect":
				f.effect.OperationID = "other"
			case "claim":
				f.claim.FencingToken = 0
			case "reference":
				f.ref.IsolationDomainID = "other"
			}
			if err := f.run(context.Background(), func(context.Context) error { return nil }); err == nil || len(f.events) != 0 {
				t.Fatal("mismatched authority reached preparation", err, f.events)
			}
		})
	}
}
func TestInvocationWorkspacePreparerRejectsInvalidConfiguration(t *testing.T) {
	for _, name := range []string{"store", "objects", "profile", "source", "scope", "digest", "generation"} {
		t.Run(name, func(t *testing.T) {
			f := newPreparationFixture(t)
			var store InvocationWorkspaceStore = f.store
			var objects workspace.SourceObjectReader = &preparationObjectsStub{f.p.config.Source.StorageProfileDigest}
			config := f.p.config
			switch name {
			case "store":
				store = (*preparationStoreStub)(nil)
			case "objects":
				objects = (*preparationObjectsStub)(nil)
			case "profile":
				objects = &preparationObjectsStub{"other"}
			case "source":
				config.Source.ID = "bad"
			case "scope":
				config.Scope.IsolationDomainID = "other"
			case "digest":
				config.PlanDigest = "bad"
			case "generation":
				config.GrantGeneration = 0
			}
			if _, err := NewInvocationWorkspacePreparer(store, objects, config); err == nil {
				t.Fatal("invalid controller configured")
			}
		})
	}
}

type workspaceRuntimeAuthorizer struct {
	calls  int
	denyAt int
}

func (a *workspaceRuntimeAuthorizer) AuthorizeInvocationRuntime(context.Context, persistence.InvocationRuntimeTarget, dgruntime.StartRequest) error {
	a.calls++
	if a.calls == a.denyAt {
		return ErrInvocationRuntimeDenied
	}
	return nil
}
func TestRuntimeDriverPreparesBeforeStartupAndReauthorizes(t *testing.T) {
	for _, denyAt := range []int{0, 1, 2, 3, 4} {
		t.Run(fmt.Sprint(denyAt), func(t *testing.T) {
			f := newPreparationFixture(t)
			store := &runtimeStoreStub{target: f.target, sourcePrepared: true}
			auth := &workspaceRuntimeAuthorizer{denyAt: denyAt}
			f.provider.beforeStart = func() {
				if len(f.events) == 0 || f.events[len(f.events)-1] != "complete" || store.beginCalls != 1 || auth.calls != 4 {
					t.Fatal("runtime started before authorized completion", f.events, store.beginCalls, auth.calls)
				}
			}
			driver, err := NewInvocationRuntimeDriver(store, auth, InvocationRuntimeRequestBuilderFunc(func(persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error) {
				return dgruntime.StartRequest{Prompt: "task", ApprovalMode: dgruntime.ApprovalLocked, SandboxMode: dgruntime.SandboxWorkspaceWrite}, nil
			}), &runtimeExecutionSourceStub{value: execution.Execution{IsolationDomainID: f.ref.IsolationDomainID, ID: f.ref.ID, State: "ready"}}, f.provider, &runtimeAdapterFactoryStub{adapter: &runtimeAdapterStub{turn: &runtimeTurnStub{events: runtimeEvents(dgruntime.Event{Sequence: 1, Type: "lifecycle.succeeded", Payload: map[string]any{"message": "done"}})}}}, &runtimeArtifactFinalizerStub{}, InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second, Readiness: func(context.Context) error { return nil }, WorkspacePreparer: f.p})
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.ApplyClaimed(context.Background(), f.claim, f.effect)
			if denyAt == 0 {
				if err != nil || f.provider.startCalls != 1 {
					t.Fatal("prepared runtime failed", err)
				}
			} else if !errors.Is(err, ErrEffectDenied) || f.provider.startCalls != 0 || store.beginCalls != 0 {
				t.Fatal("denied runtime started", err, store.beginCalls, f.provider.startCalls)
			}
		})
	}
}
func TestRuntimeDriverRequiresSourceTransportAndReadiness(t *testing.T) {
	for _, name := range []string{"transport", "readiness", "controller"} {
		t.Run(name, func(t *testing.T) {
			f := newPreparationFixture(t)
			var provider invocationRuntimeProvider = f.provider
			config := InvocationRuntimeDriverConfig{LeaseDuration: time.Minute, RenewInterval: time.Second, Readiness: func(context.Context) error { return nil }, WorkspacePreparer: f.p}
			switch name {
			case "transport":
				provider = f.provider.runtimeProviderStub
			case "readiness":
				config.Readiness = nil
			case "controller":
				config.WorkspacePreparer = &InvocationWorkspacePreparer{}
			}
			if _, err := NewInvocationRuntimeDriver(&runtimeStoreStub{}, &runtimeAuthorizerStub{}, InvocationRuntimeRequestBuilderFunc(func(persistence.InvocationRuntimeTarget) (dgruntime.StartRequest, error) {
				return dgruntime.StartRequest{}, nil
			}), &runtimeExecutionSourceStub{}, provider, &runtimeAdapterFactoryStub{}, &runtimeArtifactFinalizerStub{}, config); err == nil {
				t.Fatal("invalid preparation composition accepted")
			}
		})
	}
}

func TestInvocationWorkspaceTransferCannotExtendReadDeadline(t *testing.T) {
	f := newPreparationFixture(t)
	f.store.deliveryExpires = f.store.expires.Add(time.Minute)
	if err := f.run(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !f.provider.deadline.Equal(f.store.expires) {
		t.Fatal("fresh delivery extended read deadline")
	}
}
func TestInvocationWorkspaceTransferStopsAtShorterDeliveryDeadline(t *testing.T) {
	f := newPreparationFixture(t)
	f.store.deliveryExpires = time.Now().Add(100 * time.Millisecond)
	f.provider.transfer = func(ctx context.Context) { <-ctx.Done() }
	if err := f.run(context.Background(), func(context.Context) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired transfer completed", err)
	}
	for _, event := range f.events {
		if event == "complete" {
			t.Fatal("expired transfer completed")
		}
	}
}
