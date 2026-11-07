package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const WorkspaceRetentionRecoveryContract = "dataground.workspace-retention-recovery/v1"

type WorkspaceRetentionRecoveryInput struct {
	IsolationDomainID  string    `json:"isolationDomainId"`
	OperationID        string    `json:"operationId"`
	ExpectedGeneration int64     `json:"expectedGeneration"`
	OwnerBinding       string    `json:"ownerBinding"`
	ActorID            string    `json:"actorId"`
	CorrelationID      string    `json:"correlationId"`
	ReasonDigest       string    `json:"reasonDigest"`
	Deadline           time.Time `json:"deadline"`
}

// RecoverWorkspaceRetention is an internal database-authorized operator command.
// It gives an expired cancellation a new bounded retention window, never
// an execution lease. Workers must still check current cancel, source and export
// authority, image acceptance and retained bytes before completing cancellation.
func (repository *Repository) RecoverWorkspaceRetention(ctx context.Context, input WorkspaceRetentionRecoveryInput) error {
	if !repository.Configured() || ctx == nil {
		return retentionRecoveryDenied()
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := recoverWorkspaceRetentionTx(ctx, tx, input, 0); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Both command paths share the same owner, generation, cancellation and audit
// checks. A public request also pins the invocation version observed by its caller.
func recoverWorkspaceRetentionTx(ctx context.Context, tx pgx.Tx, input WorkspaceRetentionRecoveryInput, expectedVersion int64) error {
	if ctx == nil || input.IsolationDomainID == "" || input.OperationID == "" || input.ExpectedGeneration < 1 ||
		input.ActorID == "" || len(input.ActorID) > 256 || input.CorrelationID == "" || len(input.CorrelationID) > 256 ||
		!workspaceOwnerBindingPattern.MatchString(input.OwnerBinding) || !workspaceOwnerBindingPattern.MatchString(input.ReasonDigest) || input.Deadline.IsZero() {
		return retentionRecoveryDenied()
	}
	input.Deadline = input.Deadline.UTC().Truncate(time.Microsecond)
	encoded, err := json.Marshal(struct {
		Contract string                          `json:"contract"`
		Request  WorkspaceRetentionRecoveryInput `json:"request"`
	}{WorkspaceRetentionRecoveryContract, input})
	if err != nil {
		return retentionRecoveryDenied()
	}
	digest := sha256.Sum256(encoded)
	// Serialize replays of this command, including a lost commit acknowledgement.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "workspace-retention-recovery:"+input.IsolationDomainID+":"+input.CorrelationID); err != nil {
		return err
	}
	var prior []byte
	err = tx.QueryRow(ctx, `SELECT request_digest FROM workspace_retention_recoveries WHERE isolation_domain_id=$1 AND correlation_id=$2`, input.IsolationDomainID, input.CorrelationID).Scan(&prior)
	if err == nil {
		if !bytes.Equal(prior, digest[:]) {
			return &DomainError{Code: "IDEMPOTENCY_KEY_REUSED", Message: "Retention recovery identifier was reused with different content."}
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var invocationID string
	if err := tx.QueryRow(ctx, `SELECT invocation_id FROM invocation_execution_operations WHERE isolation_domain_id=$1 AND id=$2`, input.IsolationDomainID, input.OperationID).Scan(&invocationID); err != nil {
		return retentionRecoveryReadError(err)
	}
	invocation, err := getInvocationForUpdate(ctx, tx, input.IsolationDomainID, invocationID)
	if err != nil {
		return retentionRecoveryReadError(err)
	}
	if invocation.State != "failed" || (expectedVersion > 0 && int64(invocation.Metadata.Version) != expectedVersion) {
		return retentionRecoveryDenied()
	}
	var oldDeadline, now time.Time
	err = tx.QueryRow(ctx, `SELECT deadline_at,clock_timestamp() FROM invocation_execution_operations
 WHERE isolation_domain_id=$1 AND id=$2 AND generation=$3 AND observed_state='failed' AND state_machine_version=2
 AND command='cancel' AND desired_state='cancelled'
 AND error->>'code'='OPERATION_DEADLINE_EXCEEDED' AND lease_owner IS NULL AND lease_expires_at IS NULL FOR UPDATE`, input.IsolationDomainID, input.OperationID, input.ExpectedGeneration).Scan(&oldDeadline, &now)
	if err != nil {
		return retentionRecoveryReadError(err)
	}
	if oldDeadline.After(now) || !input.Deadline.After(now) || input.Deadline.After(now.Add(15*time.Minute)) {
		return retentionRecoveryDenied()
	}
	// Lock the exact instance against concurrent teardown through the commit.
	var executionID string
	err = tx.QueryRow(ctx, `SELECT instance.id FROM execution_instances instance
 JOIN invocation_workspace_quiescence_intents intent ON intent.isolation_domain_id=instance.isolation_domain_id AND intent.operation_id=instance.operation_id AND intent.record->>'executionId'=instance.id
 JOIN invocation_workspace_owner_stop_receipts stopped ON stopped.isolation_domain_id=intent.isolation_domain_id AND stopped.operation_id=intent.operation_id
 WHERE instance.isolation_domain_id=$1 AND instance.operation_id=$2 AND instance.observed_state='ready' AND stopped.owner_binding=$3 FOR UPDATE OF instance`, input.IsolationDomainID, input.OperationID, input.OwnerBinding).Scan(&executionID)
	if err != nil {
		return retentionRecoveryReadError(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO workspace_retention_recoveries(isolation_domain_id,correlation_id,contract,operation_id,request_digest,actor_id,reason_digest,owner_binding,prior_generation,prior_deadline,deadline_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, input.IsolationDomainID, input.CorrelationID, WorkspaceRetentionRecoveryContract, input.OperationID, digest[:], input.ActorID, input.ReasonDigest, input.OwnerBinding, input.ExpectedGeneration, oldDeadline, input.Deadline)
	if err != nil {
		var databaseError *pgconn.PgError
		if errors.As(err, &databaseError) && (databaseError.Code == "P0001" || databaseError.Code == "23514" || databaseError.Code == "23503" || databaseError.Code == "23505") {
			return retentionRecoveryDenied()
		}
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE invocation_execution_operations SET command='cancel',desired_state='cancelled',observed_state='cancelling',generation=generation+1,
 lease_token=lease_token+1,lease_owner=NULL,lease_expires_at=NULL,due_at=$3,deadline_at=$4,effect_actor_id=$5,effect_correlation_id=$6,
 error_classification=NULL,error=NULL,terminal_result=NULL,last_transition_at=$3,updated_at=$3 WHERE isolation_domain_id=$1 AND id=$2`, input.IsolationDomainID, input.OperationID, now, input.Deadline, input.ActorID, input.CorrelationID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE invocations SET state='cancelling',generation=generation+1,version=version+1,completed_at=NULL,error=NULL,updated_at=$3 WHERE isolation_domain_id=$1 AND id=$2`, input.IsolationDomainID, invocationID, now)
	if err != nil {
		return err
	}
	if err := writeOutboxAndAudit(ctx, tx, input.IsolationDomainID, "invocation", invocationID, "workspace.retention-recovery.accepted", input.ActorID, input.CorrelationID, "accepted", input.OperationID, now); err != nil {
		return err
	}
	invocation.State = "cancelling"
	invocation.CorrelationID = input.CorrelationID
	if err := writeInvocationEvent(ctx, tx, invocation, "lifecycle.cancellation.requested", input.ActorID, map[string]any{"state": "cancelling"}, now); err != nil {
		return err
	}
	return nil
}

func retentionRecoveryDenied() error {
	return &DomainError{Code: "WORKSPACE_RETENTION_NOT_RECOVERABLE", Message: "The stopped workspace does not match an expired cancellation and bounded recovery request."}
}

func retentionRecoveryReadError(err error) error {
	var missing *DomainError
	if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &missing) && missing.Code == "RESOURCE_NOT_FOUND") {
		return retentionRecoveryDenied()
	}
	return err
}
