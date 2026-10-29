package persistence

import (
	"context"
	"errors"
	"fmt"
	"github.com/asabla/dataground/internal/domain"
	"time"
)

type ScopeArtifactListPage struct {
	Items   []domain.ScopeArtifactSummary
	HasMore bool
}

func ValidArtifactDiscoveryState(state string) bool {
	switch state {
	case "", "pending", "available", "failed", "deleted":
		return true
	default:
		return false
	}
}

// ListScopeArtifacts requires separate scope-wide metadata authority at entry.
// The query never reads object contents or private storage bindings.
func (repository *Repository) ListScopeArtifacts(ctx context.Context, scope, state string, before *time.Time, beforeID string, limit int) (ScopeArtifactListPage, error) {
	if !repository.Configured() || ctx == nil || !artifactListScopePattern.MatchString(scope) || !ValidArtifactDiscoveryState(state) || limit < 1 || limit > 100 || (before == nil) != (beforeID == "") || (before != nil && (before.IsZero() || !artifactListIDPattern.MatchString(beforeID))) {
		return ScopeArtifactListPage{}, errors.New("scope artifact list request is invalid")
	}
	rows, err := repository.pool.Query(ctx, `
 SELECT a.id,a.invocation_id,a.name,a.kind,a.media_type,a.size_bytes,a.digest,a.state,a.sensitive,
 a.generation,a.version,a.created_at,a.updated_at,a.created_by,i.service_id,s.name,i.revision_id
 FROM artifacts a
 JOIN invocations i ON i.isolation_domain_id=a.isolation_domain_id AND i.id=a.invocation_id
 JOIN agent_services s ON s.isolation_domain_id=i.isolation_domain_id AND s.id=i.service_id
 WHERE a.isolation_domain_id=$1 AND ($2='' OR a.state=$2)
 AND ($3::timestamptz IS NULL OR (a.created_at,a.id)<($3,$4))
 ORDER BY a.created_at DESC,a.id DESC LIMIT $5`, scope, state, before, beforeID, limit+1)
	if err != nil {
		return ScopeArtifactListPage{}, fmt.Errorf("list scope artifacts: %w", err)
	}
	defer rows.Close()
	items := make([]domain.ScopeArtifactSummary, 0, limit+1)
	for rows.Next() {
		var item domain.ScopeArtifactSummary
		item.Metadata.IsolationDomainID = scope
		if err := rows.Scan(&item.Metadata.ID, &item.InvocationID, &item.Name, &item.Kind, &item.MediaType, &item.SizeBytes, &item.Digest, &item.State, &item.Sensitive, &item.Metadata.Generation, &item.Metadata.Version, &item.Metadata.CreatedAt, &item.Metadata.UpdatedAt, &item.Metadata.CreatedBy, &item.ServiceID, &item.ServiceName, &item.RevisionID); err != nil {
			return ScopeArtifactListPage{}, fmt.Errorf("scan scope artifact: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ScopeArtifactListPage{}, fmt.Errorf("iterate scope artifacts: %w", err)
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return ScopeArtifactListPage{Items: items, HasMore: more}, nil
}
