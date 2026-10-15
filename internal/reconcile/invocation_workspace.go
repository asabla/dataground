package reconcile

import (
	"context"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
)

type InvocationWorkspaceStore interface {
	BeginInvocationWorkspacePreparation(context.Context, persistence.OperationClaim, persistence.EffectRecord, execution.ExecutionRef, int64) (persistence.InvocationWorkspacePreparation, error)
	ReadPreparingWorkspaceSource(context.Context, persistence.OperationClaim, persistence.EffectRecord, workspace.SourceObjectReader, persistence.InvocationWorkspacePreparation, persistence.SourceReadEvaluator) (*workspace.Snapshot, time.Time, error)
	AuthorizeInvocationWorkspaceTransfer(context.Context, persistence.OperationClaim, persistence.EffectRecord, persistence.InvocationWorkspacePreparation, persistence.SourceReadEvaluator) (time.Time, error)
	CompleteInvocationWorkspacePreparation(context.Context, persistence.OperationClaim, persistence.EffectRecord, persistence.InvocationWorkspacePreparation, workspace.Receipt, persistence.SourceReadEvaluator) error
}

type invocationWorkspaceProvider interface {
	Observe(context.Context, execution.ExecutionRef) (execution.Observation, error)
	MaterializeWorkspace(context.Context, execution.ExecutionRef, *workspace.Snapshot) (workspace.Receipt, error)
}

type InvocationWorkspaceConfig struct {
	Scope           InvocationAuthorizationPolicyScope
	Source          workspace.SourceRecord
	PlanDigest      string
	GrantGeneration int64
}

var workspacePlanDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// InvocationWorkspacePreparer is configured for one immutable source plan.
// Only the runtime driver invokes it, under that driver's effect authorization,
// readiness checks, and provider. The owning composition must accept the exact
// helper image and prevent out-of-band harness startup in its managed execution.
type InvocationWorkspacePreparer struct {
	store   InvocationWorkspaceStore
	objects workspace.SourceObjectReader
	config  InvocationWorkspaceConfig
}

func NewInvocationWorkspacePreparer(store InvocationWorkspaceStore, objects workspace.SourceObjectReader, config InvocationWorkspaceConfig) (*InvocationWorkspacePreparer, error) {
	p := &InvocationWorkspacePreparer{store: store, objects: objects, config: config}
	if !p.valid() {
		return nil, persistence.ErrWorkspacePreparation
	}
	return p, nil
}

func (p *InvocationWorkspacePreparer) valid() bool {
	return p != nil && !governedInvocationDependencyMissing(p.store) && !governedInvocationDependencyMissing(p.objects) &&
		validInvocationAuthorizationPolicyScope(p.config.Scope) && workspace.ValidSourceRecord(p.config.Source) &&
		p.config.Scope.IsolationDomainID == p.config.Source.IsolationDomainID && workspacePlanDigestPattern.MatchString(p.config.PlanDigest) && p.config.GrantGeneration > 0 &&
		p.objects.WorkspaceSourceStorageDigest() == p.config.Source.StorageProfileDigest
}

func (p *InvocationWorkspacePreparer) prepare(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, ref execution.ExecutionRef, provider invocationWorkspaceProvider, check func(context.Context) error) error {
	if !p.valid() || ctx == nil || check == nil || governedInvocationDependencyMissing(provider) ||
		!invocationRuntimeEffectMatchesClaim(claim, effect) || !invocationRuntimeTargetMatchesClaim(target, claim) ||
		p.config.Scope != (InvocationAuthorizationPolicyScope{IsolationDomainID: target.IsolationDomainID, ServiceID: target.ServiceID, RevisionID: target.RevisionID}) ||
		ref.IsolationDomainID != target.IsolationDomainID || ref.ID == "" {
		return persistence.ErrWorkspacePreparation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	preparation, err := p.store.BeginInvocationWorkspacePreparation(ctx, claim, effect, ref, p.config.GrantGeneration)
	if err != nil {
		return err
	}
	if preparation.Execution != ref || preparation.Source != p.config.Source || preparation.PlanDigest != p.config.PlanDigest || preparation.GrantGeneration != p.config.GrantGeneration || !time.Now().Before(preparation.ExpiresAt) {
		return persistence.ErrWorkspacePreparation
	}
	// No renewal extends a source transfer. A replacement lease must repeat the
	// read and helper observation before it can commit its own completion.
	prepareCtx, cancel := context.WithDeadline(ctx, preparation.ExpiresAt)
	defer cancel()
	snapshot, expires, err := p.store.ReadPreparingWorkspaceSource(prepareCtx, claim, effect, p.objects, preparation, &SourceCedarEvaluator{})
	if err != nil {
		return err
	}
	if snapshot == nil || !time.Now().Before(expires) || expires.After(preparation.ExpiresAt) {
		return persistence.ErrWorkspacePreparation
	}
	transferCtx, stop := context.WithDeadline(prepareCtx, expires)
	defer stop()
	expected := workspace.Receipt{SchemaVersion: "dataground.workspace-source-materialization/v1", ArchiveDigest: p.config.Source.ArchiveDigest, EntryCount: p.config.Source.EntryCount, SourceBytes: p.config.Source.SourceBytes}
	if snapshot.Receipt() != expected {
		return persistence.ErrWorkspacePreparation
	}
	if err := transferCtx.Err(); err != nil {
		return err
	}
	observation, err := provider.Observe(transferCtx, ref)
	if err != nil || observation.IsolationDomainID != ref.IsolationDomainID || observation.ExecutionID != ref.ID || observation.State != "ready" {
		return persistence.ErrWorkspacePreparation
	}
	if err := check(transferCtx); err != nil {
		return err
	}
	deliveryExpires, err := p.store.AuthorizeInvocationWorkspaceTransfer(transferCtx, claim, effect, preparation, &SourceCedarEvaluator{})
	if err != nil {
		return err
	}
	if !time.Now().Before(deliveryExpires) {
		return persistence.ErrWorkspacePreparation
	}
	// A renewed claim or grant cannot widen the previously authorized read.
	deliveryCtx, endDelivery := context.WithDeadline(transferCtx, deliveryExpires)
	defer endDelivery()
	if err := deliveryCtx.Err(); err != nil {
		return err
	}
	receipt, err := provider.MaterializeWorkspace(deliveryCtx, ref, snapshot)
	if err != nil || receipt != expected {
		// A lost acknowledgement may follow a completed installation. Do not
		// record completion or retry here; claim recovery repeats exact read-back.
		return persistence.ErrWorkspacePreparation
	}
	if err := deliveryCtx.Err(); err != nil {
		return err
	}
	if err := check(deliveryCtx); err != nil {
		return err
	}
	return p.store.CompleteInvocationWorkspacePreparation(deliveryCtx, claim, effect, preparation, receipt, &SourceCedarEvaluator{})
}
