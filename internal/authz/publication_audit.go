package authz

import "context"

const PublicationDecisionContract = "dataground.publication-authorization-decision/v1"

// PublicationRequest contains accepted identity and bounded reviewed facts.
// Public callers must derive ActorID from authentication, never from a body.
// It deliberately has no invocation identity or native runtime fields.
type PublicationRequest struct {
	ActorID            string
	IsolationDomainID  string
	OperationID        string
	ServiceID          string
	RevisionID         string
	CorrelationID      string
	Phase              string
	ExpectedVersion    int64
	FencingToken       int64
	PlanDigest         string
	VerificationDigest string
	PolicyDigest       string
}

func (request PublicationRequest) Valid() bool {
	return validInvocationActorID(request.ActorID) && domainIDPattern.MatchString(request.IsolationDomainID) &&
		invocationAuditOperationIDPattern.MatchString(request.OperationID) && invocationAuditServiceIDPattern.MatchString(request.ServiceID) && invocationAuditRevisionIDPattern.MatchString(request.RevisionID) &&
		correlationIDPattern.MatchString(request.CorrelationID) && (request.Phase == "entry" || request.Phase == "effect") &&
		(request.Phase == "entry" && request.FencingToken == 0 || request.Phase == "effect" && request.FencingToken > 0 && request.FencingToken <= 9007199254740991) &&
		request.ExpectedVersion > 0 && request.ExpectedVersion <= 9007199254740991 && policyDigestPattern.MatchString(request.PlanDigest) && policyDigestPattern.MatchString(request.VerificationDigest) && policyDigestPattern.MatchString(request.PolicyDigest)
}

// Publication decisions retain their own scope and phases. They must not be
// flattened into the existing invocation or public API decision export streams.
type PublicationDecisionRecord struct {
	Request        PublicationRequest
	PolicyContract string
	PolicySetID    string
	Outcome        Outcome
}

func (record PublicationDecisionRecord) Valid() bool {
	if !record.Request.Valid() || !policySetIDPattern.MatchString(record.PolicySetID) || !validOutcome(record.Outcome) {
		return false
	}
	switch record.PolicyContract {
	case "dataground.invocation-authorization-policy/v1", "dataground.invocation-authorization-policy/v2", "dataground.invocation-authorization-policy/v3", "dataground.invocation-authorization-policy/v4":
		return record.Outcome != OutcomeAllowed
	case "dataground.invocation-authorization-policy/v5":
		return true
	default:
		return false
	}
}

type PublicationDecisionRecorder interface {
	RecordPublicationAuthorizationDecision(context.Context, PublicationDecisionRecord) error
}
