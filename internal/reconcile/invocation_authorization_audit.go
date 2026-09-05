package reconcile

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/asabla/dataground/internal/authz"
)

// AuditedInvocationCedarEvaluator withholds every completed Cedar evaluation
// until its exact policy provenance and durable invocation scope are recorded.
// Policy resolution remains outside this boundary, so lookup failures cannot
// be mislabeled as completed decisions.
type AuditedInvocationCedarEvaluator struct {
	delegate InvocationCedarEvaluator
	recorder authz.InvocationDecisionRecorder
}

func NewAuditedInvocationCedarEvaluator(
	delegate InvocationCedarEvaluator,
	recorder authz.InvocationDecisionRecorder,
) (*AuditedInvocationCedarEvaluator, error) {
	if governedInvocationDependencyMissing(delegate) ||
		governedInvocationDependencyMissing(recorder) {
		return nil, errors.New("invocation authorization audit dependencies are required")
	}
	return &AuditedInvocationCedarEvaluator{delegate: delegate, recorder: recorder}, nil
}

func (evaluator *AuditedInvocationCedarEvaluator) EvaluateInvocationAuthorization(
	ctx context.Context,
	policy InvocationAuthorizationPolicy,
	input InvocationCedarInput,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if evaluator == nil ||
		governedInvocationDependencyMissing(evaluator.delegate) ||
		governedInvocationDependencyMissing(evaluator.recorder) ||
		!validInvocationCedarInput(input) ||
		!validInvocationAuthorizationPolicy(policy, InvocationAuthorizationPolicyScope{
			IsolationDomainID: input.IsolationDomainID,
			ServiceID:         input.ServiceID,
			RevisionID:        input.RevisionID,
		}) {
		return ErrInvocationAuthorizationPolicyUnavailable
	}

	err := evaluator.delegate.EvaluateInvocationAuthorization(ctx, policy, input)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	outcome, complete := invocationAuthorizationAuditOutcome(err)
	if !complete {
		return err
	}
	record := authz.InvocationDecisionRecord{
		ActorID:           input.Principal.ID,
		IsolationDomainID: input.IsolationDomainID,
		OperationID:       input.OperationID,
		InvocationID:      input.Resource.ID,
		ServiceID:         input.ServiceID,
		RevisionID:        input.RevisionID,
		Action:            authz.InvocationAction(input.Action.ID),
		Outcome:           outcome,
		PolicySetID:       policy.PolicySetID,
		PolicyDigest:      "sha256:" + hex.EncodeToString(policy.Digest[:]),
		CorrelationID:     input.CorrelationID,
	}
	if input.Question != nil {
		questionRecorder, ok := evaluator.recorder.(authz.InvocationQuestionDecisionRecorder)
		if !ok || governedInvocationDependencyMissing(questionRecorder) {
			return ErrInvocationAuthorizationPolicyUnavailable
		}
		questionRecord := authz.InvocationQuestionDecisionRecord{
			Invocation: record, PolicyContract: policy.Contract, QuestionID: input.Question.ID, QuestionVersion: input.Question.Version, Phase: input.Question.Phase,
			QuestionCount: input.Question.QuestionCount, FreeTextCount: input.Question.FreeTextCount, SelectedOptionCount: input.Question.SelectedOptionCount,
		}
		if !questionRecord.Valid() {
			return ErrInvocationAuthorizationPolicyUnavailable
		}
		if recordErr := questionRecorder.RecordInvocationQuestionAuthorizationDecision(ctx, questionRecord); recordErr != nil {
			return ErrInvocationAuthorizationPolicyUnavailable
		}
		return err
	}
	if !record.Valid() {
		return ErrInvocationAuthorizationPolicyUnavailable
	}
	if recordErr := evaluator.recorder.RecordInvocationAuthorizationDecision(ctx, record); recordErr != nil {
		return ErrInvocationAuthorizationPolicyUnavailable
	}
	return err
}

func (*AuditedInvocationCedarEvaluator) MarshalJSON() ([]byte, error) {
	return nil, errors.New("invocation authorization evaluators cannot be serialized")
}

func invocationAuthorizationAuditOutcome(err error) (authz.Outcome, bool) {
	switch {
	case err == nil:
		return authz.OutcomeAllowed, true
	case errors.Is(err, ErrInvocationAuthorizationDenied):
		return authz.OutcomeDenied, true
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "", false
	default:
		return authz.OutcomeUnavailable, true
	}
}

var _ InvocationCedarEvaluator = (*AuditedInvocationCedarEvaluator)(nil)
var _ json.Marshaler = (*AuditedInvocationCedarEvaluator)(nil)
