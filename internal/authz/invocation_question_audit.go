package authz

import (
	"context"
	"regexp"
)

const InvocationQuestionDecisionContract = "dataground.invocation-question-authorization-decision/v1"

var invocationQuestionAuditIDPattern = regexp.MustCompile(`^qst_[0-9a-z]{20,32}$`)

// Question decisions carry the exact question/version and phase. They are a
// separate append-only stream; the existing v1 export contract cannot express
// this context and must not silently flatten it into an invocation decision.
type InvocationQuestionDecisionRecord struct {
	Invocation          InvocationDecisionRecord
	PolicyContract      string
	QuestionID          string
	QuestionVersion     int64
	Phase               string
	QuestionCount       int
	FreeTextCount       int
	SelectedOptionCount int
}

func (record InvocationQuestionDecisionRecord) Valid() bool {
	if !record.Invocation.validContext() || record.Invocation.Action != InvocationAnswer ||
		!invocationQuestionAuditIDPattern.MatchString(record.QuestionID) || record.QuestionVersion < 1 ||
		(record.Phase != "entry" && record.Phase != "effect") || record.QuestionCount < 1 || record.QuestionCount > 3 ||
		record.FreeTextCount < 0 || record.FreeTextCount > record.QuestionCount ||
		record.SelectedOptionCount < record.QuestionCount-record.FreeTextCount || record.SelectedOptionCount > 4*(record.QuestionCount-record.FreeTextCount) {
		return false
	}
	switch record.PolicyContract {
	case "dataground.invocation-authorization-policy/v1", "dataground.invocation-authorization-policy/v2", "dataground.invocation-authorization-policy/v3":
		return record.Invocation.Outcome != OutcomeAllowed
	case "dataground.invocation-authorization-policy/v4":
		return true
	default:
		return false
	}
}

type InvocationQuestionDecisionRecorder interface {
	RecordInvocationQuestionAuthorizationDecision(context.Context, InvocationQuestionDecisionRecord) error
}
