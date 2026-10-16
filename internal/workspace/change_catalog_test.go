package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/identity"
)

func changeRetentionFixture(t *testing.T) (ChangeExportBinding, *ChangeSet) {
	t.Helper()
	base := inspectedFixture(t, archiveEntry{name: "private", content: "base"})
	final := inspectedFixture(t, archiveEntry{name: "private", content: "new\x00\xff"})
	changes, err := Compare(base, final)
	if err != nil {
		t.Fatal(err)
	}
	scope, operation := identity.New("iso"), identity.New("op")
	source := SourceRecord{SchemaVersion: SourceRecordV1, IsolationDomainID: scope, WorkspaceID: identity.New("wsp"), ID: identity.New("src"), ArchiveDigest: base.receipt.ArchiveDigest, StorageProfileDigest: "sha256:" + strings.Repeat("a", 64), ArchiveBytes: int64(len(base.archive)), EntryCount: base.receipt.EntryCount, SourceBytes: base.receipt.SourceBytes}
	binding := ChangeExportBinding{Record: ChangeExportRecord{SchemaVersion: ChangeExportContract, Source: source, InvocationID: identity.New("inv"), OperationID: operation, EffectID: identity.Derived("eff", scope+":invocation-execution:"+operation+":run-invocation"), ExecutionID: identity.New("exe"), PlanDigest: "sha256:" + strings.Repeat("b", 64), Changes: changes.Receipt()}, ActorID: "operator", CorrelationID: identity.New("cor"), LeaseOwner: "worker", FencingToken: 1, StateMachineVersion: 2}
	return binding, changes
}

type changeCatalogStub struct {
	entry                 ChangeExportEntry
	prepares, completions int
	denyPrepare           int
	failComplete          bool
}

func (s *changeCatalogStub) PrepareWorkspaceChangeExport(_ context.Context, b ChangeExportBinding) (ChangeExportEntry, error) {
	s.prepares++
	if s.entry.Record.SchemaVersion != "" && s.entry.Record != b.Record {
		return ChangeExportEntry{}, ErrChangeExportConflict
	}
	if s.entry.Ready {
		return s.entry, nil
	}
	if s.prepares == s.denyPrepare {
		return ChangeExportEntry{}, ErrChangeExportUnavailable
	}
	s.entry.Record = b.Record
	return s.entry, nil
}
func (s *changeCatalogStub) CompleteWorkspaceChangeExport(_ context.Context, b ChangeExportBinding) (ChangeExportEntry, error) {
	s.completions++
	if s.failComplete {
		return ChangeExportEntry{}, ErrChangeExportUnavailable
	}
	if s.entry.Record != b.Record {
		return ChangeExportEntry{}, ErrChangeExportConflict
	}
	s.entry.Ready = true
	return s.entry, nil
}
func (s *changeCatalogStub) GetWorkspaceChangeExport(_ context.Context, scope, op string) (ChangeExportEntry, error) {
	if s.entry.Record.Source.IsolationDomainID != scope || s.entry.Record.OperationID != op {
		return ChangeExportEntry{}, ErrChangeExportMissing
	}
	return s.entry, nil
}

type changeObjectsStub struct {
	data          []byte
	reads, writes int
	readErr       error
	loseWrite     bool
}

func (s *changeObjectsStub) OpenInvocationArtifactObject(context.Context, string) (io.ReadCloser, error) {
	s.reads++
	if s.readErr != nil {
		return nil, s.readErr
	}
	if s.data == nil {
		return nil, artifact.ErrInvocationArtifactObjectMissing
	}
	return io.NopCloser(bytes.NewReader(s.data)), nil
}
func (s *changeObjectsStub) PutInvocationArtifactObjectIfAbsent(_ context.Context, _ string, r io.Reader, _ int64, _, _ string) error {
	s.writes++
	if s.data != nil {
		return artifact.ErrInvocationArtifactObjectConflict
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.data = data
	if s.loseWrite {
		return errors.New("lost acknowledgement")
	}
	return nil
}
func TestWorkspaceChangeRetentionRecoversExactObjectWithoutRepeatingWrite(t *testing.T) {
	binding, changes := changeRetentionFixture(t)
	catalog := &changeCatalogStub{failComplete: true}
	objects := &changeObjectsStub{loseWrite: true}
	if _, err := RetainChanges(context.Background(), catalog, objects, objects, binding, changes); err == nil {
		t.Fatal("failed catalog confirmed export")
	}
	if objects.writes != 1 || len(objects.data) == 0 || catalog.entry.Ready {
		t.Fatal("lost write was not retained as pending")
	}
	catalog.failComplete = false
	binding.LeaseOwner = "replacement"
	binding.FencingToken++
	result, err := RetainChanges(context.Background(), catalog, objects, objects, binding, changes)
	if err != nil || !result.Ready || result.Record != binding.Record || objects.writes != 1 {
		t.Fatal("recovery repeated write or changed export", err)
	}
	completions := catalog.completions
	result, err = RetainChanges(context.Background(), catalog, objects, objects, binding, changes)
	if err != nil || !result.Ready || objects.writes != 1 || catalog.completions != completions {
		t.Fatal("exact replay repeated effects", err)
	}
}
func TestWorkspaceChangeRetentionFailsClosedBeforeNewEffects(t *testing.T) {
	for _, mode := range []string{"entry-denial", "effect-denial", "object-unavailable", "object-conflict", "record-conflict", "receipt-conflict", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			binding, changes := changeRetentionFixture(t)
			catalog := &changeCatalogStub{}
			objects := &changeObjectsStub{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "entry-denial":
				catalog.denyPrepare = 1
			case "effect-denial":
				catalog.denyPrepare = 2
			case "object-unavailable":
				objects.readErr = errors.New("unavailable")
			case "object-conflict":
				objects.data = []byte("different")
			case "record-conflict":
				catalog.entry.Record = binding.Record
				catalog.entry.Record.ExecutionID = identity.New("exe")
			case "receipt-conflict":
				binding.Record.Changes.EntryCount++
			case "cancelled":
				cancel()
			}
			if entry, err := RetainChanges(ctx, catalog, objects, objects, binding, changes); err == nil || entry.Ready || objects.writes != 0 || catalog.completions != 0 {
				t.Fatal("unconfirmed export caused effect", err)
			}
			if (mode == "entry-denial" || mode == "record-conflict" || mode == "receipt-conflict" || mode == "cancelled") && objects.reads != 0 {
				t.Fatal("rejected entry reached object storage")
			}
		})
	}
}
func TestWorkspaceChangeRetentionRejectsMalformedBindings(t *testing.T) {
	for _, mutate := range []func(*ChangeExportBinding){
		func(b *ChangeExportBinding) { b.Record.SchemaVersion = "wrong" }, func(b *ChangeExportBinding) { b.Record.Source.IsolationDomainID = identity.New("iso") }, func(b *ChangeExportBinding) { b.Record.Changes.BaseArchiveDigest = "sha256:" + strings.Repeat("0", 64) }, func(b *ChangeExportBinding) { b.Record.Changes.SizeBytes = MaximumChangeSetBytes + 1 }, func(b *ChangeExportBinding) { b.Record.Changes.ChangeCount = 2*MaximumEntries + 1 }, func(b *ChangeExportBinding) { b.Record.Changes.EntryCount = MaximumEntries + 1 }, func(b *ChangeExportBinding) { b.Record.Changes.SourceBytes = MaximumSourceBytes + 1 }, func(b *ChangeExportBinding) { b.Record.ExecutionID = "native-path" }, func(b *ChangeExportBinding) { b.Record.PlanDigest = "wrong" }, func(b *ChangeExportBinding) { b.FencingToken = 0 }, func(b *ChangeExportBinding) { b.StateMachineVersion++ }, func(b *ChangeExportBinding) { b.ActorID = "bad\nactor" }, func(b *ChangeExportBinding) { b.CorrelationID = "bad" },
	} {
		binding, changes := changeRetentionFixture(t)
		mutate(&binding)
		catalog, objects := &changeCatalogStub{}, &changeObjectsStub{}
		if ValidChangeExportBinding(binding) {
			t.Fatal("invalid binding accepted")
		}
		if _, err := RetainChanges(context.Background(), catalog, objects, objects, binding, changes); err == nil || catalog.prepares != 0 || objects.reads != 0 || objects.writes != 0 {
			t.Fatal("invalid binding reached storage", err)
		}
	}
	binding, changes := changeRetentionFixture(t)
	var catalog *changeCatalogStub
	var objects *changeObjectsStub
	if _, err := RetainChanges(context.Background(), catalog, objects, objects, binding, changes); err == nil {
		t.Fatal("typed nil dependencies accepted")
	}
}
