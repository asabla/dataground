package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/asabla/dataground/internal/domain"
)

type ResourceSearchPage struct {
	Items   []domain.ResourceSearchResult
	HasMore bool
}

// SearchResources requires its own scope-wide metadata permission at API entry.
// The union reads only public navigation metadata, never inputs, outputs or storage bindings.
func (repository *Repository) SearchResources(ctx context.Context, scope, query, kind string, before *time.Time, beforeID string, limit int) (ResourceSearchPage, error) {
	if !repository.Configured() || ctx == nil || !artifactListScopePattern.MatchString(scope) || query == "" || !domain.ValidDiscoverySearch(query) || !domain.ValidResourceSearchKind(kind) || limit < 1 || limit > 100 || (before == nil) != (beforeID == "") || (before != nil && (before.IsZero() || !domain.ValidResourceSearchID(beforeID))) {
		return ResourceSearchPage{}, errors.New("resource search request is invalid")
	}
	rows, err := repository.pool.Query(ctx, `
 SELECT id,kind,name,description,workspace_id,service_id,invocation_id,state,created_at FROM (
`+resourceNavigationSQL+`
 ) resources
 WHERE ($3='' OR kind=$3)
 AND (strpos(translate(name,'ABCDEFGHIJKLMNOPQRSTUVWXYZ','abcdefghijklmnopqrstuvwxyz'),$2)>0
   OR strpos(translate(description,'ABCDEFGHIJKLMNOPQRSTUVWXYZ','abcdefghijklmnopqrstuvwxyz'),$2)>0 OR strpos(id,$2)>0)
 AND ($4::timestamptz IS NULL OR (created_at,id)<($4,$5))
 ORDER BY created_at DESC,id DESC LIMIT $6`, scope, domain.FoldDiscoverySearch(query), kind, before, beforeID, limit+1)
	if err != nil {
		return ResourceSearchPage{}, fmt.Errorf("search resources: %w", err)
	}
	defer rows.Close()
	items := make([]domain.ResourceSearchResult, 0, limit+1)
	for rows.Next() {
		var item domain.ResourceSearchResult
		item.Metadata.IsolationDomainID = scope
		if err := rows.Scan(&item.Metadata.ID, &item.Kind, &item.Name, &item.Description, &item.WorkspaceID, &item.ServiceID, &item.InvocationID, &item.State, &item.Metadata.CreatedAt); err != nil {
			return ResourceSearchPage{}, fmt.Errorf("scan search result: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ResourceSearchPage{}, fmt.Errorf("iterate search results: %w", err)
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return ResourceSearchPage{Items: items, HasMore: more}, nil
}

// Shared by authorized metadata discovery; $1 is always the isolation scope.
const resourceNavigationSQL = `
   SELECT id,'workspace'::text kind,name,description,''::text workspace_id,''::text service_id,''::text invocation_id,''::text state,created_at
   FROM workspaces WHERE isolation_domain_id=$1
   UNION ALL
   SELECT id,'agent-service',name,description,COALESCE(workspace_id,''),'','','',created_at
   FROM agent_services WHERE isolation_domain_id=$1
   UNION ALL
   SELECT i.id,'invocation',s.name,'',COALESCE(s.workspace_id,''),i.service_id,'',i.state,i.created_at
   FROM invocations i JOIN agent_services s ON s.isolation_domain_id=i.isolation_domain_id AND s.id=i.service_id
   WHERE i.isolation_domain_id=$1
   UNION ALL
   SELECT a.id,'artifact',a.name,'',COALESCE(s.workspace_id,''),i.service_id,a.invocation_id,a.state,a.created_at
   FROM artifacts a JOIN invocations i ON i.isolation_domain_id=a.isolation_domain_id AND i.id=a.invocation_id
   JOIN agent_services s ON s.isolation_domain_id=i.isolation_domain_id AND s.id=i.service_id
   WHERE a.isolation_domain_id=$1
`
