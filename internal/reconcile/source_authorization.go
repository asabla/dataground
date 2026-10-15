package reconcile

import (
	"context"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/persistence"
	cedar "github.com/cedar-policy/cedar-go"
)

// SourceCedarEvaluator is a pure evaluator. The repository owns the surrounding
// claim, policy and grant locks and atomically retains both decision outcomes.
type SourceCedarEvaluator struct{}

func (*SourceCedarEvaluator) EvaluateSourceRead(ctx context.Context, record persistence.InvocationAuthorizationPolicyRecord, request authz.SourceReadRequest) authz.Outcome {
	if ctx == nil || ctx.Err() != nil || !request.Valid() || !record.Valid() {
		return authz.OutcomeUnavailable
	}
	scope := InvocationAuthorizationPolicyScope{IsolationDomainID: request.Use.Record.IsolationDomainID, ServiceID: request.ServiceID, RevisionID: request.Use.RevisionID}
	policy := InvocationAuthorizationPolicy{Contract: record.Contract, IsolationDomainID: record.IsolationDomainID, ServiceID: record.ServiceID, RevisionID: record.RevisionID, PolicySetID: record.PolicySetID, Schema: record.Schema, Policies: record.Policies, Entities: record.Entities}
	copy(policy.Digest[:], record.PolicyDigest)
	if !validInvocationAuthorizationPolicy(policy, scope) {
		return authz.OutcomeUnavailable
	}
	// Contract opt-in is required even when an older policy permits all actions.
	if policy.Contract != InvocationAuthorizationPolicySourceContract {
		return authz.OutcomeDenied
	}
	policies, err := validatedInvocationCedarPolicySet(policy, scope)
	if err != nil {
		return authz.OutcomeUnavailable
	}
	entities, err := validatedInvocationCedarEntities(policy, scope)
	if err != nil {
		return authz.OutcomeUnavailable
	}
	r := request.Use.Record
	decision, diagnostic := cedar.Authorize(policies, entities, cedar.Request{
		Principal: cedar.NewEntityUID("DataGround::Actor", cedar.String(request.Use.ActorID)),
		Action:    cedar.NewEntityUID("DataGround::Action", "read-source"),
		Resource:  cedar.NewEntityUID("DataGround::WorkspaceSource", cedar.String(r.IsolationDomainID+":"+r.WorkspaceID+":"+r.ID)),
		Context: cedar.NewRecord(cedar.RecordMap{
			"isolationDomainID": cedar.String(r.IsolationDomainID), "operationID": cedar.String(request.Use.OperationID), "invocationID": cedar.String(request.InvocationID), "serviceID": cedar.String(request.ServiceID), "revisionID": cedar.String(request.Use.RevisionID), "correlationID": cedar.String(request.Use.CorrelationID),
			"workspaceID": cedar.String(r.WorkspaceID), "sourceID": cedar.String(r.ID), "archiveDigest": cedar.String(r.ArchiveDigest), "storageProfileDigest": cedar.String(r.StorageProfileDigest), "phase": cedar.String(request.Phase), "planDigest": cedar.String(request.PlanDigest), "effectID": cedar.String(request.EffectID), "grantGeneration": cedar.Long(request.Use.GrantGeneration), "fencingToken": cedar.Long(request.FencingToken),
		}),
	})
	if ctx.Err() != nil || len(diagnostic.Errors) != 0 {
		return authz.OutcomeUnavailable
	}
	if decision == cedar.Deny {
		return authz.OutcomeDenied
	}
	return authz.OutcomeAllowed
}

// This method prevents accidental serialization through diagnostic interfaces.
func (*SourceCedarEvaluator) MarshalJSON() ([]byte, error) {
	return nil, ErrInvocationAuthorizationPolicyUnavailable
}

var _ persistence.SourceReadEvaluator = (*SourceCedarEvaluator)(nil)
