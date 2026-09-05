package persistence

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/asabla/dataground/internal/domain"
	"github.com/jackc/pgx/v5"
)

var aliasListNamePattern = regexp.MustCompile(`^[a-z](?:[a-z0-9-]*[a-z0-9])?$`)

type ServiceAliasListPage struct {
	Items   []domain.ServiceAlias
	HasMore bool
}

// ListServiceAliases reads current active routes. Names are immutable and unique
// within a service, so withdrawal of a boundary row does not invalidate a cursor.
func (repository *Repository) ListServiceAliases(ctx context.Context, isolationDomainID, serviceID, afterName string, limit int) (ServiceAliasListPage, error) {
	if !repository.Configured() || isolationDomainID == "" || serviceID == "" || limit < 1 || limit > 100 ||
		(afterName != "" && (len(afterName) > 63 || !aliasListNamePattern.MatchString(afterName))) {
		return ServiceAliasListPage{}, errors.New("alias list request is invalid")
	}
	var serviceExists bool
	if err := repository.pool.QueryRow(ctx, `SELECT true FROM agent_services WHERE isolation_domain_id=$1 AND id=$2`, isolationDomainID, serviceID).Scan(&serviceExists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ServiceAliasListPage{}, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Agent service was not found."}
		}
		return ServiceAliasListPage{}, fmt.Errorf("read alias list service: %w", err)
	}
	rows, err := repository.pool.Query(ctx, `
 SELECT isolation_domain_id, id, service_id, name, revision_id,
        generation, version, created_at, updated_at, created_by, withdrawn_at
 FROM service_aliases
 WHERE isolation_domain_id=$1 AND service_id=$2 AND withdrawn_at IS NULL
   AND name COLLATE "C" > $3
 ORDER BY name COLLATE "C" ASC
 LIMIT $4
`, isolationDomainID, serviceID, afterName, limit+1)
	if err != nil {
		return ServiceAliasListPage{}, fmt.Errorf("list service aliases: %w", err)
	}
	defer rows.Close()
	items := make([]domain.ServiceAlias, 0, limit+1)
	for rows.Next() {
		var item domain.ServiceAlias
		if err := scanAlias(rows, &item); err != nil {
			return ServiceAliasListPage{}, fmt.Errorf("scan service alias: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ServiceAliasListPage{}, fmt.Errorf("iterate service aliases: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return ServiceAliasListPage{Items: items, HasMore: hasMore}, nil
}
