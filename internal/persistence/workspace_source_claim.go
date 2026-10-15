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
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return nil, workspace.ErrSourceUnavailable
	}
	defer tx.Rollback(ctx)
	target, expires, err := bound.lock(ctx, tx)
	if err != nil {
		return nil, err
	}
	if bound.record != record {
		return nil, workspace.ErrSourceUnauthorized
	}
	use := workspace.SourceUse{Contract: workspace.SourceUseV1, Record: bound.record, RevisionID: target.RevisionID, OperationID: target.OperationID, GrantGeneration: generation, ActorID: target.ActorID, CorrelationID: target.CorrelationID}
	if !use.Valid() {
		return nil, workspace.ErrSourceInvalid
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, workspace.ErrSourceUnavailable
	}
	// Use database lease and operation deadlines, never caller-carried timestamps.
	// Renewal cannot extend a read already admitted under an earlier lease window.
	readCtx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()
	return workspace.ReadSource(readCtx, repository, objects, bound, use)
}

type invocationSourceAuthorizer struct {
	repository *Repository
	claim      OperationClaim
	effect     EffectRecord
	record     workspace.SourceRecord
	planDigest string
	evaluator  SourceReadEvaluator
}

func (bound *invocationSourceAuthorizer) AuthorizeWorkspaceSource(ctx context.Context, use workspace.SourceUse, phase string) (workspace.SourcePermit, error) {
	return bound.repository.authorizeWorkspaceSource(ctx, use, phase, bound)
}
func (bound *invocationSourceAuthorizer) lock(ctx context.Context, tx pgx.Tx) (InvocationRuntimeTarget, time.Time, error) {
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
