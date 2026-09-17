package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/jackc/pgx/v5"
)

type InvocationQuestionListPage struct {
	Items   []domain.InvocationQuestionSummary
	HasMore bool
}

func (repository *Repository) ListInvocationQuestions(ctx context.Context, isolationDomainID, invocationID string, beforeCreatedAt *time.Time, beforeID string, limit int) (InvocationQuestionListPage, error) {
	if !repository.Configured() || ctx == nil || !questionScopePattern.MatchString(isolationDomainID) ||
		!approvalInvocationPattern.MatchString(invocationID) || limit < 1 || limit > 100 ||
		(beforeCreatedAt == nil) != (beforeID == "") ||
		(beforeCreatedAt != nil && (beforeCreatedAt.IsZero() || !questionIDPattern.MatchString(beforeID))) {
		return InvocationQuestionListPage{}, ErrInvocationRuntimeQuestionInvalid
	}
	var exists bool
	if err := repository.pool.QueryRow(ctx, `SELECT true FROM invocations WHERE isolation_domain_id=$1 AND id=$2`, isolationDomainID, invocationID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InvocationQuestionListPage{}, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Invocation was not found."}
		}
		return InvocationQuestionListPage{}, fmt.Errorf("read question list invocation: %w", err)
	}
	// Do not fetch question content for discovery. Individual reads and answers
	// have separate authorization boundaries.
	rows, err := repository.pool.Query(ctx, `
		SELECT id,state,version,expires_at,created_at,updated_at
		FROM invocation_runtime_questions
		WHERE isolation_domain_id=$1 AND invocation_id=$2
		  AND ($3::timestamptz IS NULL OR (created_at,id)<($3,$4))
		ORDER BY created_at DESC,id DESC LIMIT $5
	`, isolationDomainID, invocationID, beforeCreatedAt, beforeID, limit+1)
	if err != nil {
		return InvocationQuestionListPage{}, fmt.Errorf("list invocation questions: %w", err)
	}
	defer rows.Close()
	items := make([]domain.InvocationQuestionSummary, 0, limit+1)
	for rows.Next() {
		var item domain.InvocationQuestionSummary
		item.IsolationDomainID, item.InvocationID = isolationDomainID, invocationID
		item.SchemaVersion = domain.InvocationQuestionSummarySchemaV1
		if err := rows.Scan(&item.ID, &item.State, &item.Version, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return InvocationQuestionListPage{}, fmt.Errorf("scan invocation question summary: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return InvocationQuestionListPage{}, fmt.Errorf("iterate invocation questions: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return InvocationQuestionListPage{Items: items, HasMore: hasMore}, nil
}
