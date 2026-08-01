package persistence

import (
	"context"
	"errors"
	"fmt"

	"github.com/asabla/dataground/internal/authz"
)

var ErrInvocationAuthorizationDecisionInvalid = errors.New("invocation authorization decision record is invalid")

func (repository *Repository) RecordInvocationAuthorizationDecision(
	ctx context.Context,
	record authz.InvocationDecisionRecord,
) error {
	if repository == nil || repository.pool == nil || !record.Valid() {
		return ErrInvocationAuthorizationDecisionInvalid
	}
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO invocation_authorization_decisions (
			isolation_domain_id,
			operation_id,
			invocation_id,
			service_id,
			revision_id,
			actor_id,
			action,
			outcome,
			policy_set_id,
			policy_digest,
			correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`,
		record.IsolationDomainID,
		record.OperationID,
		record.InvocationID,
		record.ServiceID,
		record.RevisionID,
		record.ActorID,
		string(record.Action),
		string(record.Outcome),
		record.PolicySetID,
		record.PolicyDigest,
		record.CorrelationID,
	); err != nil {
		return fmt.Errorf("record invocation authorization decision: %w", err)
	}
	return nil
}

var _ authz.InvocationDecisionRecorder = (*Repository)(nil)
