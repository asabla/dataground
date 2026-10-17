package reconcile

import (
	"context"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/runtime/nativehost"
)

type InvocationWorkspaceQuiescenceStore interface {
	GetInvocationWorkspaceQuiescence(context.Context, string, string) (persistence.InvocationWorkspaceQuiescenceEntry, error)
	BeginInvocationWorkspaceQuiescence(context.Context, persistence.OperationClaim, persistence.EffectRecord, execution.ExecutionRef, string) (persistence.InvocationWorkspaceQuiescenceEntry, error)
	CompleteInvocationWorkspaceQuiescence(context.Context, persistence.OperationClaim, persistence.EffectRecord, persistence.InvocationWorkspaceQuiescence) (persistence.InvocationWorkspaceQuiescenceEntry, error)
}

// ConfirmInvocationWorkspaceQuiescence is an internal terminal controller for
// the original runtime claim. Its caller must bind session to ref at startup,
// exclude other writers, and supply current effect and accepted-image checks.
// A failure retains the workspace. This does not export files or permit teardown.
// A replacement worker must observe durable completion; it cannot use a new
// session to recover a missing acknowledgement from the original owner.
func ConfirmInvocationWorkspaceQuiescence(ctx context.Context, store InvocationWorkspaceQuiescenceStore, claim persistence.OperationClaim, effect persistence.EffectRecord, ref execution.ExecutionRef, session *nativehost.Session, check func(context.Context) error) (persistence.InvocationWorkspaceQuiescenceEntry, error) {
	if ctx == nil || ctx.Err() != nil || governedInvocationDependencyMissing(store) || session == nil || check == nil || !invocationRuntimeEffectMatchesClaim(claim, effect) || ref.IsolationDomainID != claim.IsolationDomainID || ref.ID == "" {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	binding, err := session.OwnerBinding()
	if err != nil {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	if err := check(ctx); err != nil {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, err
	}
	if confirmed, err := session.ConfirmedOwnerBinding(); err == nil {
		// Retry may reuse the original process-local proof only when intent is
		// already retained. Never create intent after an unrecorded cleanup.
		existing, err := store.GetInvocationWorkspaceQuiescence(ctx, claim.IsolationDomainID, claim.ID)
		if err != nil || confirmed != binding || existing.Record.OwnerBinding != binding {
			return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
		}
	}
	entry, err := store.BeginInvocationWorkspaceQuiescence(ctx, claim, effect, ref, binding)
	if err != nil {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, err
	}
	r := entry.Record
	if r.SchemaVersion != persistence.WorkspaceQuiescenceContract || r.Source.IsolationDomainID != ref.IsolationDomainID || r.ExecutionID != ref.ID || r.InvocationID != claim.ResourceID || r.OperationID != claim.ID || r.EffectID != effect.EffectID || r.OwnerBinding != binding || r.RuntimeOwner != claim.LeaseOwner || r.RuntimeToken != claim.FencingToken || r.ActorID != claim.ActorID || r.CorrelationID != claim.CorrelationID {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	if entry.Confirmed {
		return entry, nil
	}
	if !time.Now().Before(entry.ExpiresAt) {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	cleanupCtx, cancel := context.WithDeadline(ctx, entry.ExpiresAt)
	defer cancel()
	if err := check(cleanupCtx); err != nil {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, err
	}
	if err := session.Quiesce(cleanupCtx); err != nil {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	confirmed, err := session.ConfirmedOwnerBinding()
	if err != nil || confirmed != binding || cleanupCtx.Err() != nil {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	if err := check(cleanupCtx); err != nil {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, err
	}
	completed, err := store.CompleteInvocationWorkspaceQuiescence(cleanupCtx, claim, effect, r)
	if err != nil {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, err
	}
	if !completed.Confirmed || completed.Record != r {
		return persistence.InvocationWorkspaceQuiescenceEntry{}, persistence.ErrWorkspaceQuiescence
	}
	return completed, nil
}
