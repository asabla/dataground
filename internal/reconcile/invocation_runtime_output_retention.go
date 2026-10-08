package reconcile

import (
	"context"
	"errors"

	"github.com/asabla/dataground/internal/persistence"
)

func (driver *InvocationRuntimeDriver) retainInvalidRuntimeOutput(ctx context.Context, claim persistence.OperationClaim, effect persistence.EffectRecord, sequence uint64) error {
	ctx, cancel := context.WithDeadline(ctx, claim.DeadlineAt)
	defer cancel()
	if err := driver.ready(ctx); err != nil {
		return errors.Join(ErrAmbiguousEffect, err)
	}
	claim, err := driver.store.RenewLease(ctx, claim, driver.leaseDuration)
	if err != nil {
		return errors.Join(ErrAmbiguousEffect, err)
	}
	failure, err := driver.store.PrepareInvocationRuntimeOutputFailure(ctx, claim, effect, sequence)
	if err != nil {
		return errors.Join(ErrAmbiguousEffect, err)
	}
	// Reauthorize retention under the current policy, including after worker
	// replacement. This path acquires no runtime session or workspace export.
	target, err := driver.store.GetClaimedInvocationRuntimeTarget(ctx, claim)
	if err != nil {
		return errors.Join(ErrAmbiguousEffect, err)
	}
	if !invocationRuntimeTargetMatchesClaim(target, claim) {
		return errors.Join(ErrAmbiguousEffect, ErrInvocationRuntimeTargetMismatch)
	}
	request, err := driver.requests.BuildInvocationRuntimeRequest(target)
	if err != nil {
		return errors.Join(ErrAmbiguousEffect, err)
	}
	if err := driver.validateInvocationRuntimeRequest(request); err != nil {
		return errors.Join(ErrAmbiguousEffect, err)
	}
	if err := driver.authorizer.AuthorizeInvocationRuntime(ctx, target, request); err != nil {
		return errors.Join(ErrAmbiguousEffect, err)
	}
	if err := driver.ready(ctx); err != nil {
		return errors.Join(ErrAmbiguousEffect, err)
	}
	claim, err = driver.store.RenewLease(ctx, claim, driver.leaseDuration)
	if err != nil {
		return errors.Join(ErrAmbiguousEffect, err)
	}
	if !failure.Retained {
		if _, err := driver.artifacts.Finalize(ctx, failure.Artifact); err != nil {
			return errors.Join(ErrAmbiguousEffect, err)
		}
	}
	if err := driver.ready(ctx); err != nil {
		return errors.Join(ErrAmbiguousEffect, err)
	}
	if _, err := driver.store.FailInvocationRuntimeAttempt(ctx, claim, effect, failure.Result); err != nil {
		return errors.Join(ErrAmbiguousEffect, err)
	}
	return errors.Join(ErrEffectTerminal, ErrInvocationRuntimeOutputInvalid)
}
