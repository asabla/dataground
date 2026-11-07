package persistence

import (
	"context"
	"errors"

	"github.com/asabla/dataground/internal/domain"
)

// ReadWorkspaceRetention requires separate invocation-scoped metadata authority.
// One scoped query reads recorded evidence without accessing source or artifact bytes.
func (repository *Repository) ReadWorkspaceRetention(ctx context.Context, scope, invocationID string) (domain.WorkspaceRetention, error) {
	if !repository.Configured() || ctx == nil || !artifactListScopePattern.MatchString(scope) || !artifactListInvocationPattern.MatchString(invocationID) {
		return domain.WorkspaceRetention{}, errors.New("workspace retention read is invalid")
	}
	value := domain.WorkspaceRetention{SchemaVersion: "dataground.invocation-workspace-retention/v1", IsolationDomainID: scope, InvocationID: invocationID}
	err := repository.pool.QueryRow(ctx, `SELECT
 CASE WHEN completion.operation_id IS NOT NULL THEN 'recorded'
      WHEN instance.id IS NULL THEN 'not_observed'
      WHEN stopped.operation_id IS NOT NULL OR cleanup.operation_id IS NOT NULL THEN 'export_pending'
      ELSE 'stop_unconfirmed' END,
 CASE WHEN operation.command='cancel' AND operation.observed_state='failed' AND operation.error->>'code'='OPERATION_DEADLINE_EXCEEDED' THEN 'review_required'
      WHEN operation.command='cancel' AND operation.observed_state='cancelling' AND EXISTS(
       SELECT 1 FROM workspace_retention_recoveries recovery WHERE recovery.isolation_domain_id=operation.isolation_domain_id AND recovery.operation_id=operation.id AND recovery.correlation_id=operation.effect_correlation_id
      ) THEN 'requested' ELSE 'none' END,
 COALESCE(completion.artifact_id,''),completion.completed_at
 FROM invocations invocation
 JOIN invocation_execution_operations operation ON operation.isolation_domain_id=invocation.isolation_domain_id AND operation.invocation_id=invocation.id
 JOIN service_revision_execution_plans plan ON plan.isolation_domain_id=invocation.isolation_domain_id AND plan.revision_id=invocation.revision_id AND plan.workspace_source IS NOT NULL
 LEFT JOIN execution_instances instance ON instance.isolation_domain_id=operation.isolation_domain_id AND instance.operation_id=operation.id
 LEFT JOIN invocation_workspace_owner_stop_receipts stopped ON stopped.isolation_domain_id=operation.isolation_domain_id AND stopped.operation_id=operation.id
 LEFT JOIN invocation_workspace_quiescence_completions cleanup ON cleanup.isolation_domain_id=operation.isolation_domain_id AND cleanup.operation_id=operation.id
 LEFT JOIN invocation_workspace_change_completions completion ON completion.isolation_domain_id=operation.isolation_domain_id AND completion.operation_id=operation.id
 WHERE invocation.isolation_domain_id=$1 AND invocation.id=$2`, scope, invocationID).Scan(&value.State, &value.Recovery, &value.ArtifactID, &value.RecordedAt)
	return value, err
}
