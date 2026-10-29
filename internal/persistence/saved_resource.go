package persistence

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
)

type SavedResourceOwner struct {
	Kind authn.PrincipalKind
	ID   string
}

func (owner SavedResourceOwner) Valid() bool {
	if !savedPrincipalID.MatchString(owner.ID) {
		return false
	}
	switch owner.Kind {
	case authn.PrincipalHuman, authn.PrincipalService, authn.PrincipalPlatformService, authn.PrincipalSandboxWorkload, authn.PrincipalDistributedCompute:
		return true
	}
	return false
}

var savedPrincipalID = regexp.MustCompile(`^[a-z][a-z0-9_-]{2,127}$`)

type SetSavedResourceInput struct {
	Owner         SavedResourceOwner
	Kind          string
	ResourceID    string
	Saved         bool
	CorrelationID string
}

type SavedResourcePage struct {
	Items   []domain.SavedResource
	HasMore bool
}

func (repository *Repository) SetSavedResource(ctx context.Context, idempotency Idempotency, input SetSavedResourceInput) (CommandResult, error) {
	if !repository.Configured() || ctx == nil || !artifactListScopePattern.MatchString(idempotency.IsolationDomainID) || !input.Owner.Valid() || !domain.ValidSavedResource(input.Kind, input.ResourceID) || input.CorrelationID == "" {
		return CommandResult{}, errors.New("saved resource command is invalid")
	}
	return repository.execute(ctx, idempotency, func(tx pgx.Tx, now time.Time) (int, any, error) {
		if input.Saved {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM (`+resourceNavigationSQL+`) resources WHERE kind=$2 AND id=$3)`, idempotency.IsolationDomainID, input.Kind, input.ResourceID).Scan(&exists); err != nil {
				return 0, nil, fmt.Errorf("read saved resource target: %w", err)
			}
			if !exists {
				return 0, nil, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Resource was not found."}
			}
		}
		now = now.UTC().Truncate(time.Microsecond)
		state := domain.SavedResourceState{IsolationDomainID: idempotency.IsolationDomainID, Kind: input.Kind, ResourceID: input.ResourceID, Saved: input.Saved, UpdatedAt: now}
		// Repeated saves with new keys retain their original position. An unsave
		// followed by a new save moves the reference to the top of the list.
		if err := tx.QueryRow(ctx, `INSERT INTO saved_resources
		 (isolation_domain_id,principal_kind,principal_id,resource_kind,resource_id,saved,updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (isolation_domain_id,principal_kind,principal_id,resource_kind,resource_id)
		 DO UPDATE SET saved=EXCLUDED.saved, updated_at=CASE WHEN saved_resources.saved=EXCLUDED.saved THEN saved_resources.updated_at ELSE EXCLUDED.updated_at END
		 RETURNING updated_at`, idempotency.IsolationDomainID, input.Owner.Kind, input.Owner.ID, input.Kind, input.ResourceID, input.Saved, now).Scan(&state.UpdatedAt); err != nil {
			return 0, nil, fmt.Errorf("set saved resource: %w", err)
		}
		event := "resource.unsaved"
		if input.Saved {
			event = "resource.saved"
		}
		// The aggregate identifies this principal's reference without placing a
		// principal kind, resource name or resource content in the outbox payload.
		aggregate := identity.Derived("sav", string(input.Owner.Kind)+":"+input.Owner.ID+":"+input.ResourceID)
		if err := writeOutboxAndAudit(ctx, tx, idempotency.IsolationDomainID, "saved-resource", aggregate, event, input.Owner.ID, input.CorrelationID, "accepted", "", now); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, state, nil
	})
}

func (repository *Repository) ListSavedResources(ctx context.Context, scope string, owner SavedResourceOwner, before *time.Time, beforeID string, limit int) (SavedResourcePage, error) {
	if !repository.Configured() || ctx == nil || !artifactListScopePattern.MatchString(scope) || !owner.Valid() || limit < 1 || limit > 100 || (before == nil) != (beforeID == "") || (before != nil && (before.IsZero() || !domain.ValidResourceSearchID(beforeID))) {
		return SavedResourcePage{}, errors.New("saved resource list is invalid")
	}
	rows, err := repository.pool.Query(ctx, `SELECT s.resource_kind,s.resource_id,s.updated_at,
	 COALESCE(r.name,''),COALESCE(r.description,''),COALESCE(r.workspace_id,''),COALESCE(r.service_id,''),COALESCE(r.invocation_id,''),COALESCE(r.state,''),r.created_at
	 FROM saved_resources s LEFT JOIN (`+resourceNavigationSQL+`) r ON r.kind=s.resource_kind AND r.id=s.resource_id
	 WHERE s.isolation_domain_id=$1 AND s.principal_kind=$2 AND s.principal_id=$3 AND s.saved
	 AND ($4::timestamptz IS NULL OR (s.updated_at,s.resource_id)<($4,$5))
	 ORDER BY s.updated_at DESC,s.resource_id DESC LIMIT $6`, scope, owner.Kind, owner.ID, before, beforeID, limit+1)
	if err != nil {
		return SavedResourcePage{}, fmt.Errorf("list saved resources: %w", err)
	}
	defer rows.Close()
	items := make([]domain.SavedResource, 0, limit+1)
	for rows.Next() {
		var item domain.SavedResource
		var resource domain.ResourceSearchResult
		var created *time.Time
		if err := rows.Scan(&item.Kind, &item.ResourceID, &item.SavedAt, &resource.Name, &resource.Description, &resource.WorkspaceID, &resource.ServiceID, &resource.InvocationID, &resource.State, &created); err != nil {
			return SavedResourcePage{}, fmt.Errorf("scan saved resource: %w", err)
		}
		item.IsolationDomainID = scope
		if created != nil {
			resource.Kind = item.Kind
			resource.Metadata = domain.SearchMetadata{ID: item.ResourceID, IsolationDomainID: scope, CreatedAt: *created}
			item.Resource = &resource
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SavedResourcePage{}, fmt.Errorf("iterate saved resources: %w", err)
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return SavedResourcePage{Items: items, HasMore: more}, nil
}
