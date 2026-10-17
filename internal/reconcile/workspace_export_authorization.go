package reconcile

import (
	"context"

	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/persistence"
	cedar "github.com/cedar-policy/cedar-go"
)

// WorkspaceExportCedarEvaluator is pure. The repository owns current claim and
// policy locks and commits the complete decision before any export is admitted.
type WorkspaceExportCedarEvaluator struct{}

func (*WorkspaceExportCedarEvaluator) EvaluateWorkspaceChangeExport(ctx context.Context, record persistence.InvocationAuthorizationPolicyRecord, request authz.WorkspaceChangeExportRequest) authz.Outcome {
	if ctx == nil || ctx.Err() != nil || !request.Valid() || !record.Valid() {
		return authz.OutcomeUnavailable
	}
	scope := InvocationAuthorizationPolicyScope{IsolationDomainID: request.Source.IsolationDomainID, ServiceID: request.ServiceID, RevisionID: request.RevisionID}
	policy := InvocationAuthorizationPolicy{Contract: record.Contract, IsolationDomainID: record.IsolationDomainID, ServiceID: record.ServiceID, RevisionID: record.RevisionID, PolicySetID: record.PolicySetID, Schema: record.Schema, Policies: record.Policies, Entities: record.Entities}
	copy(policy.Digest[:], record.PolicyDigest)
	if !validInvocationAuthorizationPolicy(policy, scope) {
		return authz.OutcomeUnavailable
	}
	if policy.Contract != InvocationAuthorizationPolicyWorkspaceExportContract {
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
	r := request.Source
	decision, diagnostic := cedar.Authorize(policies, entities, cedar.Request{
		Principal: cedar.NewEntityUID("DataGround::Actor", cedar.String(request.ActorID)),
		Action:    cedar.NewEntityUID("DataGround::Action", "export-changes"),
		Resource:  cedar.NewEntityUID("DataGround::WorkspaceSource", cedar.String(r.IsolationDomainID+":"+r.WorkspaceID+":"+r.ID)),
		Context: cedar.NewRecord(cedar.RecordMap{
			"isolationDomainID": cedar.String(r.IsolationDomainID), "operationID": cedar.String(request.OperationID), "invocationID": cedar.String(request.InvocationID), "serviceID": cedar.String(request.ServiceID), "revisionID": cedar.String(request.RevisionID), "correlationID": cedar.String(request.CorrelationID),
			"workspaceID": cedar.String(r.WorkspaceID), "sourceID": cedar.String(r.ID), "archiveDigest": cedar.String(r.ArchiveDigest), "storageProfileDigest": cedar.String(r.StorageProfileDigest), "phase": cedar.String(request.Phase), "planDigest": cedar.String(request.PlanDigest), "effectID": cedar.String(request.EffectID), "fencingToken": cedar.Long(request.FencingToken),
			"executionID": cedar.String(request.ExecutionID), "ownerBinding": cedar.String(request.OwnerBinding), "runtimeToken": cedar.Long(request.RuntimeToken),
			"changes": cedar.NewRecord(cedar.RecordMap{"present": cedar.Boolean(request.Phase == "retain"), "digest": cedar.String(request.Changes.Digest), "resultArchiveDigest": cedar.String(request.Changes.ResultArchiveDigest), "sizeBytes": cedar.Long(request.Changes.SizeBytes), "changeCount": cedar.Long(request.Changes.ChangeCount), "entryCount": cedar.Long(request.Changes.EntryCount), "sourceBytes": cedar.Long(request.Changes.SourceBytes)}),
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

func (*WorkspaceExportCedarEvaluator) MarshalJSON() ([]byte, error) {
	return nil, ErrInvocationAuthorizationPolicyUnavailable
}
