package persistence

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrInvocationAdmissionTargetMissing = errors.New("invocation admission target is missing")

type InvocationAdmissionTarget struct {
	IsolationDomainID   string
	OperationID         string
	InvocationID        string
	ServiceID           string
	RevisionID          string
	ActorID             string
	CorrelationID       string
	StateMachineVersion int
}

// GetInvocationAdmissionTarget resolves only the durable identity needed to
// reauthorize one consequential start effect. Scope and operation identity are
// both part of the lookup.
func (repository *Repository) GetInvocationAdmissionTarget(
	ctx context.Context,
	isolationDomainID string,
	operationID string,
) (InvocationAdmissionTarget, error) {
	var target InvocationAdmissionTarget
	err := repository.pool.QueryRow(ctx, `
		SELECT operation.isolation_domain_id, operation.id, operation.invocation_id,
		       invocation.service_id, invocation.revision_id,
		       COALESCE(operation.effect_actor_id, operation.actor_id),
		       COALESCE(operation.effect_correlation_id, operation.correlation_id),
		       operation.state_machine_version
		FROM invocation_execution_operations AS operation
		JOIN invocations AS invocation
		  ON invocation.isolation_domain_id = operation.isolation_domain_id
		 AND invocation.id = operation.invocation_id
		WHERE operation.isolation_domain_id = $1
		  AND operation.id = $2
		  AND (
		    operation.command = 'invoke'
		    OR (
		      operation.command = 'repair'
		      AND operation.effect_actor_id IS NOT NULL
		      AND operation.effect_correlation_id IS NOT NULL
		    )
		  )
	`, isolationDomainID, operationID).Scan(
		&target.IsolationDomainID,
		&target.OperationID,
		&target.InvocationID,
		&target.ServiceID,
		&target.RevisionID,
		&target.ActorID,
		&target.CorrelationID,
		&target.StateMachineVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationAdmissionTarget{}, ErrInvocationAdmissionTargetMissing
	}
	if err != nil {
		return InvocationAdmissionTarget{}, err
	}
	return target, nil
}
