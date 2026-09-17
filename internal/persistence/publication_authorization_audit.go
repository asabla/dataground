package persistence

import (
	"context"

	"github.com/asabla/dataground/internal/authz"
)

// A snapshot read avoids a foreign-key lock against the publication transaction
// while that transaction waits for its independently committed decision audit.
func (repository *Repository) RecordPublicationAuthorizationDecision(ctx context.Context, record authz.PublicationDecisionRecord) error {
	if repository == nil || !repository.Configured() || ctx == nil || !record.Valid() {
		return ErrDevelopmentPublicationUnavailable
	}
	request := record.Request
	result, err := repository.pool.Exec(ctx, `INSERT INTO publication_authorization_decisions (
        contract,isolation_domain_id,service_id,revision_id,operation_id,actor_id,correlation_id,phase,expected_version,fencing_token,
        plan_digest,verification_digest,policy_contract,policy_set_id,policy_digest,outcome)
    SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16
    FROM service_revisions revision WHERE revision.isolation_domain_id=$2 AND revision.service_id=$3 AND revision.id=$4
      AND ($8='entry' OR EXISTS (
        SELECT 1 FROM service_publication_operations operation JOIN governed_publication_requests request
          ON request.isolation_domain_id=operation.isolation_domain_id AND request.operation_id=operation.id
        WHERE operation.isolation_domain_id=$2 AND operation.id=$5 AND operation.revision_id=$4 AND operation.state_machine_version IN (3,4)
          AND operation.observed_state='validating' AND operation.lease_token=$10 AND operation.lease_expires_at>clock_timestamp() AND operation.deadline_at>clock_timestamp()
          AND COALESCE(operation.effect_actor_id,operation.actor_id)=$6 AND COALESCE(operation.effect_correlation_id,operation.correlation_id)=$7
          AND request.service_id=$3 AND request.revision_id=$4 AND request.expected_version=$9
          AND request.plan_digest=$11 AND request.verification_digest=$12 AND request.policy_digest=$15
      ))`, authz.PublicationDecisionContract, request.IsolationDomainID, request.ServiceID, request.RevisionID, request.OperationID, request.ActorID, request.CorrelationID, request.Phase, request.ExpectedVersion, request.FencingToken, request.PlanDigest, request.VerificationDigest, record.PolicyContract, record.PolicySetID, request.PolicyDigest, string(record.Outcome))
	if err != nil || result.RowsAffected() != 1 {
		return ErrDevelopmentPublicationUnavailable
	}
	return nil
}

var _ authz.PublicationDecisionRecorder = (*Repository)(nil)
