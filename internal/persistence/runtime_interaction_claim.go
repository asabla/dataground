package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func lockRuntimeInteractionAttempt(ctx context.Context, tx pgx.Tx, claim OperationClaim, effect EffectRecord) error {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	if _, err := lockInvocationRuntimeClaim(ctx, tx, claim, now); err != nil {
		return err
	}
	if err := verifyInvocationRuntimeEffect(ctx, tx, effect); err != nil {
		return err
	}
	// The operation lock prevents lease replacement during a decision. NOWAIT
	// avoids reversing the operation-to-invocation order of lifecycle commits.
	var valid bool
	err := tx.QueryRow(ctx, `SELECT true FROM invocation_execution_operations operation
 JOIN invocation_runtime_attempts attempt ON attempt.isolation_domain_id=operation.isolation_domain_id AND attempt.operation_id=operation.id
 WHERE operation.isolation_domain_id=$1 AND operation.id=$2 AND operation.invocation_id=$3
 AND operation.command=$4 AND operation.observed_state='running' AND operation.lease_owner=$5 AND operation.lease_token=$6
 AND operation.lease_expires_at>clock_timestamp() AND operation.deadline_at>clock_timestamp()
 AND attempt.effect_id=$7 AND attempt.status='reserved' AND attempt.lease_owner=$5 AND attempt.fencing_token=$6
 FOR UPDATE OF operation NOWAIT`, claim.IsolationDomainID, claim.ID, claim.ResourceID, claim.Command, claim.LeaseOwner, claim.FencingToken, effect.EffectID).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) || runtimeInteractionLockUnavailable(err) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	return nil
}

func runtimeInteractionLockUnavailable(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) && databaseError.Code == "55P03"
}
