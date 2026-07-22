package persistence

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrInvocationCancellationTargetMissing = errors.New("invocation cancellation target is missing")

type InvocationCancellationTarget struct {
	IsolationDomainID   string
	OperationID         string
	InvocationID        string
	ServiceID           string
	RevisionID          string
	ActorID             string
	CorrelationID       string
	StateMachineVersion int
	AdmissionPrepared   bool
}

// GetInvocationCancellationTarget resolves the durable identity and admission
// history needed to authorize and safely reconcile one cancellation effect.
func (repository *Repository) GetInvocationCancellationTarget(
	ctx context.Context,
	isolationDomainID string,
	operationID string,
) (InvocationCancellationTarget, error) {
	var target InvocationCancellationTarget
	err := repository.pool.QueryRow(ctx, `
		SELECT operation.isolation_domain_id, operation.id, operation.invocation_id,
		       invocation.service_id, invocation.revision_id,
		       operation.effect_actor_id, operation.effect_correlation_id,
		       operation.state_machine_version,
		       EXISTS (
		         SELECT 1
		         FROM external_effects AS effect
		         WHERE effect.isolation_domain_id = operation.isolation_domain_id
		           AND effect.operation_kind = 'invocation-execution'
		           AND effect.operation_id = operation.id
		           AND effect.phase = 'start-invocation'
		       )
		FROM invocation_execution_operations AS operation
		JOIN invocations AS invocation
		  ON invocation.isolation_domain_id = operation.isolation_domain_id
		 AND invocation.id = operation.invocation_id
		WHERE operation.isolation_domain_id = $1
		  AND operation.id = $2
		  AND operation.command = 'cancel'
		  AND operation.effect_actor_id IS NOT NULL
		  AND operation.effect_correlation_id IS NOT NULL
	`, isolationDomainID, operationID).Scan(
		&target.IsolationDomainID,
		&target.OperationID,
		&target.InvocationID,
		&target.ServiceID,
		&target.RevisionID,
		&target.ActorID,
		&target.CorrelationID,
		&target.StateMachineVersion,
		&target.AdmissionPrepared,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationCancellationTarget{}, ErrInvocationCancellationTargetMissing
	}
	if err != nil {
		return InvocationCancellationTarget{}, err
	}
	return target, nil
}
