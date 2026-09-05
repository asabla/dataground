package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	cedar "github.com/cedar-policy/cedar-go"
)

func questionAuthorizationFixture() persistence.InvocationRuntimeQuestion {
	text := "private answer sentinel"
	return persistence.InvocationRuntimeQuestion{
		ID: "qst_00000000000000000001", IsolationDomainID: "iso_00000000000000000001", OperationID: "op_00000000000000000001", InvocationID: "inv_00000000000000000001", ServiceID: "svc_00000000000000000001", RevisionID: "rev_00000000000000000001",
		Version: 1, AnsweredBy: "controller", AnswerCorrelationID: "cor_00000000000000000001",
		Prompts: []domain.QuestionPrompt{{ID: "item_1", Title: "Target", Prompt: "private prompt sentinel", AllowFreeText: true}},
		Answers: []domain.QuestionAnswer{{QuestionID: "item_1", Text: &text}},
	}
}
func questionAuthorizationPolicy(t *testing.T, policies string) InvocationAuthorizationPolicy {
	t.Helper()
	question := questionAuthorizationFixture()
	var entities cedar.EntityMap
	if err := json.Unmarshal([]byte(`[{"uid":{"type":"DataGround::Actor","id":"controller"},"attrs":{},"parents":[]}]`), &entities); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(entities)
	if err != nil {
		t.Fatal(err)
	}
	value, err := NewInvocationAuthorizationPolicyWithQuestionEntities(InvocationAuthorizationPolicyScope{IsolationDomainID: question.IsolationDomainID, ServiceID: question.ServiceID, RevisionID: question.RevisionID}, "question-policy", CanonicalInvocationCedarQuestionSchema(), []byte(policies), encoded)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func questionAuthorizationInput(t *testing.T) InvocationCedarInput {
	t.Helper()
	var request InvocationAuthorizationRequest
	authorizer, err := NewInvocationAuthorizer(invocationAuthorizationDecisionFunc(func(_ context.Context, value InvocationAuthorizationRequest) error { request = value; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := authorizer.AuthorizeInvocationQuestion(context.Background(), questionAuthorizationFixture(), persistence.InvocationQuestionEntry); err != nil {
		t.Fatal(err)
	}
	input, err := mapInvocationCedarInput(request)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func TestInvocationQuestionPolicyBindsActorQuestionVersionAndPhase(t *testing.T) {
	t.Parallel()
	policy := questionAuthorizationPolicy(t, `permit(principal == DataGround::Actor::"controller", action == DataGround::Action::"answer", resource) when {
 context.question.questionID == "qst_00000000000000000001" && context.question.version == 1 && context.question.phase == "entry" && context.question.questionCount == 1 && context.question.freeTextCount == 1 && context.question.selectedOptionCount == 0
 };`)
	authorizer, err := NewStaticCedarInvocationAuthorizer([]InvocationAuthorizationPolicy{policy})
	if err != nil {
		t.Fatal(err)
	}
	if err := authorizer.AuthorizeInvocationQuestion(context.Background(), questionAuthorizationFixture(), persistence.InvocationQuestionEntry); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*persistence.InvocationRuntimeQuestion)
		phase  string
	}{
		{name: "other actor", mutate: func(q *persistence.InvocationRuntimeQuestion) { q.AnsweredBy = "observer" }, phase: "entry"},
		{name: "other question", mutate: func(q *persistence.InvocationRuntimeQuestion) { q.ID = "qst_00000000000000000002" }, phase: "entry"},
		{name: "other version", mutate: func(q *persistence.InvocationRuntimeQuestion) { q.Version++ }, phase: "entry"},
		{name: "effect separately denied", mutate: func(*persistence.InvocationRuntimeQuestion) {}, phase: "effect"},
	} {
		t.Run(test.name, func(t *testing.T) {
			question := questionAuthorizationFixture()
			test.mutate(&question)
			if err := authorizer.AuthorizeInvocationQuestion(context.Background(), question, test.phase); !errors.Is(err, ErrInvocationQuestionDenied) {
				t.Fatalf("question authorization: %v", err)
			}
		})
	}
	input := questionAuthorizationInput(t)
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "sentinel") || input.Contract != InvocationCedarQuestionContract {
		t.Fatal("question policy exposed content or used the legacy contract")
	}
	cloned, err := cloneInvocationCedarInput(input)
	if err != nil {
		t.Fatal(err)
	}
	cloned.Question.Phase = "effect"
	if input.Question.Phase != "entry" {
		t.Fatal("question input retained mutable alias")
	}
}

func TestOlderInvocationPoliciesCannotAcquireQuestionAuthority(t *testing.T) {
	t.Parallel()
	current := questionAuthorizationPolicy(t, `permit(principal,action,resource);`)
	scope := InvocationAuthorizationPolicyScope{IsolationDomainID: current.IsolationDomainID, ServiceID: current.ServiceID, RevisionID: current.RevisionID}
	legacy1, err := NewInvocationAuthorizationPolicy(scope, "legacy", CanonicalInvocationCedarSchema(), current.Policies)
	if err != nil {
		t.Fatal(err)
	}
	legacy2, err := NewInvocationAuthorizationPolicyWithEntities(scope, "legacy", CanonicalInvocationCedarEntitySchema(), current.Policies, current.Entities)
	if err != nil {
		t.Fatal(err)
	}
	legacy3, err := NewInvocationAuthorizationPolicyWithApprovalEntities(scope, "legacy", CanonicalInvocationCedarApprovalSchema(), current.Policies, current.Entities)
	if err != nil {
		t.Fatal(err)
	}
	input := questionAuthorizationInput(t)
	evaluator := NewCedarInvocationAuthorizationEvaluator()
	for _, policy := range []InvocationAuthorizationPolicy{legacy1, legacy2, legacy3} {
		if err := evaluator.EvaluateInvocationAuthorization(context.Background(), policy, input); !errors.Is(err, ErrInvocationAuthorizationDenied) {
			t.Fatalf("legacy wildcard allowed question: %s, %v", policy.Contract, err)
		}
	}
	forged := current
	forged.Contract = InvocationAuthorizationPolicyApprovalContract
	if validInvocationAuthorizationPolicy(forged, scope) {
		t.Fatal("v4 digest accepted as v3")
	}
	wrong := input
	wrong.Contract = InvocationCedarContract
	if validInvocationCedarInput(wrong) {
		t.Fatal("question accepted in legacy input contract")
	}
}

func TestQuestionRuntimePermissionBindsExactTimeout(t *testing.T) {
	t.Parallel()
	policy := questionAuthorizationPolicy(t, `permit(principal,action == DataGround::Action::"run",resource) when { context.runtime has questionTimeoutMillis && context.runtime.questionTimeoutMillis <= 1000 };`)
	authorizer, err := NewStaticCedarInvocationAuthorizer([]InvocationAuthorizationPolicy{policy})
	if err != nil {
		t.Fatal(err)
	}
	q := questionAuthorizationFixture()
	target := persistence.InvocationRuntimeTarget{IsolationDomainID: q.IsolationDomainID, OperationID: q.OperationID, InvocationID: q.InvocationID, ServiceID: q.ServiceID, RevisionID: q.RevisionID, ActorID: q.AnsweredBy, CorrelationID: q.AnswerCorrelationID}
	request := dgruntime.StartRequest{ApprovalMode: dgruntime.ApprovalLocked, SandboxMode: dgruntime.SandboxWorkspaceWrite, QuestionMode: dgruntime.QuestionInteractive, QuestionTimeout: time.Second}
	if err := authorizer.AuthorizeInvocationRuntime(context.Background(), target, request); err != nil {
		t.Fatal(err)
	}
	request.QuestionTimeout = 2 * time.Second
	if err := authorizer.AuthorizeInvocationRuntime(context.Background(), target, request); !errors.Is(err, ErrInvocationRuntimeDenied) {
		t.Fatalf("wider timeout: %v", err)
	}
	request.QuestionTimeout = time.Second + time.Nanosecond
	if err := authorizer.AuthorizeInvocationRuntime(context.Background(), target, request); !errors.Is(err, ErrInvocationAuthorizationInvalid) {
		t.Fatalf("rounded timeout: %v", err)
	}
	request.QuestionMode = dgruntime.QuestionDisabled
	if err := authorizer.AuthorizeInvocationRuntime(context.Background(), target, request); !errors.Is(err, ErrInvocationAuthorizationInvalid) {
		t.Fatalf("disabled mode with timeout: %v", err)
	}
}

type questionDecisionRecorderStub struct {
	invocationDecisionRecorderStub
	questions   []authz.InvocationQuestionDecisionRecord
	questionErr error
}

func (recorder *questionDecisionRecorderStub) RecordInvocationQuestionAuthorizationDecision(_ context.Context, record authz.InvocationQuestionDecisionRecord) error {
	recorder.questions = append(recorder.questions, record)
	return recorder.questionErr
}

func TestQuestionAuditRequiresExactDecisionStream(t *testing.T) {
	t.Parallel()
	input := questionAuthorizationInput(t)
	policy := questionAuthorizationPolicy(t, `permit(principal,action,resource);`)
	legacyRecorder := &invocationDecisionRecorderStub{}
	evaluator, err := NewAuditedInvocationCedarEvaluator(NewCedarInvocationAuthorizationEvaluator(), legacyRecorder)
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.EvaluateInvocationAuthorization(context.Background(), policy, input); !errors.Is(err, ErrInvocationAuthorizationPolicyUnavailable) {
		t.Fatalf("missing question recorder: %v", err)
	}
	if len(legacyRecorder.records) != 0 {
		t.Fatal("question decision flattened into legacy stream")
	}
	for _, outcome := range []string{"allowed", "denied", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			recorder := &questionDecisionRecorderStub{}
			delegate := invocationCedarEvaluatorFunc(func(context.Context, InvocationAuthorizationPolicy, InvocationCedarInput) error {
				switch outcome {
				case "denied":
					return ErrInvocationAuthorizationDenied
				case "unavailable":
					return errors.New("upstream detail")
				default:
					return nil
				}
			})
			audited, err := NewAuditedInvocationCedarEvaluator(delegate, recorder)
			if err != nil {
				t.Fatal(err)
			}
			_ = audited.EvaluateInvocationAuthorization(context.Background(), policy, input)
			if len(recorder.questions) != 1 || len(recorder.records) != 0 {
				t.Fatal("incorrect question audit stream")
			}
			record := recorder.questions[0]
			if !record.Valid() || record.QuestionID != input.Question.ID || record.QuestionVersion != input.Question.Version || record.Phase != input.Question.Phase || record.Invocation.Outcome != authz.Outcome(outcome) || record.PolicyContract != policy.Contract {
				t.Fatal("question audit binding lost")
			}
			recorder.questionErr = errors.New("audit unavailable")
			if err := audited.EvaluateInvocationAuthorization(context.Background(), policy, input); !errors.Is(err, ErrInvocationAuthorizationPolicyUnavailable) {
				t.Fatalf("audit failure released decision: %v", err)
			}
		})
	}
}
