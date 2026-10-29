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

var (
	artifactListScopePattern      = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
	artifactListInvocationPattern = regexp.MustCompile(`^inv_[0-9a-z]{20,32}$`)
	artifactListIDPattern         = regexp.MustCompile(`^art_[0-9a-z]{20,32}$`)
)

type InvocationArtifactListPage struct {
	Items   []domain.ArtifactDescriptor
	HasMore bool
}

func (repository *Repository) ListInvocationArtifacts(ctx context.Context, isolationDomainID, invocationID string, beforeCreatedAt *time.Time, beforeID string, limit int) (InvocationArtifactListPage, error) {
	if !repository.Configured() || ctx == nil || !artifactListScopePattern.MatchString(isolationDomainID) ||
		!artifactListInvocationPattern.MatchString(invocationID) || limit < 1 || limit > 100 ||
		(beforeCreatedAt == nil) != (beforeID == "") ||
		(beforeCreatedAt != nil && (beforeCreatedAt.IsZero() || !artifactListIDPattern.MatchString(beforeID))) {
		return InvocationArtifactListPage{}, errors.New("artifact list request is invalid")
	}
	var exists bool
	if err := repository.pool.QueryRow(ctx, `SELECT true FROM invocations WHERE isolation_domain_id=$1 AND id=$2`, isolationDomainID, invocationID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InvocationArtifactListPage{}, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Invocation was not found."}
		}
		return InvocationArtifactListPage{}, fmt.Errorf("read artifact list invocation: %w", err)
	}
	// Discovery reads descriptors only. Object bytes and storage bindings remain
	// behind their independent content authorization boundary.
	rows, err := repository.pool.Query(ctx, `
		SELECT id,name,kind,media_type,size_bytes,digest,state,sensitive,generation,version,created_at,updated_at,created_by
		FROM artifacts
		WHERE isolation_domain_id=$1 AND invocation_id=$2
		  AND ($3::timestamptz IS NULL OR (created_at,id)<($3,$4))
		ORDER BY created_at DESC,id DESC LIMIT $5
	`, isolationDomainID, invocationID, beforeCreatedAt, beforeID, limit+1)
	if err != nil {
		return InvocationArtifactListPage{}, fmt.Errorf("list invocation artifacts: %w", err)
	}
	defer rows.Close()
	items := make([]domain.ArtifactDescriptor, 0, limit+1)
	for rows.Next() {
		var item domain.ArtifactDescriptor
		item.Metadata.IsolationDomainID, item.InvocationID = isolationDomainID, invocationID
		if err := rows.Scan(&item.Metadata.ID, &item.Name, &item.Kind, &item.MediaType, &item.SizeBytes, &item.Digest, &item.State, &item.Sensitive,
			&item.Metadata.Generation, &item.Metadata.Version, &item.Metadata.CreatedAt, &item.Metadata.UpdatedAt, &item.Metadata.CreatedBy); err != nil {
			return InvocationArtifactListPage{}, fmt.Errorf("scan invocation artifact summary: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return InvocationArtifactListPage{}, fmt.Errorf("iterate invocation artifacts: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return InvocationArtifactListPage{Items: items, HasMore: hasMore}, nil
}
