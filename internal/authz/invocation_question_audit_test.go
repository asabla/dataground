package authz

import (
	"strings"
	"testing"
)

func TestQuestionAuditRecordRequiresClosedContextAndPolicyContract(t *testing.T) {
	t.Parallel()
	value := InvocationQuestionDecisionRecord{
		Invocation:     InvocationDecisionRecord{ActorID: "controller", IsolationDomainID: "iso_00000000000000000001", OperationID: "op_00000000000000000001", InvocationID: "inv_00000000000000000001", ServiceID: "svc_00000000000000000001", RevisionID: "rev_00000000000000000001", Action: InvocationAnswer, Outcome: OutcomeAllowed, PolicySetID: "question-policy", PolicyDigest: "sha256:" + strings.Repeat("a", 64), CorrelationID: "cor_00000000000000000001"},
		PolicyContract: "dataground.invocation-authorization-policy/v4", QuestionID: "qst_00000000000000000001", QuestionVersion: 1, Phase: "entry", QuestionCount: 1, FreeTextCount: 1,
	}
	if !value.Valid() {
		t.Fatal("valid question audit record rejected")
	}
	if value.Invocation.Valid() {
		t.Fatal("question record accepted by legacy invocation stream")
	}
	for name, mutate := range map[string]func(*InvocationQuestionDecisionRecord){
		"missing question":       func(r *InvocationQuestionDecisionRecord) { r.QuestionID = "" },
		"native id":              func(r *InvocationQuestionDecisionRecord) { r.QuestionID = "question-1" },
		"missing version":        func(r *InvocationQuestionDecisionRecord) { r.QuestionVersion = 0 },
		"unknown phase":          func(r *InvocationQuestionDecisionRecord) { r.Phase = "retry" },
		"scope missing":          func(r *InvocationQuestionDecisionRecord) { r.Invocation.IsolationDomainID = "" },
		"unknown action":         func(r *InvocationQuestionDecisionRecord) { r.Invocation.Action = InvocationApprove },
		"excess free text":       func(r *InvocationQuestionDecisionRecord) { r.FreeTextCount = 2 },
		"selected and free text": func(r *InvocationQuestionDecisionRecord) { r.SelectedOptionCount = 1 },
		"unknown policy":         func(r *InvocationQuestionDecisionRecord) { r.PolicyContract = "latest" },
		"legacy policy allowed": func(r *InvocationQuestionDecisionRecord) {
			r.PolicyContract = "dataground.invocation-authorization-policy/v3"
		},
	} {
		t.Run(name, func(t *testing.T) {
			record := value
			mutate(&record)
			if record.Valid() {
				t.Fatal("invalid question audit accepted")
			}
		})
	}
	value.PolicyContract = "dataground.invocation-authorization-policy/v3"
	value.Invocation.Outcome = OutcomeDenied
	if !value.Valid() {
		t.Fatal("legacy policy denial could not be audited")
	}
}
