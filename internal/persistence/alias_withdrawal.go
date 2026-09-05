package persistence

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/jackc/pgx/v5"
)

type WithdrawAliasInput struct {
	ServiceID, Name, ActorID, CorrelationID string
	ExpectedVersion                         int
}

// WithdrawAlias retains the alias identity and monotonically increasing version
// while removing it from discovery and new admission. The same row lock fences
// concurrent assignment and invocation acceptance.
func (repository *Repository) WithdrawAlias(ctx context.Context, idempotency Idempotency, input WithdrawAliasInput) (CommandResult, error) {
	if input.ExpectedVersion < 1 {
		return CommandResult{}, &DomainError{Code: "INVALID_REQUEST", Message: "A positive expected alias version is required."}
	}
	return repository.execute(ctx, idempotency, func(tx pgx.Tx, now time.Time) (int, any, error) {
		var alias domain.ServiceAlias
		err := scanAlias(tx.QueryRow(ctx, `
 SELECT isolation_domain_id, id, service_id, name, revision_id,
        generation, version, created_at, updated_at, created_by, withdrawn_at
 FROM service_aliases
 WHERE isolation_domain_id=$1 AND service_id=$2 AND name=$3
   AND withdrawn_at IS NULL
 FOR UPDATE
`, idempotency.IsolationDomainID, input.ServiceID, input.Name), &alias)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, &DomainError{Code: "RESOURCE_NOT_FOUND", Message: "Service alias was not found."}
		}
		if err != nil {
			return 0, nil, fmt.Errorf("read alias for withdrawal: %w", err)
		}
		if alias.Metadata.Version != input.ExpectedVersion {
			return 0, nil, &DomainError{Code: "VERSION_CONFLICT", Message: "Alias version did not match."}
		}
		if _, err := tx.Exec(ctx, `
 UPDATE service_aliases
 SET withdrawn_at=$4, updated_at=$4, version=version+1, generation=generation+1
 WHERE isolation_domain_id=$1 AND service_id=$2 AND name=$3
`, idempotency.IsolationDomainID, input.ServiceID, input.Name, now); err != nil {
			return 0, nil, fmt.Errorf("withdraw service alias: %w", err)
		}
		alias.WithdrawnAt = &now
		alias.Metadata.Version++
		alias.Metadata.Generation++
		alias.Metadata.UpdatedAt = now
		if err := writeOutboxAndAudit(ctx, tx, idempotency.IsolationDomainID, "service-alias", alias.Metadata.ID, "service-alias.withdrawn", input.ActorID, input.CorrelationID, "accepted", "", now); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, alias, nil
	})
}
