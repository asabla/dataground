package persistence

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/jackc/pgx/v5"
)

var workspaceRecordID = regexp.MustCompile(`^wsp_[0-9a-z]{20,32}$`)

type CreateWorkspaceInput struct{ ID, Name, Description, ActorID, CorrelationID string }
type WorkspaceListPage struct {
	Items   []domain.Workspace
	HasMore bool
}

func (repository *Repository) GetWorkspace(ctx context.Context, scope, id string) (domain.Workspace, error) {
	if !repository.Configured() || scope == "" {
		return domain.Workspace{}, errors.New("workspace read request is invalid")
	}
	var value domain.Workspace
	value.Metadata.IsolationDomainID = scope
	err := repository.pool.QueryRow(ctx, `SELECT id,name,description,generation,version,created_at,updated_at,created_by FROM workspaces WHERE isolation_domain_id=$1 AND id=$2`, scope, id).Scan(&value.Metadata.ID, &value.Name, &value.Description, &value.Metadata.Generation, &value.Metadata.Version, &value.Metadata.CreatedAt, &value.Metadata.UpdatedAt, &value.Metadata.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Workspace{}, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Workspace was not found."}
	}
	return value, err
}
func (repository *Repository) CreateWorkspace(
	ctx context.Context,
	idempotency Idempotency,
	input CreateWorkspaceInput,
) (CommandResult, error) {
	if !repository.Configured() || !workspaceRecordID.MatchString(input.ID) || !domain.ValidWorkspaceMetadata(input.Name, input.Description) || input.ActorID == "" || input.CorrelationID == "" {
		return CommandResult{}, errors.New("workspace input is invalid")
	}
	return repository.execute(ctx, idempotency, func(tx pgx.Tx, now time.Time) (int, any, error) {
		now = now.Truncate(time.Microsecond)
		workspace := domain.Workspace{
			Metadata:    metadata(input.ID, idempotency.IsolationDomainID, input.ActorID, now),
			Name:        input.Name,
			Description: input.Description,
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO workspaces (
				isolation_domain_id, id, name, description, generation, version,
				created_at, updated_at, created_by
			) VALUES ($1, $2, $3, $4, 1, 1, $5, $5, $6)
		`, idempotency.IsolationDomainID, input.ID, input.Name, input.Description, now, input.ActorID)
		if err != nil {
			return 0, nil, fmt.Errorf("insert workspace: %w", err)
		}
		if err := writeOutboxAndAudit(
			ctx,
			tx,
			idempotency.IsolationDomainID,
			"workspace",
			input.ID,
			"workspace.created",
			input.ActorID,
			input.CorrelationID,
			"accepted",
			"",
			now,
		); err != nil {
			return 0, nil, err
		}
		return http.StatusCreated, workspace, nil
	})
}

func (repository *Repository) ListWorkspaces(
	ctx context.Context,
	isolationDomainID string,
	beforeCreatedAt *time.Time,
	beforeID string,
	limit int,
	query string,
) (WorkspaceListPage, error) {
	if !repository.Configured() || isolationDomainID == "" || limit < 1 || limit > 100 || !domain.ValidDiscoverySearch(query) ||
		(beforeCreatedAt == nil) != (beforeID == "") {
		return WorkspaceListPage{}, errors.New("workspace list request is invalid")
	}
	rows, err := repository.pool.Query(ctx, `
		SELECT id, name, description, generation, version, created_at, updated_at, created_by
		FROM workspaces
		WHERE isolation_domain_id = $1
		  AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
		  AND ($5 = '' OR strpos(translate(name, 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz'), $5) > 0
		    OR strpos(translate(description, 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz'), $5) > 0
		    OR strpos(id, $5) > 0)
		ORDER BY created_at DESC, id DESC
		LIMIT $4
	`, isolationDomainID, beforeCreatedAt, beforeID, limit+1, domain.FoldDiscoverySearch(query))
	if err != nil {
		return WorkspaceListPage{}, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()
	workspaces := make([]domain.Workspace, 0, limit+1)
	for rows.Next() {
		var workspace domain.Workspace
		workspace.Metadata.IsolationDomainID = isolationDomainID
		if err := rows.Scan(
			&workspace.Metadata.ID,
			&workspace.Name,
			&workspace.Description,
			&workspace.Metadata.Generation,
			&workspace.Metadata.Version,
			&workspace.Metadata.CreatedAt,
			&workspace.Metadata.UpdatedAt,
			&workspace.Metadata.CreatedBy,
		); err != nil {
			return WorkspaceListPage{}, fmt.Errorf("scan workspace: %w", err)
		}
		workspaces = append(workspaces, workspace)
	}
	if err := rows.Err(); err != nil {
		return WorkspaceListPage{}, fmt.Errorf("iterate workspaces: %w", err)
	}
	hasMore := len(workspaces) > limit
	if hasMore {
		workspaces = workspaces[:limit]
	}
	return WorkspaceListPage{Items: workspaces, HasMore: hasMore}, nil
}
