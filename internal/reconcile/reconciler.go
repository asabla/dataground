package reconcile

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/asabla/dataground/internal/lifecycle/invocation"
	"github.com/asabla/dataground/internal/lifecycle/publication"
	"github.com/asabla/dataground/internal/persistence"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

const defaultLeaseDuration = 30 * time.Second

var (
	ErrAmbiguousEffect = errors.New("external effect acknowledgement is ambiguous")
	ErrEffectDenied    = errors.New("external effect was denied")
	ErrEffectInvalid   = errors.New("external effect request is invalid")
)

type Store interface {
	ClaimNext(context.Context, string, string, time.Duration) (*persistence.OperationClaim, error)
	Advance(context.Context, persistence.OperationClaim, string, map[string]any) error
	Fail(context.Context, persistence.OperationClaim, persistence.OperationFailureReason) error
	ScheduleRetry(context.Context, persistence.OperationClaim, string, string, time.Time) error
	PrepareEffect(context.Context, persistence.OperationClaim, string, [32]byte) (persistence.EffectRecord, error)
	RecordEffect(context.Context, persistence.EffectRecord, string, map[string]any, string) error
}

type EffectDriver interface {
	Observe(context.Context, persistence.EffectRecord) (map[string]any, bool, error)
	Apply(context.Context, persistence.EffectRecord) (map[string]any, error)
}

type Reconciler struct {
	store         Store
	driver        EffectDriver
	workerID      string
	leaseDuration time.Duration
	now           func() time.Time
}

func New(store Store, driver EffectDriver, workerID string) *Reconciler {
	return &Reconciler{
		store:         store,
		driver:        driver,
		workerID:      workerID,
		leaseDuration: defaultLeaseDuration,
		now:           func() time.Time { return time.Now().UTC() },
	}
}

// RunOne advances at most one operation. Durable due_at values control when an
// operation is claimable; callers may poll without owning durable timer state.
func (reconciler *Reconciler) RunOne(ctx context.Context, kind string) (bool, error) {
	ctx, span := otel.Tracer("dataground/control-plane-reconciler").Start(ctx, "reconcile.operation")
	defer span.End()
	claim, err := reconciler.store.ClaimNext(ctx, kind, reconciler.workerID, reconciler.leaseDuration)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "claim failed")
		return false, err
	}
	if claim == nil {
		return false, nil
	}
	span.SetAttributes(
		attribute.String("dataground.operation.kind", claim.Kind),
		attribute.String("dataground.operation.id", claim.ID),
		attribute.String("dataground.isolation_domain.id", claim.IsolationDomainID),
		attribute.Int("dataground.operation.attempt", claim.Attempt),
	)
	if !claim.DeadlineAt.After(reconciler.now()) {
		err = reconciler.store.Fail(ctx, *claim, persistence.OperationFailureDeadline)
	} else {
		err = reconciler.advance(ctx, *claim)
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "reconciliation failed")
		return true, err
	}
	return true, nil
}

func (reconciler *Reconciler) advance(ctx context.Context, claim persistence.OperationClaim) error {
	switch claim.Kind {
	case persistence.OperationKindPublication:
		return reconciler.advancePublication(ctx, claim)
	case persistence.OperationKindInvocation:
		return reconciler.advanceInvocation(ctx, claim)
	default:
		return fmt.Errorf("unsupported operation kind %q", claim.Kind)
	}
}

func (reconciler *Reconciler) advancePublication(ctx context.Context, claim persistence.OperationClaim) error {
	if claim.Command == "cancel" {
		return reconciler.store.Advance(ctx, claim, "cancelled", nil)
	}
	switch claim.ObservedState {
	case "queued":
		return reconciler.store.Advance(ctx, claim, "validating", nil)
	case "validating":
		return reconciler.store.Advance(ctx, claim, "applying", nil)
	case "applying":
		return reconciler.applyEffect(ctx, claim, "publish-revision", "observing")
	case "observing":
		result, ready, err := reconciler.observeEffect(ctx, claim, "publish-revision")
		if err != nil {
			return reconciler.retry(ctx, claim, err)
		}
		if !ready {
			return reconciler.retry(ctx, claim, ErrAmbiguousEffect)
		}
		return reconciler.store.Advance(ctx, claim, "published", result)
	default:
		return fmt.Errorf("publication state %q is not reconcilable", claim.ObservedState)
	}
}

func (reconciler *Reconciler) advanceInvocation(ctx context.Context, claim persistence.OperationClaim) error {
	switch claim.StateMachineVersion {
	case 1:
		if claim.Command == "cancel" {
			if claim.ObservedState != "cancelling" {
				return reconciler.store.Advance(ctx, claim, "cancelling", nil)
			}
			return reconciler.store.Advance(ctx, claim, "cancelled", nil)
		}
		return reconciler.advanceInvocationV1(ctx, claim)
	case invocation.StateMachineVersion:
		if claim.Command == "cancel" {
			if claim.ObservedState != "cancelling" {
				return reconciler.store.Advance(ctx, claim, "cancelling", nil)
			}
			return reconciler.applyEffect(ctx, claim, "cancel-invocation", "cancelled")
		}
		return reconciler.advanceInvocationV2(ctx, claim)
	default:
		return fmt.Errorf("invocation state machine version %d is unsupported", claim.StateMachineVersion)
	}
}

func (reconciler *Reconciler) advanceInvocationV1(
	ctx context.Context,
	claim persistence.OperationClaim,
) error {
	switch claim.ObservedState {
	case "queued":
		return reconciler.store.Advance(ctx, claim, "starting", nil)
	case "starting":
		return reconciler.applyEffect(ctx, claim, "start-invocation", "observing")
	case "observing":
		result, ready, err := reconciler.observeEffect(ctx, claim, "start-invocation")
		if err != nil {
			return reconciler.retry(ctx, claim, err)
		}
		if !ready {
			return reconciler.retry(ctx, claim, ErrAmbiguousEffect)
		}
		return reconciler.store.Advance(ctx, claim, "succeeded", result)
	default:
		return fmt.Errorf("version 1 invocation state %q is not reconcilable", claim.ObservedState)
	}
}

func (reconciler *Reconciler) advanceInvocationV2(
	ctx context.Context,
	claim persistence.OperationClaim,
) error {
	switch claim.ObservedState {
	case "queued":
		return reconciler.store.Advance(ctx, claim, "starting", nil)
	case "starting":
		return reconciler.applyEffect(ctx, claim, "start-invocation", "running")
	case "running":
		return reconciler.applyEffect(ctx, claim, "run-invocation", "observing")
	case "observing":
		result, ready, err := reconciler.observeEffect(ctx, claim, "run-invocation")
		if err != nil {
			return reconciler.retry(ctx, claim, err)
		}
		if !ready {
			return reconciler.retry(ctx, claim, ErrAmbiguousEffect)
		}
		return reconciler.store.Advance(ctx, claim, "succeeded", result)
	default:
		return fmt.Errorf("version 2 invocation state %q is not reconcilable", claim.ObservedState)
	}
}

func (reconciler *Reconciler) applyEffect(
	ctx context.Context,
	claim persistence.OperationClaim,
	phase string,
	nextState string,
) error {
	if !effectAllowed(claim, phase) {
		return fmt.Errorf(
			"operation %q in state %q does not allow effect %q",
			claim.Kind,
			claim.ObservedState,
			phase,
		)
	}
	digest := sha256.Sum256([]byte(claim.IsolationDomainID + ":" + claim.ID + ":" + phase))
	effect, err := reconciler.store.PrepareEffect(ctx, claim, phase, digest)
	if err != nil {
		return err
	}
	result, observed, err := reconciler.driver.Observe(ctx, effect)
	if err != nil {
		if rejected, rejectionErr := reconciler.rejectEffect(ctx, claim, effect, err); rejected {
			return rejectionErr
		}
		return reconciler.retry(ctx, claim, err)
	}
	if !observed {
		result, err = reconciler.driver.Apply(ctx, effect)
		if err != nil {
			if rejected, rejectionErr := reconciler.rejectEffect(ctx, claim, effect, err); rejected {
				return rejectionErr
			}
			status := "failed"
			if errors.Is(err, ErrAmbiguousEffect) {
				status = "unknown"
			}
			if recordErr := reconciler.store.RecordEffect(ctx, effect, status, nil, "EXTERNAL_EFFECT_UNCONFIRMED"); recordErr != nil {
				return errors.Join(err, recordErr)
			}
			return reconciler.retry(ctx, claim, err)
		}
	}
	if err := reconciler.store.RecordEffect(ctx, effect, "succeeded", result, ""); err != nil {
		// The external effect may have succeeded. Persist ambiguity and let the
		// next worker observe the provider receipt before any repeat.
		return reconciler.retry(ctx, claim, ErrAmbiguousEffect)
	}
	return reconciler.store.Advance(ctx, claim, nextState, nil)
}

func effectAllowed(claim persistence.OperationClaim, phase string) bool {
	switch claim.Kind {
	case persistence.OperationKindPublication:
		return publication.AllowsEffect(
			publication.Command(claim.Command),
			publication.State(claim.ObservedState),
			phase,
		)
	case persistence.OperationKindInvocation:
		return invocation.AllowsEffect(
			invocation.Command(claim.Command),
			invocation.State(claim.ObservedState),
			phase,
		)
	default:
		return false
	}
}

func (reconciler *Reconciler) observeEffect(
	ctx context.Context,
	claim persistence.OperationClaim,
	phase string,
) (map[string]any, bool, error) {
	digest := sha256.Sum256([]byte(claim.IsolationDomainID + ":" + claim.ID + ":" + phase))
	effect, err := reconciler.store.PrepareEffect(ctx, claim, phase, digest)
	if err != nil {
		return nil, false, err
	}
	if effect.Status == "succeeded" {
		return effect.Observation, true, nil
	}
	result, observed, err := reconciler.driver.Observe(ctx, effect)
	if err != nil || !observed {
		return result, observed, err
	}
	if err := reconciler.store.RecordEffect(ctx, effect, "succeeded", result, ""); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

func (reconciler *Reconciler) rejectEffect(
	ctx context.Context,
	claim persistence.OperationClaim,
	effect persistence.EffectRecord,
	cause error,
) (bool, error) {
	reason, code, rejected := effectRejection(cause)
	if !rejected {
		return false, nil
	}
	if err := reconciler.store.RecordEffect(ctx, effect, "failed", nil, code); err != nil {
		return true, errors.Join(cause, err)
	}
	return true, reconciler.store.Fail(ctx, claim, reason)
}

func effectRejection(cause error) (persistence.OperationFailureReason, string, bool) {
	switch {
	case errors.Is(cause, ErrEffectDenied):
		return persistence.OperationFailureEffectDenied, "EXTERNAL_EFFECT_DENIED", true
	case errors.Is(cause, ErrEffectInvalid):
		return persistence.OperationFailureEffectInvalid, "EXTERNAL_EFFECT_INVALID", true
	default:
		return "", "", false
	}
}

func (reconciler *Reconciler) retry(ctx context.Context, claim persistence.OperationClaim, cause error) error {
	classification := "retryable"
	if errors.Is(cause, ErrAmbiguousEffect) {
		classification = "unknown"
	}
	delay := time.Duration(1<<min(claim.Attempt, 6)) * time.Second
	return reconciler.store.ScheduleRetry(
		ctx, claim, classification, "RECONCILIATION_RETRY", reconciler.now().Add(delay),
	)
}
