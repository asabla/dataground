package persistence

import (
	"context"
	"errors"
	"fmt"

	"github.com/asabla/dataground/internal/domain"
	"github.com/jackc/pgx/v5"
)

func (repository *Repository) GetService(ctx context.Context, isolationDomainID, serviceID string) (domain.AgentService, error) {
	if !repository.Configured() || isolationDomainID == "" || serviceID == "" {
		return domain.AgentService{}, errors.New("service read request is invalid")
	}
	var service domain.AgentService
	err := repository.pool.QueryRow(ctx, `
		SELECT isolation_domain_id, id, name, description, generation, version,
		       created_at, updated_at, created_by, COALESCE(workspace_id,'')
		FROM agent_services WHERE isolation_domain_id = $1 AND id = $2
	`, isolationDomainID, serviceID).Scan(
		&service.Metadata.IsolationDomainID, &service.Metadata.ID,
		&service.Name, &service.Description, &service.Metadata.Generation,
		&service.Metadata.Version, &service.Metadata.CreatedAt,
		&service.Metadata.UpdatedAt, &service.Metadata.CreatedBy, &service.WorkspaceID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AgentService{}, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Agent service was not found."}
	}
	if err != nil {
		return domain.AgentService{}, fmt.Errorf("read agent service: %w", err)
	}
	return service, nil
}
