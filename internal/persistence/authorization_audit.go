package persistence

import (
	"context"
	"errors"
	"fmt"

	"github.com/asabla/dataground/internal/authz"
)

var ErrAuthorizationDecisionInvalid = errors.New("authorization decision record is invalid")

func (repository *Repository) RecordAuthorizationDecision(
	ctx context.Context,
	record authz.DecisionRecord,
) error {
	if repository == nil || repository.pool == nil || !record.Valid() {
		return ErrAuthorizationDecisionInvalid
	}
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO api_authorization_decisions (
			isolation_domain_id,
			principal_id,
			principal_kind,
			action,
			resource_type,
			resource_id,
			outcome,
			policy_set_id,
			policy_digest,
			correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		record.IsolationDomainID,
		record.PrincipalID,
		string(record.PrincipalKind),
		string(record.Action),
		string(record.ResourceType),
		record.ResourceID,
		string(record.Outcome),
		record.PolicySetID,
		record.PolicyDigest,
		record.CorrelationID,
	); err != nil {
		return fmt.Errorf("record API authorization decision: %w", err)
	}
	return nil
}

var _ authz.DecisionRecorder = (*Repository)(nil)
