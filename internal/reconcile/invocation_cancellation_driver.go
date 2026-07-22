package reconcile

import (
	"context"
	"errors"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/lifecycle/invocation"
	"github.com/asabla/dataground/internal/persistence"
)

var (
	ErrInvocationCancellationDenied           = errors.New("invocation cancellation denied")
	ErrInvocationCancellationTargetMismatch   = errors.New("invocation cancellation target does not match durable effect")
	ErrInvocationCancellationExecutionUnknown = errors.New("invocation cancellation cannot prove execution absence")
)

type InvocationCancellationTargetStore interface {
	GetInvocationCancellationTarget(context.Context, string, string) (persistence.InvocationCancellationTarget, error)
}

type InvocationCancellationAuthorizer interface {
	AuthorizeInvocationCancellation(context.Context, persistence.InvocationCancellationTarget) error
}

type invocationCancellationProvider interface {
	Observe(context.Context, execution.ExecutionRef) (execution.Observation, error)
	Terminate(context.Context, execution.ExecutionRef) error
}

// InvocationCancellationDriver is the consequential-effect bridge from one
// durable cancellation effect to governed provider termination.
type InvocationCancellationDriver struct {
	targets    InvocationCancellationTargetStore
	authorizer InvocationCancellationAuthorizer
	executions executionByOperationSource
	provider   invocationCancellationProvider
}

func NewInvocationCancellationDriver(
	targets InvocationCancellationTargetStore,
	authorizer InvocationCancellationAuthorizer,
	executions executionByOperationSource,
	provider invocationCancellationProvider,
) (*InvocationCancellationDriver, error) {
	if targets == nil || authorizer == nil || executions == nil || provider == nil {
		return nil, errors.New("invocation cancellation driver dependencies are required")
	}
	return &InvocationCancellationDriver{
		targets: targets, authorizer: authorizer, executions: executions, provider: provider,
	}, nil
}

func (driver *InvocationCancellationDriver) Observe(
	ctx context.Context,
	effect persistence.EffectRecord,
) (map[string]any, bool, error) {
	target, err := driver.target(ctx, effect)
	if err != nil {
		return nil, false, err
	}
	value, found, err := driver.execution(ctx, target)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return invocationCancellationAbsentObservation(), true, nil
	}
	if value.State == "terminated" {
		return invocationCancellationObservation(value.ID, value.State), true, nil
	}
	observation, err := driver.provider.Observe(ctx, execution.ExecutionRef{
		IsolationDomainID: target.IsolationDomainID,
		ID:                value.ID,
	})
	if err != nil {
		return nil, false, err
	}
	if observation.IsolationDomainID != target.IsolationDomainID ||
		observation.ExecutionID != value.ID ||
		observation.State == "" {
		return nil, false, errors.Join(
			ErrAmbiguousEffect,
			ErrInvocationCancellationTargetMismatch,
		)
	}
	return invocationCancellationObservation(observation.ExecutionID, observation.State),
		observation.State == "terminated",
		nil
}

func (driver *InvocationCancellationDriver) Apply(
	ctx context.Context,
	effect persistence.EffectRecord,
) (map[string]any, error) {
	target, err := driver.target(ctx, effect)
	if err != nil {
		return nil, err
	}
	value, found, err := driver.execution(ctx, target)
	if err != nil {
		return nil, err
	}
	if !found {
		return invocationCancellationAbsentObservation(), nil
	}
	if value.State == "terminated" {
		return invocationCancellationObservation(value.ID, value.State), nil
	}
	if err := driver.authorizer.AuthorizeInvocationCancellation(ctx, target); err != nil {
		if errors.Is(err, ErrInvocationCancellationDenied) {
			return nil, errors.Join(ErrEffectDenied, err)
		}
		return nil, err
	}
	ref := execution.ExecutionRef{
		IsolationDomainID: target.IsolationDomainID,
		ID:                value.ID,
	}
	if err := driver.provider.Terminate(ctx, ref); err != nil {
		return nil, errors.Join(ErrAmbiguousEffect, err)
	}
	return invocationCancellationObservation(value.ID, "terminated"), nil
}

func (driver *InvocationCancellationDriver) target(
	ctx context.Context,
	effect persistence.EffectRecord,
) (persistence.InvocationCancellationTarget, error) {
	if effect.OperationKind != persistence.OperationKindInvocation ||
		effect.Phase != "cancel-invocation" ||
		effect.IsolationDomainID == "" ||
		effect.OperationID == "" {
		return persistence.InvocationCancellationTarget{}, errors.Join(
			ErrEffectInvalid,
			ErrInvocationCancellationTargetMismatch,
		)
	}
	target, err := driver.targets.GetInvocationCancellationTarget(
		ctx,
		effect.IsolationDomainID,
		effect.OperationID,
	)
	if errors.Is(err, persistence.ErrInvocationCancellationTargetMissing) {
		return persistence.InvocationCancellationTarget{}, errors.Join(ErrEffectInvalid, err)
	}
	if err != nil {
		return persistence.InvocationCancellationTarget{}, err
	}
	if target.IsolationDomainID != effect.IsolationDomainID ||
		target.OperationID != effect.OperationID ||
		target.InvocationID == "" ||
		target.ServiceID == "" ||
		target.RevisionID == "" ||
		target.ActorID == "" ||
		target.CorrelationID == "" ||
		target.StateMachineVersion != invocation.StateMachineVersion {
		return persistence.InvocationCancellationTarget{}, errors.Join(
			ErrEffectInvalid,
			ErrInvocationCancellationTargetMismatch,
		)
	}
	return target, nil
}

func (driver *InvocationCancellationDriver) execution(
	ctx context.Context,
	target persistence.InvocationCancellationTarget,
) (execution.Execution, bool, error) {
	value, err := driver.executions.GetExecutionByOperation(
		ctx,
		target.IsolationDomainID,
		target.OperationID,
	)
	if errors.Is(err, execution.ErrExecutionMissing) {
		if target.AdmissionPrepared {
			return execution.Execution{}, false, errors.Join(
				ErrAmbiguousEffect,
				ErrInvocationCancellationExecutionUnknown,
			)
		}
		return execution.Execution{}, false, nil
	}
	if err != nil {
		return execution.Execution{}, false, err
	}
	if value.IsolationDomainID != target.IsolationDomainID || value.ID == "" || value.State == "" {
		return execution.Execution{}, false, errors.Join(
			ErrAmbiguousEffect,
			ErrInvocationCancellationTargetMismatch,
		)
	}
	return value, true, nil
}

func invocationCancellationObservation(executionID string, state string) map[string]any {
	return map[string]any{
		"executionId": executionID,
		"state":       state,
	}
}

func invocationCancellationAbsentObservation() map[string]any {
	return map[string]any{"state": "absent"}
}

var _ EffectDriver = (*InvocationCancellationDriver)(nil)
