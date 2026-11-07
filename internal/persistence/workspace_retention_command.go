package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/jackc/pgx/v5"
)

type RequestWorkspaceRetentionRecoveryInput struct {
	InvocationID    string
	ExpectedVersion int64
	ActorID         string
	CorrelationID   string
}

// RequestWorkspaceRetentionRecovery is called only after public action
// authorization. It resolves native bindings internally and shares the existing
// recovery transaction; the accepted request grants no execution or deletion.
func (repository *Repository) RequestWorkspaceRetentionRecovery(ctx context.Context, idempotency Idempotency, input RequestWorkspaceRetentionRecoveryInput) (CommandResult, error) {
	if !repository.Configured() || ctx == nil || !artifactListScopePattern.MatchString(idempotency.IsolationDomainID) || !artifactListInvocationPattern.MatchString(input.InvocationID) || input.ExpectedVersion < 1 || input.ExpectedVersion > 9007199254740990 || input.ActorID == "" || input.CorrelationID == "" {
		return CommandResult{}, retentionRecoveryDenied()
	}
	return repository.execute(ctx, idempotency, func(tx pgx.Tx, _ time.Time) (int, any, error) {
		var operation, binding string
		var generation int64
		var now time.Time
		// This observation grants no authority. The shared transaction locks and
		// rechecks the exact generation, invocation version and binding before use.
		err := tx.QueryRow(ctx, `SELECT operation.id,operation.generation,stopped.owner_binding,clock_timestamp()
 FROM invocations invocation
 JOIN invocation_execution_operations operation ON operation.isolation_domain_id=invocation.isolation_domain_id AND operation.invocation_id=invocation.id
 JOIN invocation_workspace_owner_stop_receipts stopped ON stopped.isolation_domain_id=operation.isolation_domain_id AND stopped.operation_id=operation.id
 WHERE invocation.isolation_domain_id=$1 AND invocation.id=$2 AND invocation.version=$3`, idempotency.IsolationDomainID, input.InvocationID, input.ExpectedVersion).Scan(&operation, &generation, &binding, &now)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, retentionRecoveryDenied()
		}
		if err != nil {
			return 0, nil, err
		}
		deadline := now.UTC().Truncate(time.Microsecond).Add(5 * time.Minute)
		reason := sha256.Sum256([]byte("dataground.public-workspace-retention-recovery/v1"))
		err = recoverWorkspaceRetentionTx(ctx, tx, WorkspaceRetentionRecoveryInput{
			IsolationDomainID: idempotency.IsolationDomainID, OperationID: operation, ExpectedGeneration: generation, OwnerBinding: binding, ActorID: input.ActorID, CorrelationID: input.CorrelationID, ReasonDigest: fmt.Sprintf("sha256:%x", reason), Deadline: deadline,
		}, input.ExpectedVersion)
		if err != nil {
			return 0, nil, err
		}
		return http.StatusAccepted, domain.WorkspaceRetentionRecoveryReceipt{
			SchemaVersion: "dataground.workspace-retention-recovery-receipt/v1", IsolationDomainID: idempotency.IsolationDomainID, InvocationID: input.InvocationID, State: "requested", InvocationVersion: input.ExpectedVersion + 1, DeadlineAt: deadline, CorrelationID: input.CorrelationID,
		}, nil
	})
}
