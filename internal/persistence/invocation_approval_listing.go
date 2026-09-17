package persistence

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/jackc/pgx/v5"
)

var approvalListScopePattern = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)

type InvocationApprovalListPage struct {
	Items   []domain.InvocationApproval
	HasMore bool
}

func (repository *Repository) ListInvocationApprovals(ctx context.Context, isolationDomainID, invocationID string, beforeCreatedAt *time.Time, beforeID string, limit int) (InvocationApprovalListPage, error) {
	if !repository.Configured() || ctx == nil || !approvalListScopePattern.MatchString(isolationDomainID) ||
		!approvalInvocationPattern.MatchString(invocationID) || limit < 1 || limit > 100 ||
		(beforeCreatedAt == nil) != (beforeID == "") ||
		(beforeCreatedAt != nil && (beforeCreatedAt.IsZero() || !approvalIDPattern.MatchString(beforeID))) {
		return InvocationApprovalListPage{}, ErrInvocationRuntimeApprovalInvalid
	}
	var exists bool
	if err := repository.pool.QueryRow(ctx, `SELECT true FROM invocations WHERE isolation_domain_id=$1 AND id=$2`, isolationDomainID, invocationID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InvocationApprovalListPage{}, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Invocation was not found."}
		}
		return InvocationApprovalListPage{}, fmt.Errorf("read approval list invocation: %w", err)
	}
	// Select only the public projection. Native handles and execution routing
	// never leave the persistence boundary through this collection.
	rows, err := repository.pool.Query(ctx, `
		SELECT contract,id,requested_action,state,version,COALESCE(decision,''),
		       COALESCE(resolved_by,''),resolved_at,created_at,updated_at,
		       expires_at,closed_at,COALESCE(close_reason,'')
		FROM invocation_runtime_approvals
		WHERE isolation_domain_id=$1 AND invocation_id=$2
		  AND ($3::timestamptz IS NULL OR (created_at,id)<($3,$4))
		ORDER BY created_at DESC,id DESC LIMIT $5
	`, isolationDomainID, invocationID, beforeCreatedAt, beforeID, limit+1)
	if err != nil {
		return InvocationApprovalListPage{}, fmt.Errorf("list invocation approvals: %w", err)
	}
	defer rows.Close()
	items := make([]domain.InvocationApproval, 0, limit+1)
	for rows.Next() {
		var item domain.InvocationApproval
		var contract string
		item.IsolationDomainID, item.InvocationID = isolationDomainID, invocationID
		if err := rows.Scan(&contract, &item.ID, &item.RequestedAction, &item.State, &item.Version,
			&item.Decision, &item.ResolvedBy, &item.ResolvedAt, &item.CreatedAt, &item.UpdatedAt,
			&item.ExpiresAt, &item.ClosedAt, &item.CloseReason); err != nil {
			return InvocationApprovalListPage{}, fmt.Errorf("scan invocation approval: %w", err)
		}
		switch contract {
		case InvocationRuntimeApprovalContract:
			item.SchemaVersion = domain.InvocationApprovalSchemaV2
		case InvocationRuntimeApprovalLegacyContract:
			item.SchemaVersion = domain.InvocationApprovalSchemaV1
		default:
			return InvocationApprovalListPage{}, ErrInvocationRuntimeApprovalInvalid
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return InvocationApprovalListPage{}, fmt.Errorf("iterate invocation approvals: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return InvocationApprovalListPage{Items: items, HasMore: hasMore}, nil
}
