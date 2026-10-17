package reconcile

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
)

type InvocationWorkspaceExportStore interface {
	workspace.ChangeExportCatalog
	GetInvocationWorkspaceQuiescence(context.Context, string, string) (persistence.InvocationWorkspaceQuiescenceEntry, error)
	AuthorizeInvocationWorkspaceChangeExport(context.Context, persistence.OperationClaim, persistence.EffectRecord, persistence.InvocationWorkspaceQuiescence, string, workspace.ChangeSetReceipt, persistence.WorkspaceChangeExportEvaluator) (persistence.WorkspaceChangeExportPermit, error)
	ReadExportWorkspaceSource(context.Context, persistence.OperationClaim, persistence.EffectRecord, workspace.SourceObjectReader, persistence.InvocationWorkspaceQuiescence, int64, persistence.SourceReadEvaluator) (*workspace.Snapshot, time.Time, error)
}

type InvocationWorkspaceChangeProvider interface {
	ExportWorkspaceChanges(context.Context, execution.ExecutionRef, *workspace.Snapshot) (*workspace.ChangeSet, error)
}

// InvocationWorkspaceExporter composes confirmed cleanup, current Cedar and
// source authority, bounded capture and immutable artifact retention. Its owner
// must keep other writers excluded and verify the exact accepted helper image
// through check. It never starts a runtime or grants sandbox teardown authority.
type InvocationWorkspaceExporter struct {
	store  InvocationWorkspaceExportStore
	source workspace.SourceObjectReader
	reader artifact.ObjectReader
	writer artifact.ObjectWriter
	config InvocationWorkspaceConfig
}

func NewInvocationWorkspaceExporter(store InvocationWorkspaceExportStore, source workspace.SourceObjectReader, reader artifact.ObjectReader, writer artifact.ObjectWriter, config InvocationWorkspaceConfig) (*InvocationWorkspaceExporter, error) {
	e := &InvocationWorkspaceExporter{store: store, source: source, reader: reader, writer: writer, config: config}
	if !e.valid() {
		return nil, workspace.ErrChangeExportConflict
	}
	return e, nil
}
func (e *InvocationWorkspaceExporter) valid() bool {
	return e != nil && !governedInvocationDependencyMissing(e.store) && !governedInvocationDependencyMissing(e.source) && !governedInvocationDependencyMissing(e.reader) && !governedInvocationDependencyMissing(e.writer) && validInvocationAuthorizationPolicyScope(e.config.Scope) && workspace.ValidSourceRecord(e.config.Source) && e.config.Scope.IsolationDomainID == e.config.Source.IsolationDomainID && workspacePlanDigestPattern.MatchString(e.config.PlanDigest) && e.config.GrantGeneration > 0 && e.source.WorkspaceSourceStorageDigest() == e.config.Source.StorageProfileDigest
}
func (e *InvocationWorkspaceExporter) Export(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, ref execution.ExecutionRef, provider InvocationWorkspaceChangeProvider, check func(context.Context) error) (workspace.ChangeExportEntry, error) {
	if !e.valid() || ctx == nil || ctx.Err() != nil || check == nil || governedInvocationDependencyMissing(provider) || !invocationRuntimeEffectMatchesClaim(claim, effect) || claim.IsolationDomainID != e.config.Scope.IsolationDomainID || claim.StateMachineVersion != 2 || ref.IsolationDomainID != e.config.Scope.IsolationDomainID || ref.ID == "" {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	if err := check(ctx); err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	cleanup, err := e.store.GetInvocationWorkspaceQuiescence(ctx, claim.IsolationDomainID, claim.ID)
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	q := cleanup.Record
	if !cleanup.Confirmed || q.SchemaVersion != persistence.WorkspaceQuiescenceContract || q.Source != e.config.Source || q.PlanDigest != e.config.PlanDigest || q.ExecutionID != ref.ID || q.InvocationID != claim.ResourceID || q.OperationID != claim.ID || q.EffectID != effect.EffectID {
		return workspace.ChangeExportEntry{}, persistence.ErrWorkspaceQuiescence
	}
	guard := &workspaceExportGuard{exporter: e, claim: claim, effect: effect, cleanup: q, check: check}
	existing, err := e.store.GetWorkspaceChangeExport(ctx, claim.IsolationDomainID, claim.ID)
	found := err == nil
	if err != nil && !errors.Is(err, workspace.ErrChangeExportMissing) {
		return workspace.ChangeExportEntry{}, err
	}
	phase := "capture"
	if found {
		if !guard.matches(existing.Record) {
			return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
		}
		guard.changes = existing.Record.Changes
		phase = "retain"
	}
	authorized, cancel, err := guard.authorize(ctx, phase)
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	defer cancel()
	base, expires, err := e.store.ReadExportWorkspaceSource(authorized, claim, effect, e.source, q, e.config.GrantGeneration, &SourceCedarEvaluator{})
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	expected := workspace.Receipt{SchemaVersion: "dataground.workspace-source-materialization/v1", ArchiveDigest: q.Source.ArchiveDigest, EntryCount: q.Source.EntryCount, SourceBytes: q.Source.SourceBytes}
	if base == nil || base.Receipt() != expected || !time.Now().Before(expires) {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	workCtx, stop := context.WithDeadline(authorized, expires)
	defer stop()
	var changes *workspace.ChangeSet
	if found {
		changes, err = guard.recover(workCtx, base, existing)
		if err != nil {
			return workspace.ChangeExportEntry{}, err
		}
	}
	if changes == nil {
		captureCtx, end, err := guard.authorize(workCtx, "capture")
		if err != nil {
			return workspace.ChangeExportEntry{}, err
		}
		changes, err = provider.ExportWorkspaceChanges(captureCtx, ref, base)
		if err == nil {
			err = captureCtx.Err()
		}
		end()
		if err != nil {
			return workspace.ChangeExportEntry{}, err
		}
		if changes == nil {
			return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
		}
		if found && changes.Receipt() != existing.Record.Changes {
			return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
		}
		guard.changes = changes.Receipt()
	}
	record := guard.record()
	if !workspace.ValidChangeExportRecord(record) {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	binding := workspace.ChangeExportBinding{Record: record, ActorID: claim.ActorID, CorrelationID: claim.CorrelationID, LeaseOwner: claim.LeaseOwner, FencingToken: claim.FencingToken, StateMachineVersion: claim.StateMachineVersion}
	result, err := workspace.RetainChanges(workCtx, guard, guard, guard, binding, changes)
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	// Completed replay can bypass catalog writes, so current authority is checked
	// once more before reporting success, even when all retained state pre-existed.
	finalCtx, end, err := guard.authorize(workCtx, "retain")
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	defer end()
	if err := finalCtx.Err(); err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	if !result.Ready || result.Record != record {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	return result, nil
}

type workspaceExportGuard struct {
	exporter *InvocationWorkspaceExporter
	claim    persistence.OperationClaim
	effect   persistence.EffectRecord
	cleanup  persistence.InvocationWorkspaceQuiescence
	check    func(context.Context) error
	blocked  error
	changes  workspace.ChangeSetReceipt
}

func (g *workspaceExportGuard) record() workspace.ChangeExportRecord {
	q := g.cleanup
	return workspace.ChangeExportRecord{SchemaVersion: workspace.ChangeExportContract, Source: q.Source, InvocationID: q.InvocationID, OperationID: q.OperationID, EffectID: q.EffectID, ExecutionID: q.ExecutionID, PlanDigest: q.PlanDigest, Changes: g.changes}
}
func (g *workspaceExportGuard) matches(r workspace.ChangeExportRecord) bool {
	expected := g.record()
	expected.Changes = r.Changes
	return workspace.ValidChangeExportRecord(r) && r == expected
}
func (g *workspaceExportGuard) authorize(ctx context.Context, phase string) (boundedResult context.Context, cancelResult context.CancelFunc, resultErr error) {
	if g.blocked != nil {
		return nil, nil, g.blocked
	}
	defer func() {
		if resultErr != nil {
			g.blocked = resultErr
		}
	}()

	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := g.check(ctx); err != nil {
		return nil, nil, err
	}
	changes := g.changes
	if phase == "capture" {
		changes = workspace.ChangeSetReceipt{}
	}
	q := g.cleanup
	c := g.claim
	scope := g.exporter.config.Scope
	expected := authz.WorkspaceChangeExportRequest{Source: q.Source, ServiceID: scope.ServiceID, RevisionID: scope.RevisionID, InvocationID: q.InvocationID, OperationID: q.OperationID, EffectID: q.EffectID, ExecutionID: q.ExecutionID, ActorID: c.ActorID, CorrelationID: c.CorrelationID, PlanDigest: q.PlanDigest, OwnerBinding: q.OwnerBinding, RuntimeToken: q.RuntimeToken, FencingToken: c.FencingToken, Phase: phase, Changes: changes}
	permit, err := g.exporter.store.AuthorizeInvocationWorkspaceChangeExport(ctx, c, g.effect, q, phase, changes, &WorkspaceExportCedarEvaluator{})
	if err != nil {
		return nil, nil, err
	}
	if !expected.Valid() || permit.Request != expected || permit.DecisionID == "" || !time.Now().Before(permit.ExpiresAt) {
		return nil, nil, persistence.ErrWorkspaceExportDenied
	}
	bounded, cancel := context.WithDeadline(ctx, permit.ExpiresAt)
	if err := bounded.Err(); err != nil {
		cancel()
		return nil, nil, err
	}
	return bounded, cancel, nil
}
func (g *workspaceExportGuard) recover(ctx context.Context, base *workspace.Snapshot, entry workspace.ChangeExportEntry) (*workspace.ChangeSet, error) {
	object, err := g.OpenInvocationArtifactObject(ctx, entry.Record.ArtifactRecord().ObjectKey)
	if errors.Is(err, artifact.ErrInvocationArtifactObjectMissing) && !entry.Ready {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(object, entry.Record.Changes.SizeBytes+1))
	defer clear(data)
	closeErr := object.Close()
	if readErr != nil || closeErr != nil || ctx.Err() != nil {
		return nil, workspace.ErrChangeExportUnavailable
	}
	changes, err := workspace.InspectChanges(base, data, entry.Record.Changes.Digest)
	if err != nil || changes.Receipt() != entry.Record.Changes {
		return nil, workspace.ErrChangeExportConflict
	}
	return changes, nil
}
func (g *workspaceExportGuard) PrepareWorkspaceChangeExport(ctx context.Context, b workspace.ChangeExportBinding) (workspace.ChangeExportEntry, error) {
	if !g.bindingMatches(b) {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	bounded, cancel, err := g.authorize(ctx, "retain")
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	defer cancel()
	entry, err := g.exporter.store.PrepareWorkspaceChangeExport(bounded, b)
	if bounded.Err() != nil {
		g.blocked = bounded.Err()
		return workspace.ChangeExportEntry{}, g.blocked
	}
	return entry, err
}
func (g *workspaceExportGuard) CompleteWorkspaceChangeExport(ctx context.Context, b workspace.ChangeExportBinding) (workspace.ChangeExportEntry, error) {
	if !g.bindingMatches(b) {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	bounded, cancel, err := g.authorize(ctx, "retain")
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	defer cancel()
	entry, err := g.exporter.store.CompleteWorkspaceChangeExport(bounded, b)
	if bounded.Err() != nil {
		g.blocked = bounded.Err()
		return workspace.ChangeExportEntry{}, g.blocked
	}
	return entry, err
}
func (g *workspaceExportGuard) bindingMatches(b workspace.ChangeExportBinding) bool {
	c := g.claim
	return b.Record == g.record() && b.ActorID == c.ActorID && b.CorrelationID == c.CorrelationID && b.LeaseOwner == c.LeaseOwner && b.FencingToken == c.FencingToken && b.StateMachineVersion == c.StateMachineVersion
}
func (g *workspaceExportGuard) GetWorkspaceChangeExport(ctx context.Context, scope, op string) (workspace.ChangeExportEntry, error) {
	if scope != g.cleanup.Source.IsolationDomainID || op != g.cleanup.OperationID {
		return workspace.ChangeExportEntry{}, workspace.ErrChangeExportConflict
	}
	bounded, cancel, err := g.authorize(ctx, "retain")
	if err != nil {
		return workspace.ChangeExportEntry{}, err
	}
	defer cancel()
	entry, err := g.exporter.store.GetWorkspaceChangeExport(bounded, scope, op)
	if bounded.Err() != nil {
		g.blocked = bounded.Err()
		return workspace.ChangeExportEntry{}, g.blocked
	}
	return entry, err
}
func (g *workspaceExportGuard) OpenInvocationArtifactObject(ctx context.Context, key string) (io.ReadCloser, error) {
	if key != g.record().ArtifactRecord().ObjectKey {
		return nil, workspace.ErrChangeExportConflict
	}
	bounded, cancel, err := g.authorize(ctx, "retain")
	if err != nil {
		return nil, err
	}
	reader, err := g.exporter.reader.OpenInvocationArtifactObject(bounded, key)
	if bounded.Err() != nil {
		g.blocked = bounded.Err()
		if reader != nil {
			_ = reader.Close()
		}
		cancel()
		return nil, g.blocked
	}
	if err != nil || reader == nil {
		if reader != nil {
			_ = reader.Close()
		}
		cancel()
		if err == nil {
			err = workspace.ErrChangeExportUnavailable
		}
		return nil, err
	}
	return &workspaceExportReader{ReadCloser: reader, ctx: bounded, cancel: cancel, guard: g}, nil
}
func (g *workspaceExportGuard) PutInvocationArtifactObjectIfAbsent(ctx context.Context, key string, content io.Reader, size int64, digest, media string) error {
	r := g.record().ArtifactRecord()
	if key != r.ObjectKey || size != r.SizeBytes || digest != r.Digest || media != r.MediaType {
		return workspace.ErrChangeExportConflict
	}
	bounded, cancel, err := g.authorize(ctx, "retain")
	if err != nil {
		return err
	}
	defer cancel()
	err = g.exporter.writer.PutInvocationArtifactObjectIfAbsent(bounded, key, content, size, digest, media)
	if bounded.Err() != nil {
		g.blocked = bounded.Err()
		return g.blocked
	}
	return err
}

type workspaceExportReader struct {
	guard *workspaceExportGuard
	io.ReadCloser
	ctx    context.Context
	cancel context.CancelFunc
}

func (r *workspaceExportReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		r.guard.blocked = err
		return 0, err
	}
	n, err := r.ReadCloser.Read(p)
	if contextErr := r.ctx.Err(); contextErr != nil {
		clear(p[:n])
		r.guard.blocked = contextErr
		return 0, contextErr
	}
	return n, err
}
func (r *workspaceExportReader) Close() error { defer r.cancel(); return r.ReadCloser.Close() }

var _ InvocationWorkspaceExportStore = (*persistence.Repository)(nil)
