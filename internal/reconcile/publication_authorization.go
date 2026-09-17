package reconcile

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/asabla/dataground/internal/authz"
	cedar "github.com/cedar-policy/cedar-go"
)

var (
	ErrPublicationAuthorizationDenied      = errors.New("publication authorization denied")
	ErrPublicationAuthorizationUnavailable = errors.New("publication authorization unavailable")
)

// PublicationAuthorizer uses the current exact-scope policy for both entry and
// effect checks. A caller must hold its lifecycle and policy fencing boundary
// across the effect check and publication commit; this evaluator grants no lease.
type PublicationAuthorizer struct {
	source   InvocationAuthorizationPolicySource
	recorder authz.PublicationDecisionRecorder
}

func NewPublicationAuthorizer(source InvocationAuthorizationPolicySource, recorder authz.PublicationDecisionRecorder) (*PublicationAuthorizer, error) {
	if governedInvocationDependencyMissing(source) || governedInvocationDependencyMissing(recorder) {
		return nil, ErrPublicationAuthorizationUnavailable
	}
	return &PublicationAuthorizer{source: source, recorder: recorder}, nil
}

func (authorizer *PublicationAuthorizer) AuthorizePublication(ctx context.Context, request authz.PublicationRequest) error {
	if ctx == nil || ctx.Err() != nil || authorizer == nil || governedInvocationDependencyMissing(authorizer.source) || governedInvocationDependencyMissing(authorizer.recorder) || !request.Valid() {
		return ErrPublicationAuthorizationUnavailable
	}
	scope := InvocationAuthorizationPolicyScope{IsolationDomainID: request.IsolationDomainID, ServiceID: request.ServiceID, RevisionID: request.RevisionID}
	policy, err := authorizer.source.ResolveInvocationAuthorizationPolicy(ctx, scope)
	if err != nil || !validInvocationAuthorizationPolicy(policy, scope) || "sha256:"+hex.EncodeToString(policy.Digest[:]) != request.PolicyDigest {
		return ErrPublicationAuthorizationUnavailable
	}
	policy = cloneInvocationAuthorizationPolicy(policy)
	outcome := authz.OutcomeAllowed
	evaluationErr := evaluatePublicationAuthorization(ctx, policy, request)
	switch {
	case ctx.Err() != nil:
		return ErrPublicationAuthorizationUnavailable
	case errors.Is(evaluationErr, ErrPublicationAuthorizationDenied):
		outcome = authz.OutcomeDenied
	case evaluationErr != nil:
		outcome = authz.OutcomeUnavailable
	}
	record := authz.PublicationDecisionRecord{Request: request, PolicyContract: policy.Contract, PolicySetID: policy.PolicySetID, Outcome: outcome}
	if !record.Valid() || authorizer.recorder.RecordPublicationAuthorizationDecision(ctx, record) != nil {
		return ErrPublicationAuthorizationUnavailable
	}
	if ctx.Err() != nil {
		return ErrPublicationAuthorizationUnavailable
	}
	return evaluationErr
}

func evaluatePublicationAuthorization(ctx context.Context, policy InvocationAuthorizationPolicy, request authz.PublicationRequest) error {
	// An older wildcard policy never gains publication authority. Selecting the
	// new contract is an explicit policy installation decision.
	if policy.Contract != InvocationAuthorizationPolicyPublicationContract {
		return ErrPublicationAuthorizationDenied
	}
	scope := InvocationAuthorizationPolicyScope{IsolationDomainID: request.IsolationDomainID, ServiceID: request.ServiceID, RevisionID: request.RevisionID}
	policies, err := validatedInvocationCedarPolicySet(policy, scope)
	if err != nil {
		return ErrPublicationAuthorizationUnavailable
	}
	entities, err := validatedInvocationCedarEntities(policy, scope)
	if err != nil {
		return ErrPublicationAuthorizationUnavailable
	}
	decision, diagnostic := cedar.Authorize(policies, entities, cedar.Request{
		Principal: cedar.NewEntityUID("DataGround::Actor", cedar.String(request.ActorID)),
		Action:    cedar.NewEntityUID("DataGround::Action", "publish"),
		Resource:  cedar.NewEntityUID("DataGround::ServiceRevision", cedar.String(request.RevisionID)),
		Context: cedar.NewRecord(cedar.RecordMap{
			"isolationDomainID": cedar.String(request.IsolationDomainID), "serviceID": cedar.String(request.ServiceID), "revisionID": cedar.String(request.RevisionID), "operationID": cedar.String(request.OperationID), "correlationID": cedar.String(request.CorrelationID),
			"fencingToken": cedar.Long(request.FencingToken), "phase": cedar.String(request.Phase), "expectedVersion": cedar.Long(request.ExpectedVersion), "planDigest": cedar.String(request.PlanDigest), "verificationDigest": cedar.String(request.VerificationDigest),
		}),
	})
	if ctx.Err() != nil || len(diagnostic.Errors) != 0 {
		return ErrPublicationAuthorizationUnavailable
	}
	if decision == cedar.Deny {
		return ErrPublicationAuthorizationDenied
	}
	return nil
}

func (*PublicationAuthorizer) MarshalJSON() ([]byte, error) {
	return nil, errors.New("publication authorizers cannot be serialized")
}
