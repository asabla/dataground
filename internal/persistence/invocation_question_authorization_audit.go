package persistence

import (
	"context"

	"github.com/asabla/dataground/internal/authz"
)

// A completed policy decision commits independently of the answer transaction.
// Verify the immutable question scope with a snapshot read: a foreign-key row
// lock would wait on the caller's question lock while that caller awaits audit.
func (repository *Repository) RecordInvocationQuestionAuthorizationDecision(ctx context.Context, record authz.InvocationQuestionDecisionRecord) error {
	if repository == nil || repository.pool == nil || ctx == nil || !record.Valid() {
		return ErrInvocationAuthorizationDecisionInvalid
	}
	invocation := record.Invocation
	result, err := repository.pool.Exec(ctx, `INSERT INTO invocation_question_authorization_decisions (
 contract,isolation_domain_id,operation_id,invocation_id,service_id,revision_id,question_id,question_version,phase,actor_id,action,outcome,
 policy_contract,policy_set_id,policy_digest,correlation_id,question_count,free_text_count,selected_option_count)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'answer',$11,$12,$13,$14,$15,$16,$17,$18
 FROM invocation_runtime_questions question
 WHERE question.isolation_domain_id=$2 AND question.operation_id=$3 AND question.invocation_id=$4 AND question.service_id=$5 AND question.revision_id=$6
 AND question.id=$7 AND question.version=$8 AND jsonb_array_length(question.prompts)=$16`,
		authz.InvocationQuestionDecisionContract, invocation.IsolationDomainID, invocation.OperationID, invocation.InvocationID, invocation.ServiceID, invocation.RevisionID,
		record.QuestionID, record.QuestionVersion, record.Phase, invocation.ActorID, string(invocation.Outcome), record.PolicyContract, invocation.PolicySetID, invocation.PolicyDigest, invocation.CorrelationID,
		record.QuestionCount, record.FreeTextCount, record.SelectedOptionCount)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrInvocationAuthorizationDecisionInvalid
	}
	return nil
}

var _ authz.InvocationQuestionDecisionRecorder = (*Repository)(nil)
