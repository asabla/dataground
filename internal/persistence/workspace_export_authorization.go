package persistence

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/workspace"
)

var ErrWorkspaceExportDenied = errors.New("workspace change export is not authorized")
var ErrWorkspaceExportUnavailable = errors.New("workspace change export authorization is unavailable")

type WorkspaceChangeExportEvaluator interface {
	EvaluateWorkspaceChangeExport(context.Context, InvocationAuthorizationPolicyRecord, authz.WorkspaceChangeExportRequest) authz.Outcome
}

type WorkspaceChangeExportPermit struct {
	DecisionID string
	ExpiresAt  time.Time
	Request    authz.WorkspaceChangeExportRequest
}

// AuthorizeInvocationWorkspaceChangeExport resolves current policy only after
// locking a live claim and its exact confirmed cleanup observation. Replacement
// workers may export the original stopped workspace; they acquire no new turn.
// Every completed policy outcome commits before the caller receives a permit.
// Callers must repeat this check immediately before capture and each retention
// effect; this method performs no content access and grants no teardown right.
func (repository *Repository) AuthorizeInvocationWorkspaceChangeExport(ctx context.Context, claim OperationClaim, effect EffectRecord, expected InvocationWorkspaceQuiescence, phase string, changes workspace.ChangeSetReceipt, evaluator WorkspaceChangeExportEvaluator) (WorkspaceChangeExportPermit, error) {
	if !repository.Configured() || ctx == nil || !validInvocationRuntimeAttempt(claim, effect) || expected.Source.IsolationDomainID != claim.IsolationDomainID || expected.OperationID != claim.ID || expected.InvocationID != claim.ResourceID || expected.EffectID != effect.EffectID || missingWorkspaceExportEvaluator(evaluator) {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportUnavailable
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportUnavailable
	}
	defer tx.Rollback(ctx)
	entry, found, err := readWorkspaceQuiescence(ctx, tx, claim.IsolationDomainID, claim.ID)
	if err != nil || !found || !entry.Confirmed || entry.Record != expected {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportDenied
	}
	if verifyInvocationRuntimeEffect(ctx, tx, effect) != nil {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportDenied
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportUnavailable
	}
	var expires time.Time
	if err := tx.QueryRow(ctx, `SELECT lock_workspace_export_authorization($1,$2,$3,$4,$5,$6,$7)`, claim.IsolationDomainID, claim.ID, claim.LeaseOwner, claim.FencingToken, claim.ActorID, claim.CorrelationID, encoded).Scan(&expires); err != nil {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportDenied
	}
	var service, revision, actor, correlation string
	if err := tx.QueryRow(ctx, `SELECT invocation.service_id,invocation.revision_id,COALESCE(operation.effect_actor_id,operation.actor_id),COALESCE(operation.effect_correlation_id,operation.correlation_id)
 FROM invocation_execution_operations operation JOIN invocations invocation ON invocation.isolation_domain_id=operation.isolation_domain_id AND invocation.id=operation.invocation_id
 WHERE operation.isolation_domain_id=$1 AND operation.id=$2`, claim.IsolationDomainID, claim.ID).Scan(&service, &revision, &actor, &correlation); err != nil {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportUnavailable
	}
	r := authz.WorkspaceChangeExportRequest{Source: expected.Source, ServiceID: service, RevisionID: revision, InvocationID: expected.InvocationID, OperationID: expected.OperationID, EffectID: expected.EffectID, ExecutionID: expected.ExecutionID, ActorID: actor, CorrelationID: correlation, PlanDigest: expected.PlanDigest, OwnerBinding: expected.OwnerBinding, RuntimeToken: expected.RuntimeToken, FencingToken: claim.FencingToken, Phase: phase, Changes: changes}
	if !r.Valid() {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportDenied
	}
	if lockInvocationAuthorizationPolicyScope(ctx, tx, claim.IsolationDomainID, service, revision) != nil {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportUnavailable
	}
	policy, err := getActiveInvocationAuthorizationPolicy(ctx, tx, claim.IsolationDomainID, service, revision)
	if err != nil && !errors.Is(err, ErrInvocationAuthorizationPolicyRecordMissing) {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportUnavailable
	}
	var policyContract, policySet, policyDigest any
	policyOutcome := authz.OutcomeDenied
	if err == nil {
		policyContract, policySet, policyDigest = policy.Contract, policy.PolicySetID, "sha256:"+hex.EncodeToString(policy.PolicyDigest)
		policyOutcome = evaluator.EvaluateWorkspaceChangeExport(ctx, cloneInvocationAuthorizationPolicyRecord(policy), r)
	}
	if ctx.Err() != nil || (policyOutcome != authz.OutcomeAllowed && policyOutcome != authz.OutcomeDenied && policyOutcome != authz.OutcomeUnavailable) {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportUnavailable
	}
	requestBytes, err := json.Marshal(r)
	if err != nil {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportUnavailable
	}
	id := identity.New("wed")
	var outcome string
	err = tx.QueryRow(ctx, `INSERT INTO workspace_change_export_decisions(isolation_domain_id,id,contract,operation_id,request,lease_owner,fencing_token,expires_at,policy_contract,policy_set_id,policy_digest,policy_outcome,outcome)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,CASE WHEN $12='allowed' AND $8::timestamptz>clock_timestamp() THEN 'allowed' WHEN $12='unavailable' THEN 'unavailable' ELSE 'denied' END) RETURNING outcome`, claim.IsolationDomainID, id, authz.WorkspaceChangeExportDecisionContract, claim.ID, requestBytes, claim.LeaseOwner, claim.FencingToken, expires, policyContract, policySet, policyDigest, string(policyOutcome)).Scan(&outcome)
	if err != nil || tx.Commit(ctx) != nil {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportUnavailable
	}
	if outcome == string(authz.OutcomeUnavailable) {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportUnavailable
	}
	if outcome != string(authz.OutcomeAllowed) || !time.Now().Before(expires) {
		return WorkspaceChangeExportPermit{}, ErrWorkspaceExportDenied
	}
	return WorkspaceChangeExportPermit{DecisionID: id, ExpiresAt: expires, Request: r}, nil
}

func missingWorkspaceExportEvaluator(value WorkspaceChangeExportEvaluator) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Func, reflect.Slice, reflect.Chan:
		return v.IsNil()
	}
	return false
}
