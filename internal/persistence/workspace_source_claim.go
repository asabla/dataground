package persistence

import (
	"context"
	"github.com/asabla/dataground/internal/authz"
	"reflect"
	"time"

	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5"
)

// ReadClaimedWorkspaceSource binds a private source read to a reserved runtime
// attempt. The caller's source pin must match the immutable revision plan.
// A required pure evaluator checks the active Cedar policy. This method neither starts
// a runtime nor transfers content into its sandbox.
func (repository *Repository) ReadClaimedWorkspaceSource(ctx context.Context, claim OperationClaim, effect EffectRecord, objects workspace.SourceObjectReader, record workspace.SourceRecord, generation int64, evaluator SourceReadEvaluator) (*workspace.Snapshot, error) {
	if ctx == nil || objects == nil || missingSourceReadEvaluator(evaluator) || !validInvocationRuntimeAttempt(claim, effect) || record.IsolationDomainID != claim.IsolationDomainID {
		return nil, workspace.ErrSourceInvalid
	}
	if !repository.Configured() {
		return nil, workspace.ErrSourceUnavailable
	}
	bound := &invocationSourceAuthorizer{repository: repository, claim: claim, effect: effect, evaluator: evaluator}
	use, expires, err := bound.sourceUse(ctx, record, generation)
	if err != nil {
		return nil, err
	}
	// Use database lease and operation deadlines, never caller-carried timestamps.
	// Renewal cannot extend a read already admitted under an earlier lease window.
	readCtx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	return workspace.ReadSource(readCtx, repository, objects, bound, use)
}

func (bound *invocationSourceAuthorizer) sourceUse(ctx context.Context, record workspace.SourceRecord, generation int64) (workspace.SourceUse, time.Time, error) {
	tx, err := bound.repository.pool.Begin(ctx)
	if err != nil {
		return workspace.SourceUse{}, time.Time{}, workspace.ErrSourceUnavailable
	}
	defer tx.Rollback(ctx)
	target, expires, err := bound.lock(ctx, tx)
	if err != nil {
		return workspace.SourceUse{}, time.Time{}, err
	}
	if bound.record != record {
		return workspace.SourceUse{}, time.Time{}, workspace.ErrSourceUnauthorized
	}
	use := workspace.SourceUse{Contract: workspace.SourceUseV1, Record: bound.record, RevisionID: target.RevisionID, OperationID: target.OperationID, GrantGeneration: generation, ActorID: target.ActorID, CorrelationID: target.CorrelationID}
	if !use.Valid() {
		return workspace.SourceUse{}, time.Time{}, workspace.ErrSourceInvalid
	}
	if err := tx.Commit(ctx); err != nil {
		return workspace.SourceUse{}, time.Time{}, workspace.ErrSourceUnavailable
	}
	bound.readExpires = expires
	return use, expires, nil
}

type invocationSourceAuthorizer struct {
	repository      *Repository
	claim           OperationClaim
	effect          EffectRecord
	record          workspace.SourceRecord
	planDigest      string
	evaluator       SourceReadEvaluator
	preparation     *InvocationWorkspacePreparation
	quiescence      *InvocationWorkspaceQuiescence
	completion      bool
	deliveryExpires time.Time
	readExpires     time.Time
	currentExpires  time.Time
}

func (bound *invocationSourceAuthorizer) AuthorizeWorkspaceSource(ctx context.Context, use workspace.SourceUse, phase string) (workspace.SourcePermit, error) {
	permit, err := bound.repository.authorizeWorkspaceSource(ctx, use, phase, bound)
	if err == nil {
		bound.deliveryExpires = permit.ExpiresAt
		if bound.readExpires.Before(bound.deliveryExpires) {
			bound.deliveryExpires = bound.readExpires
		}
		if bound.currentExpires.Before(bound.deliveryExpires) {
			bound.deliveryExpires = bound.currentExpires
		}
	}
	return permit, err
}
func (bound *invocationSourceAuthorizer) lock(ctx context.Context, tx pgx.Tx) (InvocationRuntimeTarget, time.Time, error) {
	if bound.quiescence != nil {
		target, expires, err := lockWorkspaceExportTarget(ctx, tx, bound.claim, bound.effect, *bound.quiescence)
		if err != nil {
			return InvocationRuntimeTarget{}, time.Time{}, workspace.ErrSourceUnauthorized
		}
		bound.record, bound.planDigest = bound.quiescence.Source, bound.quiescence.PlanDigest
		return target, expires, nil
	}

	if bound.preparation != nil {
		target, current, err := lockWorkspacePreparationTarget(ctx, tx, bound.claim, bound.effect)
		if err != nil {
			return target, time.Time{}, err
		}
		current.GrantGeneration = bound.preparation.GrantGeneration
		if current.Execution != bound.preparation.Execution || current.Source != bound.preparation.Source || current.PlanDigest != bound.preparation.PlanDigest {
			return target, time.Time{}, ErrWorkspacePreparation
		}
		if err := verifyWorkspacePreparation(ctx, tx, bound.claim, bound.effect, current); err != nil {
			return target, time.Time{}, err
		}
		bound.record, bound.planDigest = current.Source, current.PlanDigest
		return target, current.ExpiresAt, nil
	}
	if err := lockRuntimeInteractionAttempt(ctx, tx, bound.claim, bound.effect); err != nil {
		return InvocationRuntimeTarget{}, time.Time{}, workspace.ErrSourceUnauthorized
	}
	target, err := getInvocationRuntimeTarget(ctx, tx, bound.claim.IsolationDomainID, bound.claim.ID)
	if err != nil || target.InvocationID != bound.claim.ResourceID {
		return InvocationRuntimeTarget{}, time.Time{}, workspace.ErrSourceUnauthorized
	}
	plan, err := executionpostgres.ReadExecutionPlan(ctx, tx, target.IsolationDomainID, target.RevisionID)
	if err != nil || plan.SchemaVersion != execution.ExecutionPlanSchemaV2 || plan.WorkspaceSource == nil || plan.IsolationDomainID != target.IsolationDomainID || plan.RevisionID != target.RevisionID {
		return InvocationRuntimeTarget{}, time.Time{}, workspace.ErrSourceUnauthorized
	}
	if bound.record != (workspace.SourceRecord{}) && bound.record != *plan.WorkspaceSource {
		return InvocationRuntimeTarget{}, time.Time{}, workspace.ErrSourceUnauthorized
	}
	bound.record = *plan.WorkspaceSource
	bound.planDigest, err = execution.DigestExecutionPlan(plan)
	if err != nil {
		return InvocationRuntimeTarget{}, time.Time{}, workspace.ErrSourceUnauthorized
	}
	var expires time.Time
	if err := tx.QueryRow(ctx, `SELECT LEAST(lease_expires_at,deadline_at) FROM invocation_execution_operations WHERE isolation_domain_id=$1 AND id=$2`, bound.claim.IsolationDomainID, bound.claim.ID).Scan(&expires); err != nil {
		return InvocationRuntimeTarget{}, time.Time{}, workspace.ErrSourceUnavailable
	}
	return target, expires, nil
}

// SourceReadEvaluator cannot perform durable authorization itself. The repository
// owns policy resolution and retains the completed result with the grant decision.
type SourceReadEvaluator interface {
	EvaluateSourceRead(context.Context, InvocationAuthorizationPolicyRecord, authz.SourceReadRequest) authz.Outcome
}

func missingSourceReadEvaluator(v SourceReadEvaluator) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Func, reflect.Slice, reflect.Chan:
		return r.IsNil()
	}
	return false
}

// ReadExportWorkspaceSource reads the original base only after exact confirmed
// cleanup. It accepts a current replacement claim without granting another turn.
// Source grants and Cedar read authority are checked at admission and delivery;
// callers still need separate current export and accepted-image authority.
func (repository *Repository) ReadExportWorkspaceSource(ctx context.Context, claim OperationClaim, effect EffectRecord, objects workspace.SourceObjectReader, expected InvocationWorkspaceQuiescence, generation int64, evaluator SourceReadEvaluator) (*workspace.Snapshot, time.Time, error) {
	if ctx == nil || objects == nil || missingSourceReadEvaluator(evaluator) || !validInvocationRuntimeAttempt(claim, effect) || !workspace.ValidSourceRecord(expected.Source) {
		return nil, time.Time{}, workspace.ErrSourceInvalid
	}
	if !repository.Configured() {
		return nil, time.Time{}, workspace.ErrSourceUnavailable
	}
	bound := &invocationSourceAuthorizer{repository: repository, claim: claim, effect: effect, evaluator: evaluator, quiescence: &expected}
	use, expires, err := bound.sourceUse(ctx, expected.Source, generation)
	if err != nil {
		return nil, time.Time{}, err
	}
	readCtx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	snapshot, err := workspace.ReadSource(readCtx, repository, objects, bound, use)
	if err != nil {
		return nil, time.Time{}, err
	}
	return snapshot, bound.deliveryExpires, nil
}
