package authz

import (
	"context"
	"regexp"
	"unicode"
	"unicode/utf8"
)

var (
	invocationAuditOperationIDPattern  = regexp.MustCompile(`^op_[0-9a-z]{20,32}$`)
	invocationAuditInvocationIDPattern = regexp.MustCompile(`^inv_[0-9a-z]{20,32}$`)
	invocationAuditServiceIDPattern    = regexp.MustCompile(`^svc_[0-9a-z]{20,32}$`)
	invocationAuditRevisionIDPattern   = regexp.MustCompile(`^rev_[0-9a-z]{20,32}$`)
)

type InvocationAction string

const (
	InvocationAdmit  InvocationAction = "admit"
	InvocationRun    InvocationAction = "run"
	InvocationCancel InvocationAction = "cancel"
)

type InvocationDecisionRecord struct {
	ActorID           string
	IsolationDomainID string
	OperationID       string
	InvocationID      string
	ServiceID         string
	RevisionID        string
	Action            InvocationAction
	Outcome           Outcome
	PolicySetID       string
	PolicyDigest      string
	CorrelationID     string
}

func (record InvocationDecisionRecord) Valid() bool {
	return validInvocationActorID(record.ActorID) &&
		domainIDPattern.MatchString(record.IsolationDomainID) &&
		invocationAuditOperationIDPattern.MatchString(record.OperationID) &&
		invocationAuditInvocationIDPattern.MatchString(record.InvocationID) &&
		invocationAuditServiceIDPattern.MatchString(record.ServiceID) &&
		invocationAuditRevisionIDPattern.MatchString(record.RevisionID) &&
		validInvocationAction(record.Action) &&
		validOutcome(record.Outcome) &&
		validPolicyDescriptor(PolicyDescriptor{
			PolicySetID: record.PolicySetID,
			Digest:      record.PolicyDigest,
		}) &&
		correlationIDPattern.MatchString(record.CorrelationID)
}

type InvocationDecisionRecorder interface {
	RecordInvocationAuthorizationDecision(context.Context, InvocationDecisionRecord) error
}

func validInvocationAction(action InvocationAction) bool {
	switch action {
	case InvocationAdmit, InvocationRun, InvocationCancel:
		return true
	default:
		return false
	}
}

func validInvocationActorID(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
