package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/asabla/dataground/internal/domain"
)

type ScopeInvocationListPage struct {
	Items   []domain.ScopeInvocationSummary
	HasMore bool
}

func ValidInvocationDiscoveryState(state string) bool {
	switch state {
	case "", "accepted", "running", "waiting", "cancelling", "succeeded", "failed", "cancelled", "unknown":
		return true
	default:
		return false
	}
}

// ListScopeInvocations requires the separate scope-wide metadata grant at its
// API entry. It does not confer service, invocation or content read authority.
func (repository *Repository) ListScopeInvocations(ctx context.Context, scope, state string, before *time.Time, beforeID string, limit int) (ScopeInvocationListPage, error) {
	if !repository.Configured() || scope == "" || !ValidInvocationDiscoveryState(state) || limit < 1 || limit > 100 || (before == nil) != (beforeID == "") || (before != nil && before.IsZero()) {
		return ScopeInvocationListPage{}, errors.New("scope invocation list request is invalid")
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT i.id, i.service_id, s.name, i.revision_id, i.alias, i.state,
		       i.correlation_id, i.operation_id, i.completed_at,
		       i.generation, i.version, i.created_at, i.updated_at, i.created_by
		FROM invocations i
		JOIN agent_services s ON s.isolation_domain_id=i.isolation_domain_id AND s.id=i.service_id
		WHERE i.isolation_domain_id=$1 AND ($2='' OR i.state=$2)
		  AND ($3::timestamptz IS NULL OR (i.created_at,i.id)<($3,$4))
		ORDER BY i.created_at DESC,i.id DESC LIMIT $5
	`, scope, state, before, beforeID, limit+1)
	if err != nil {
		return ScopeInvocationListPage{}, fmt.Errorf("list scope invocations: %w", err)
	}
	defer rows.Close()
	items := make([]domain.ScopeInvocationSummary, 0, limit+1)
	for rows.Next() {
		var item domain.ScopeInvocationSummary
		item.Metadata.IsolationDomainID = scope
		if err := rows.Scan(&item.Metadata.ID, &item.ServiceID, &item.ServiceName, &item.RevisionID, &item.Alias, &item.State, &item.CorrelationID, &item.OperationID, &item.CompletedAt, &item.Metadata.Generation, &item.Metadata.Version, &item.Metadata.CreatedAt, &item.Metadata.UpdatedAt, &item.Metadata.CreatedBy); err != nil {
			return ScopeInvocationListPage{}, fmt.Errorf("scan scope invocation: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ScopeInvocationListPage{}, fmt.Errorf("iterate scope invocations: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return ScopeInvocationListPage{Items: items, HasMore: hasMore}, nil
}
