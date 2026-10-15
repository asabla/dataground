package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5"
)

var ErrWorkspacePreparation = errors.New("workspace preparation is unavailable or conflicts with its claim")

// InvocationWorkspacePreparation binds preparation before a runtime attempt.
// It does not inspect sandbox processes or authorize source use or a turn.
type InvocationWorkspacePreparation struct {
	Execution       execution.ExecutionRef
	Source          workspace.SourceRecord
	PlanDigest      string
	GrantGeneration int64
	ExpiresAt       time.Time
}

func (repository *Repository) BeginInvocationWorkspacePreparation(ctx context.Context, claim OperationClaim, effect EffectRecord, expected execution.ExecutionRef, generation int64) (InvocationWorkspacePreparation, error) {
	if !repository.Configured() || ctx == nil || !validInvocationRuntimeAttempt(claim, effect) || expected.IsolationDomainID != claim.IsolationDomainID || expected.ID == "" || generation < 1 {
		return InvocationWorkspacePreparation{}, ErrWorkspacePreparation
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationWorkspacePreparation{}, err
	}
	defer tx.Rollback(ctx)
	target, preparation, err := lockWorkspacePreparationTarget(ctx, tx, claim, effect)
	if err != nil {
		return preparation, err
	}
	if preparation.Execution != expected {
		return InvocationWorkspacePreparation{}, ErrWorkspacePreparation
	}
	preparation.GrantGeneration = generation
	encoded, err := json.Marshal(preparation.Source)
	if err != nil {
		return InvocationWorkspacePreparation{}, err
	}
	result, err := tx.Exec(ctx, `INSERT INTO invocation_workspace_preparations(isolation_domain_id,operation_id,fencing_token,effect_id,lease_owner,execution_id,plan_digest,source_record,grant_generation,actor_id,correlation_id)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11 WHERE EXISTS (
 SELECT 1 FROM invocation_execution_operations WHERE isolation_domain_id=$1 AND id=$2 AND lease_owner=$5 AND lease_token=$3 AND lease_expires_at>clock_timestamp() AND deadline_at>clock_timestamp()) ON CONFLICT DO NOTHING`, claim.IsolationDomainID, claim.ID, claim.FencingToken, effect.EffectID, claim.LeaseOwner, expected.ID, preparation.PlanDigest, encoded, generation, target.ActorID, target.CorrelationID)
	if err != nil {
		return InvocationWorkspacePreparation{}, err
	}
	if err := verifyWorkspacePreparation(ctx, tx, claim, effect, preparation); err != nil {
		return InvocationWorkspacePreparation{}, err
	}
	if result.RowsAffected() == 1 {
		if err := writeOutboxAndAudit(ctx, tx, claim.IsolationDomainID, "workspace-preparation", workspacePreparationResourceID(claim), "workspace.source.preparation.started", target.ActorID, target.CorrelationID, "accepted", claim.ID, repository.now()); err != nil {
			return InvocationWorkspacePreparation{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationWorkspacePreparation{}, err
	}
	return preparation, nil
}

// lockWorkspacePreparationTarget holds the invocation, operation, and exact
// execution while checking that no permission to start a turn has been consumed.
func lockWorkspacePreparationTarget(ctx context.Context, tx pgx.Tx, claim OperationClaim, effect EffectRecord) (InvocationRuntimeTarget, InvocationWorkspacePreparation, error) {
	var preparation InvocationWorkspacePreparation
	if !validInvocationRuntimeAttempt(claim, effect) {
		return InvocationRuntimeTarget{}, preparation, ErrWorkspacePreparation
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return InvocationRuntimeTarget{}, preparation, err
	}
	invocation, err := lockInvocationRuntimeClaim(ctx, tx, claim, now)
	if err != nil || invocation != claim.ResourceID {
		return InvocationRuntimeTarget{}, preparation, ErrWorkspacePreparation
	}
	if err := verifyInvocationRuntimeEffect(ctx, tx, effect); err != nil {
		return InvocationRuntimeTarget{}, preparation, err
	}
	err = tx.QueryRow(ctx, `SELECT LEAST(operation.lease_expires_at,operation.deadline_at),instance.id
 FROM invocation_execution_operations operation JOIN execution_instances instance
 ON instance.isolation_domain_id=operation.isolation_domain_id AND instance.operation_id=operation.id
 WHERE operation.isolation_domain_id=$1 AND operation.id=$2 AND operation.lease_owner=$3 AND operation.lease_token=$4
 AND operation.lease_expires_at>clock_timestamp() AND operation.deadline_at>clock_timestamp() AND instance.observed_state='ready'
 AND NOT EXISTS(SELECT 1 FROM invocation_runtime_attempts attempt WHERE attempt.isolation_domain_id=operation.isolation_domain_id AND attempt.operation_id=operation.id)
 FOR UPDATE OF operation,instance NOWAIT`, claim.IsolationDomainID, claim.ID, claim.LeaseOwner, claim.FencingToken).Scan(&preparation.ExpiresAt, &preparation.Execution.ID)
	if err != nil {
		return InvocationRuntimeTarget{}, preparation, ErrWorkspacePreparation
	}
	preparation.Execution.IsolationDomainID = claim.IsolationDomainID
	target, err := getInvocationRuntimeTarget(ctx, tx, claim.IsolationDomainID, claim.ID)
	if err != nil {
		return target, preparation, err
	}
	plan, err := executionpostgres.ReadExecutionPlan(ctx, tx, claim.IsolationDomainID, target.RevisionID)
	if err != nil || plan.SchemaVersion != execution.ExecutionPlanSchemaV2 || plan.WorkspaceSource == nil {
		return target, preparation, ErrWorkspacePreparation
	}
	preparation.Source = *plan.WorkspaceSource
	preparation.PlanDigest, err = execution.DigestExecutionPlan(plan)
	return target, preparation, err
}

func verifyWorkspacePreparation(ctx context.Context, tx pgx.Tx, claim OperationClaim, effect EffectRecord, expected InvocationWorkspacePreparation) error {
	var owner, effectID, executionID, digest string
	var encoded []byte
	var generation int64
	err := tx.QueryRow(ctx, `SELECT lease_owner,effect_id,execution_id,plan_digest,source_record,grant_generation FROM invocation_workspace_preparations
 WHERE isolation_domain_id=$1 AND operation_id=$2 AND fencing_token=$3`, claim.IsolationDomainID, claim.ID, claim.FencingToken).Scan(&owner, &effectID, &executionID, &digest, &encoded, &generation)
	if err != nil {
		return ErrWorkspacePreparation
	}
	var source workspace.SourceRecord
	if json.Unmarshal(encoded, &source) != nil || source != expected.Source || owner != claim.LeaseOwner || effectID != effect.EffectID || executionID != expected.Execution.ID || digest != expected.PlanDigest || generation != expected.GrantGeneration {
		return ErrWorkspacePreparation
	}
	return nil
}

// CompleteInvocationWorkspacePreparation commits the helper's exact receipt
// only with a fresh source-policy and grant decision under the same active claim.
func (repository *Repository) CompleteInvocationWorkspacePreparation(ctx context.Context, claim OperationClaim, effect EffectRecord, preparation InvocationWorkspacePreparation, receipt workspace.Receipt, evaluator SourceReadEvaluator) error {
	if !repository.Configured() || ctx == nil || missingSourceReadEvaluator(evaluator) || preparation.Source.IsolationDomainID != claim.IsolationDomainID || receipt != (workspace.Receipt{SchemaVersion: "dataground.workspace-source-materialization/v1", ArchiveDigest: preparation.Source.ArchiveDigest, EntryCount: preparation.Source.EntryCount, SourceBytes: preparation.Source.SourceBytes}) {
		return ErrWorkspacePreparation
	}
	bound := &invocationSourceAuthorizer{repository: repository, claim: claim, effect: effect, evaluator: evaluator, preparation: &preparation, completion: true}
	use, _, err := bound.sourceUse(ctx, preparation.Source, preparation.GrantGeneration)
	if err != nil {
		return err
	}
	_, err = bound.AuthorizeWorkspaceSource(ctx, use, workspace.SourceReadDelivery)
	return err
}

func (repository *Repository) ReadPreparingWorkspaceSource(ctx context.Context, claim OperationClaim, effect EffectRecord, objects workspace.SourceObjectReader, preparation InvocationWorkspacePreparation, evaluator SourceReadEvaluator) (*workspace.Snapshot, time.Time, error) {
	if !repository.Configured() || ctx == nil || objects == nil || missingSourceReadEvaluator(evaluator) {
		return nil, time.Time{}, ErrWorkspacePreparation
	}
	bound := &invocationSourceAuthorizer{repository: repository, claim: claim, effect: effect, evaluator: evaluator, preparation: &preparation}
	use, expires, err := bound.sourceUse(ctx, preparation.Source, preparation.GrantGeneration)
	if err != nil {
		return nil, time.Time{}, err
	}
	readCtx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	snapshot, err := workspace.ReadSource(readCtx, repository, objects, bound, use)
	return snapshot, bound.deliveryExpires, err
}

func recordWorkspaceCompletion(ctx context.Context, tx pgx.Tx, bound *invocationSourceAuthorizer, use workspace.SourceUse, decisionID string) error {
	result, err := tx.Exec(ctx, `INSERT INTO invocation_workspace_completions(isolation_domain_id,operation_id,fencing_token,decision_id)
 SELECT $1,$2,$3,$4 WHERE EXISTS(SELECT 1 FROM invocation_execution_operations WHERE isolation_domain_id=$1 AND id=$2 AND lease_owner=$5 AND lease_token=$3 AND lease_expires_at>clock_timestamp() AND deadline_at>clock_timestamp()) ON CONFLICT DO NOTHING`, bound.claim.IsolationDomainID, bound.claim.ID, bound.claim.FencingToken, decisionID, bound.claim.LeaseOwner)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invocation_workspace_completions WHERE isolation_domain_id=$1 AND operation_id=$2 AND fencing_token=$3)`, bound.claim.IsolationDomainID, bound.claim.ID, bound.claim.FencingToken).Scan(&exists); err != nil || !exists {
			return ErrWorkspacePreparation
		}
		return nil
	}
	return writeOutboxAndAudit(ctx, tx, use.Record.IsolationDomainID, "workspace-preparation", workspacePreparationResourceID(bound.claim), "workspace.source.preparation.completed", use.ActorID, use.CorrelationID, "accepted", bound.claim.ID, bound.repository.now())
}

// CheckInvocationWorkspacePreparation is the runtime-start gate. A source plan
// requires an exact completion under the current worker lease. The caller must
// then consume BeginInvocationRuntimeAttempt before opening a native runtime.
func (repository *Repository) CheckInvocationWorkspacePreparation(ctx context.Context, claim OperationClaim, effect EffectRecord) (bool, error) {
	if !repository.Configured() || ctx == nil || !validInvocationRuntimeAttempt(claim, effect) {
		return false, ErrWorkspacePreparation
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	required, err := requireWorkspaceCompletion(ctx, tx, claim, effect)
	if err != nil {
		return required, err
	}
	if err := tx.Commit(ctx); err != nil {
		return required, err
	}
	return required, nil
}

func requireWorkspaceCompletion(ctx context.Context, tx pgx.Tx, claim OperationClaim, effect EffectRecord) (bool, error) {
	var revision string
	err := tx.QueryRow(ctx, `SELECT invocation.revision_id FROM invocations invocation JOIN invocation_execution_operations operation ON operation.isolation_domain_id=invocation.isolation_domain_id AND operation.invocation_id=invocation.id WHERE operation.isolation_domain_id=$1 AND operation.id=$2`, claim.IsolationDomainID, claim.ID).Scan(&revision)
	if err != nil {
		return false, err
	}
	plan, err := executionpostgres.ReadExecutionPlan(ctx, tx, claim.IsolationDomainID, revision)
	if errors.Is(err, execution.ErrExecutionPlanMissing) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if plan.WorkspaceSource == nil {
		return false, nil
	}
	if _, err := getInvocationRuntimeAttempt(ctx, tx, claim.IsolationDomainID, claim.ID); err == nil {
		return true, ErrInvocationRuntimeAttemptAmbiguous
	} else if !errors.Is(err, ErrInvocationRuntimeAttemptMissing) {
		return true, err
	}
	_, current, err := lockWorkspacePreparationTarget(ctx, tx, claim, effect)
	if err != nil {
		return true, err
	}
	var generation int64
	err = tx.QueryRow(ctx, `SELECT preparation.grant_generation FROM invocation_workspace_preparations preparation JOIN invocation_workspace_completions completion USING(isolation_domain_id,operation_id,fencing_token) WHERE preparation.isolation_domain_id=$1 AND preparation.operation_id=$2 AND preparation.fencing_token=$3`, claim.IsolationDomainID, claim.ID, claim.FencingToken).Scan(&generation)
	if err != nil {
		return true, ErrWorkspacePreparation
	}
	current.GrantGeneration = generation
	if err := verifyWorkspacePreparation(ctx, tx, claim, effect, current); err != nil {
		return true, err
	}
	return true, nil
}

func workspacePreparationResourceID(claim OperationClaim) string {
	return claim.ID + ":" + strconv.FormatInt(claim.FencingToken, 10)
}
