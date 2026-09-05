package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/asabla/dataground/internal/domain"
	"github.com/jackc/pgx/v5"
)

const serviceRevisionSelect = `
		SELECT isolation_domain_id, id, service_id, revision_number, state, runtime_profile,
		       required_capabilities, input_schema, output_schema, published_at,
		       generation, version, created_at, updated_at, created_by
		FROM service_revisions
		WHERE isolation_domain_id = $1 AND id = $2
	`

func (repository *Repository) GetServiceRevision(ctx context.Context, isolationDomainID, revisionID string) (domain.ServiceRevision, error) {
	if !repository.Configured() || isolationDomainID == "" || revisionID == "" {
		return domain.ServiceRevision{}, errors.New("revision read request is invalid")
	}
	return scanServiceRevision(repository.pool.QueryRow(ctx, serviceRevisionSelect, isolationDomainID, revisionID))
}

func scanServiceRevision(row rowScanner) (domain.ServiceRevision, error) {
	var revision domain.ServiceRevision
	var inputSchema, outputSchema []byte
	err := row.Scan(
		&revision.Metadata.IsolationDomainID,
		&revision.Metadata.ID,
		&revision.ServiceID,
		&revision.RevisionNumber,
		&revision.State,
		&revision.RuntimeProfile,
		&revision.RequiredCapabilities,
		&inputSchema,
		&outputSchema,
		&revision.PublishedAt,
		&revision.Metadata.Generation,
		&revision.Metadata.Version,
		&revision.Metadata.CreatedAt,
		&revision.Metadata.UpdatedAt,
		&revision.Metadata.CreatedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ServiceRevision{}, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Service revision was not found."}
	}
	if err != nil {
		return domain.ServiceRevision{}, fmt.Errorf("read service revision: %w", err)
	}
	if len(inputSchema) > 0 {
		if err := json.Unmarshal(inputSchema, &revision.InputSchema); err != nil {
			return domain.ServiceRevision{}, fmt.Errorf("decode input schema: %w", err)
		}
	}
	if len(outputSchema) > 0 {
		if err := json.Unmarshal(outputSchema, &revision.OutputSchema); err != nil {
			return domain.ServiceRevision{}, fmt.Errorf("decode output schema: %w", err)
		}
	}
	return revision, nil
}
