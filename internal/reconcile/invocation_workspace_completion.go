package reconcile

import (
	"context"
	"errors"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/runtime/nativehost"
)

// InvocationWorkspaceCompletion joins the existing cleanup and export controllers.
// The owning deployment must accept the exact helper image and exclude native
// startup outside this driver. Completion retains data; it never deletes scratch.
type InvocationWorkspaceCompletion struct {
	store    InvocationWorkspaceQuiescenceStore
	exporter *InvocationWorkspaceExporter
}

func NewInvocationWorkspaceCompletion(store InvocationWorkspaceQuiescenceStore, exporter *InvocationWorkspaceExporter) (*InvocationWorkspaceCompletion, error) {
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

func (driver *InvocationRuntimeDriver) startRuntimeSession(ctx context.Context, effect persistence.EffectRecord, target persistence.InvocationRuntimeTarget, ref execution.ExecutionRef, request dgruntime.StartRequest) (execution.RuntimeSession, invocationWorkspaceFinish, error) {
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
	finish := func(ctx context.Context, claim persistence.OperationClaim) error {
		return driver.finishWorkspace(ctx, claim, effect, target, ref, request, session)
	}
	return session, finish, nil
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
