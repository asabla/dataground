package reconcile

import (
	"context"
	"errors"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/nativehost"
)

// InvocationWorkspaceCompletion joins the existing cleanup and export controllers.
// The owning deployment must accept the exact helper image and exclude native
// startup outside this driver. Completion retains data; it never deletes scratch.
type InvocationWorkspaceCompletion struct {
	store    InvocationWorkspaceCompletionStore
	exporter *InvocationWorkspaceExporter
}

type InvocationWorkspaceCompletionStore interface {
	InvocationWorkspaceQuiescenceStore
	RecordInvocationWorkspaceOwnerStop(context.Context, persistence.InvocationWorkspaceQuiescence) error
}

func NewInvocationWorkspaceCompletion(store InvocationWorkspaceCompletionStore, exporter *InvocationWorkspaceExporter) (*InvocationWorkspaceCompletion, error) {
	value := &InvocationWorkspaceCompletion{store: store, exporter: exporter}
	if !value.valid() {
		return nil, persistence.ErrWorkspaceQuiescence
	}
	return value, nil
}
func (value *InvocationWorkspaceCompletion) valid() bool {
	return value != nil && !governedInvocationDependencyMissing(value.store) && value.exporter.valid()
}

type ownedInvocationRuntimeProvider interface {
	invocationRuntimeProvider
	InvocationWorkspaceChangeProvider
	StartOwnedRuntime(context.Context, execution.ExecutionRef) (*nativehost.Session, error)
}

type invocationWorkspaceFinish func(context.Context, persistence.OperationClaim) error

func (driver *InvocationRuntimeDriver) startRuntimeSession(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, ref execution.ExecutionRef, request dgruntime.StartRequest) (execution.RuntimeSession, invocationWorkspaceFinish, error) {
	if driver.workspaceCompletion == nil {
		session, err := driver.provider.StartRuntime(ctx, ref)
		return session, nil, err
	}
	session, err := driver.provider.(ownedInvocationRuntimeProvider).StartOwnedRuntime(ctx, ref)
	if err != nil || session == nil {
		if session != nil {
			_ = session.Close()
		}
		return nil, nil, errors.Join(ErrAmbiguousEffect, persistence.ErrWorkspaceQuiescence, err)
	}
	binding, err := session.OwnerBinding()
	if err != nil {
		_ = session.Close()
		return nil, nil, errors.Join(ErrAmbiguousEffect, persistence.ErrWorkspaceQuiescence)
	}
	entry, err := driver.workspaceCompletion.store.BeginInvocationWorkspaceQuiescence(ctx, claim, effect, ref, binding)
	configuration := driver.workspaceCompletion.exporter.config
	matches := func(record persistence.InvocationWorkspaceQuiescence) bool {
		return record.Source == configuration.Source && record.PlanDigest == configuration.PlanDigest && workspaceQuiescenceMatches(record, claim, effect, ref, binding)
	}
	if err != nil || entry.Confirmed || !matches(entry.Record) {
		_ = session.Close()
		// A lost intent acknowledgement never permits a turn. Recover only the
		// stopped original connection when the exact intent can be observed.
		observation, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if confirmed, proofErr := session.ConfirmedOwnerBinding(); proofErr == nil && confirmed == binding {
			retained, readErr := driver.workspaceCompletion.store.GetInvocationWorkspaceQuiescence(observation, claim.IsolationDomainID, claim.ID)
			if readErr == nil && matches(retained.Record) {
				_ = driver.workspaceCompletion.store.RecordInvocationWorkspaceOwnerStop(observation, retained.Record)
			}
		}
		return nil, nil, errors.Join(ErrAmbiguousEffect, persistence.ErrWorkspaceQuiescence, err)
	}
	// Retain the connection before the adapter can start a turn. Close records
	// only its verified terminal exchange, even when the execution lease is gone.
	owned := &workspaceOwnerSession{Session: session, store: driver.workspaceCompletion.store, record: entry.Record}
	finish := func(ctx context.Context, claim persistence.OperationClaim) error {
		return driver.finishWorkspace(ctx, claim, effect, target, ref, request, session)
	}
	return owned, finish, nil
}

type workspaceOwnerSession struct {
	*nativehost.Session
	store  InvocationWorkspaceCompletionStore
	record persistence.InvocationWorkspaceQuiescence
}

func (session *workspaceOwnerSession) Close() error {
	if err := session.Session.Close(); err != nil {
		return errors.Join(persistence.ErrWorkspaceQuiescence, err)
	}
	binding, err := session.ConfirmedOwnerBinding()
	if err != nil || binding != session.record.OwnerBinding {
		return persistence.ErrWorkspaceQuiescence
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return session.store.RecordInvocationWorkspaceOwnerStop(ctx, session.record)
}

func (driver *InvocationRuntimeDriver) finishWorkspace(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, ref execution.ExecutionRef, request dgruntime.StartRequest, owner *nativehost.Session) error {
	check := func(ctx context.Context) error {
		if err := driver.ready(ctx); err != nil {
			return err
		}
		return driver.authorizer.AuthorizeInvocationRuntime(ctx, target, request)
	}
	if err := check(ctx); err != nil {
		return err
	}
	renewed, err := driver.store.RenewLease(ctx, claim, driver.leaseDuration)
	if err != nil {
		return err
	}
	if owner != nil {
		if _, err := ConfirmInvocationWorkspaceQuiescence(ctx, driver.workspaceCompletion.store, renewed, effect, ref, owner, check); err != nil {
			return err
		}
	}
	// Recovery relies on the retained original acknowledgement. Export rejects an
	// absent or pending proof; a replacement never starts an owner to manufacture it.
	_, err = driver.workspaceCompletion.exporter.Export(ctx, renewed, effect, ref, driver.provider.(ownedInvocationRuntimeProvider), check)
	return err
}

func (driver *InvocationRuntimeDriver) recoverWorkspace(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord) error {
	if driver.workspaceCompletion == nil {
		return nil
	}
	target, err := driver.store.GetClaimedInvocationRuntimeTarget(ctx, claim)
	if err != nil {
		return err
	}
	if !invocationRuntimeTargetMatchesClaim(target, claim) {
		return ErrInvocationRuntimeTargetMismatch
	}
	value, err := driver.executions.GetExecutionByOperation(ctx, claim.IsolationDomainID, claim.ID)
	if err != nil {
		return err
	}
	ref := execution.ExecutionRef{IsolationDomainID: value.IsolationDomainID, ID: value.ID}
	if ref.IsolationDomainID != claim.IsolationDomainID || ref.ID == "" {
		return ErrInvocationRuntimeTargetMismatch
	}
	request, err := driver.requests.BuildInvocationRuntimeRequest(target)
	if err != nil {
		return err
	}
	if err := driver.validateInvocationRuntimeRequest(request); err != nil {
		return err
	}
	if err := driver.finishWorkspace(ctx, claim, effect, target, ref, request, nil); err != nil {
		return err
	}
	return nil
}
