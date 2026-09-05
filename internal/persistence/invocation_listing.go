package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/jackc/pgx/v5"
)

type InvocationListPage struct {
	Items   []domain.InvocationSummary
	HasMore bool
}

func (repository *Repository) ListInvocations(ctx context.Context, isolationDomainID, serviceID string, beforeCreatedAt *time.Time, beforeID string, limit int) (InvocationListPage, error) {
	if !repository.Configured() || isolationDomainID == "" || serviceID == "" || limit < 1 || limit > 100 ||
		(beforeCreatedAt == nil) != (beforeID == "") {
		return InvocationListPage{}, errors.New("invocation list request is invalid")
	}
	var serviceExists bool
	if err := repository.pool.QueryRow(ctx, `SELECT true FROM agent_services WHERE isolation_domain_id = $1 AND id = $2`, isolationDomainID, serviceID).Scan(&serviceExists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InvocationListPage{}, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Agent service was not found."}
		}
		return InvocationListPage{}, fmt.Errorf("read invocation list service: %w", err)
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT id, revision_id, alias, state, correlation_id, operation_id,
		       completed_at, generation, version, created_at, updated_at, created_by
		FROM invocations
		WHERE isolation_domain_id = $1 AND service_id = $2
		  AND ($3::timestamptz IS NULL OR (created_at, id) < ($3, $4))
		ORDER BY created_at DESC, id DESC
		LIMIT $5
	`, isolationDomainID, serviceID, beforeCreatedAt, beforeID, limit+1)
	if err != nil {
		return InvocationListPage{}, fmt.Errorf("list invocations: %w", err)
	}
	defer rows.Close()
	items := make([]domain.InvocationSummary, 0, limit+1)
	for rows.Next() {
		var item domain.InvocationSummary
		item.Metadata.IsolationDomainID = isolationDomainID
		item.ServiceID = serviceID
		if err := rows.Scan(&item.Metadata.ID, &item.RevisionID, &item.Alias, &item.State,
			&item.CorrelationID, &item.OperationID, &item.CompletedAt,
			&item.Metadata.Generation, &item.Metadata.Version, &item.Metadata.CreatedAt,
			&item.Metadata.UpdatedAt, &item.Metadata.CreatedBy); err != nil {
			return InvocationListPage{}, fmt.Errorf("scan invocation summary: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return InvocationListPage{}, fmt.Errorf("iterate invocation summaries: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return InvocationListPage{Items: items, HasMore: hasMore}, nil
}
