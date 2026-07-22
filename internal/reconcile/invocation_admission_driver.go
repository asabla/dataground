package reconcile

import (
	"context"
	"errors"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/lifecycle/invocation"
	"github.com/asabla/dataground/internal/persistence"
)

var ErrInvocationAdmissionTargetMismatch = errors.New("invocation admission target does not match durable effect")

type InvocationAdmissionTargetStore interface {
	GetInvocationAdmissionTarget(context.Context, string, string) (persistence.InvocationAdmissionTarget, error)
}

type InvocationAdmissionAuthorizer interface {
	AuthorizeInvocationAdmission(context.Context, persistence.InvocationAdmissionTarget) error
}

type invocationAdmitter interface {
	Admit(context.Context, execution.AdmissionRequest) (execution.Execution, error)
}

type executionByOperationSource interface {
	GetExecutionByOperation(context.Context, string, string) (execution.Execution, error)
}

// InvocationAdmissionDriver is the consequential-effect bridge from one
// durable invocation start effect to governed execution admission.
type InvocationAdmissionDriver struct {
	targets    InvocationAdmissionTargetStore
	authorizer InvocationAdmissionAuthorizer
	admission  invocationAdmitter
	executions executionByOperationSource
}

func NewInvocationAdmissionDriver(
	targets InvocationAdmissionTargetStore,
	authorizer InvocationAdmissionAuthorizer,
	admission invocationAdmitter,
	executions executionByOperationSource,
) (*InvocationAdmissionDriver, error) {
	if targets == nil || authorizer == nil || admission == nil || executions == nil {
		return nil, errors.New("invocation admission driver dependencies are required")
	}
	return &InvocationAdmissionDriver{
		targets: targets, authorizer: authorizer, admission: admission, executions: executions,
	}, nil
}

func (driver *InvocationAdmissionDriver) Observe(
	ctx context.Context,
	effect persistence.EffectRecord,
) (map[string]any, bool, error) {
	target, err := driver.authorizedTarget(ctx, effect)
	if err != nil {
		return nil, false, err
	}
	value, err := driver.executions.GetExecutionByOperation(
		ctx,
		target.IsolationDomainID,
		target.OperationID,
	)
	if errors.Is(err, execution.ErrExecutionMissing) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if value.IsolationDomainID != target.IsolationDomainID || value.ID == "" || value.State == "" {
		return nil, false, ErrInvocationAdmissionTargetMismatch
	}
	return invocationAdmissionObservation(value), true, nil
}

func (driver *InvocationAdmissionDriver) Apply(
	ctx context.Context,
	effect persistence.EffectRecord,
) (map[string]any, error) {
	target, err := driver.authorizedTarget(ctx, effect)
	if err != nil {
		return nil, err
	}
	value, err := driver.admission.Admit(ctx, execution.AdmissionRequest{
		IsolationDomainID: target.IsolationDomainID,
		RevisionID:        target.RevisionID,
		OperationID:       target.OperationID,
	})
	if err != nil {
		return nil, err
	}
	if value.IsolationDomainID != target.IsolationDomainID || value.ID == "" || value.State == "" {
		return nil, ErrInvocationAdmissionTargetMismatch
	}
	return invocationAdmissionObservation(value), nil
}

func (driver *InvocationAdmissionDriver) authorizedTarget(
	ctx context.Context,
	effect persistence.EffectRecord,
) (persistence.InvocationAdmissionTarget, error) {
	if effect.OperationKind != persistence.OperationKindInvocation ||
		effect.Phase != "start-invocation" ||
		effect.IsolationDomainID == "" ||
		effect.OperationID == "" {
		return persistence.InvocationAdmissionTarget{}, ErrInvocationAdmissionTargetMismatch
	}
	target, err := driver.targets.GetInvocationAdmissionTarget(
		ctx,
		effect.IsolationDomainID,
		effect.OperationID,
	)
	if err != nil {
		return persistence.InvocationAdmissionTarget{}, err
	}
	if target.IsolationDomainID != effect.IsolationDomainID ||
		target.OperationID != effect.OperationID ||
		target.InvocationID == "" ||
		target.ServiceID == "" ||
		target.RevisionID == "" ||
		target.ActorID == "" ||
		target.CorrelationID == "" ||
		target.StateMachineVersion != invocation.StateMachineVersion {
		return persistence.InvocationAdmissionTarget{}, ErrInvocationAdmissionTargetMismatch
	}
	if err := driver.authorizer.AuthorizeInvocationAdmission(ctx, target); err != nil {
		return persistence.InvocationAdmissionTarget{}, err
	}
	return target, nil
}

func invocationAdmissionObservation(value execution.Execution) map[string]any {
	return map[string]any{
		"executionId": value.ID,
		"state":       value.State,
	}
}

var _ EffectDriver = (*InvocationAdmissionDriver)(nil)
