package persistence

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type RetireRevisionInput struct {
	RevisionID      string
	ExpectedVersion int
	ActorID         string
	CorrelationID   string
}

// RetireRevision closes future routing without deleting retained definitions,
// invocations, artifacts, or audit. Revision locks also serialize alias creation,
// invocation admission, and operator repair with the traffic checks below.
func (repository *Repository) RetireRevision(ctx context.Context, idempotency Idempotency, input RetireRevisionInput) (CommandResult, error) {
	if input.ExpectedVersion < 1 || input.ActorID == "" || input.CorrelationID == "" {
		return CommandResult{}, errors.New("retirement requires version and attribution")
	}
	return repository.execute(ctx, idempotency, func(tx pgx.Tx, now time.Time) (int, any, error) {
		revision, err := getRevisionForUpdate(ctx, tx, idempotency.IsolationDomainID, input.RevisionID)
		if err != nil {
			return 0, nil, err
		}
		if revision.Metadata.Version != input.ExpectedVersion {
			return 0, nil, &DomainError{Code: "VERSION_CONFLICT", Message: "Revision version did not match."}
		}
		if revision.State != "published" {
			return 0, nil, &DomainError{Code: "REVISION_NOT_PUBLISHED", Message: "Only a published revision can be retired."}
		}
		var routed, active bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
			 SELECT 1 FROM service_aliases
			 WHERE isolation_domain_id = $1 AND revision_id = $2
			)
  `, idempotency.IsolationDomainID, input.RevisionID).Scan(&routed); err != nil {
			return 0, nil, fmt.Errorf("read retirement routing: %w", err)
		}
		if routed {
			return 0, nil, &DomainError{Code: "REVISION_STILL_ROUTED", Message: "Move all aliases away from this revision before retiring it."}
		}
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
			 SELECT 1 FROM invocations AS i
			 LEFT JOIN invocation_execution_operations AS o
			   ON o.isolation_domain_id = i.isolation_domain_id AND o.invocation_id = i.id
			 WHERE i.isolation_domain_id = $1 AND i.revision_id = $2
			   AND (i.state NOT IN ('succeeded', 'failed', 'cancelled')
			        OR o.observed_state NOT IN ('succeeded', 'failed', 'cancelled'))
			) OR EXISTS (
			 SELECT 1 FROM service_publication_operations
			 WHERE isolation_domain_id = $1 AND revision_id = $2
			   AND observed_state NOT IN ('published', 'failed', 'cancelled')
			)
  `, idempotency.IsolationDomainID, input.RevisionID).Scan(&active); err != nil {
			return 0, nil, fmt.Errorf("read retirement activity: %w", err)
		}
		if active {
			return 0, nil, &DomainError{Code: "REVISION_STILL_ACTIVE", Message: "Wait for all invocation and publication activity to finish before retiring this revision."}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE service_revisions
			SET state = 'retired', generation = generation + 1,
			    version = version + 1, updated_at = $3
			WHERE isolation_domain_id = $1 AND id = $2
  `, idempotency.IsolationDomainID, input.RevisionID, now); err != nil {
			return 0, nil, fmt.Errorf("retire service revision: %w", err)
		}
		revision.State = "retired"
		revision.Metadata.Generation++
		revision.Metadata.Version++
		revision.Metadata.UpdatedAt = now
		if err := writeOutboxAndAudit(ctx, tx, idempotency.IsolationDomainID, "service-revision", input.RevisionID, "service-revision.retired", input.ActorID, input.CorrelationID, "succeeded", "", now); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, revision, nil
	})
}

func lockRepairRevision(ctx context.Context, tx pgx.Tx, kind, domainID, operationID string) error {
	var state string
	var query, expected string
	switch kind {
	case OperationKindInvocation:
		query = `
			SELECT r.state FROM invocation_execution_operations AS o
			JOIN invocations AS i
			  ON i.isolation_domain_id = o.isolation_domain_id AND i.id = o.invocation_id
			JOIN service_revisions AS r
			  ON r.isolation_domain_id = i.isolation_domain_id AND r.id = i.revision_id
			WHERE o.isolation_domain_id = $1 AND o.id = $2 AND o.observed_state = 'failed'
			FOR SHARE OF r
  `
		expected = "published"
	case OperationKindPublication:
		query = `
			SELECT r.state FROM service_publication_operations AS o
			JOIN service_revisions AS r
			  ON r.isolation_domain_id = o.isolation_domain_id AND r.id = o.revision_id
			WHERE o.isolation_domain_id = $1 AND o.id = $2 AND o.observed_state = 'failed'
			FOR SHARE OF r
  `
		expected = "draft"
	default:
		return errors.New("unsupported revision repair kind")
	}
	err := tx.QueryRow(ctx, query, domainID, operationID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && state != expected) {
		return &DomainError{Code: "OPERATION_NOT_REPAIRABLE", Message: "The revision no longer permits this operation to be repaired."}
	}
	if err != nil {
		return fmt.Errorf("lock operation revision for repair: %w", err)
	}
	return nil
}
