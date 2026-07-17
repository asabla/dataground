package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type OutboxClaim struct {
	ID                string
	IsolationDomainID string
	AggregateType     string
	AggregateID       string
	EventType         string
	Payload           map[string]any
	CorrelationID     string
	Attempt           int
	LeaseOwner        string
	FencingToken      int64
}

func (repository *Repository) ClaimOutbox(
	ctx context.Context,
	workerID string,
	leaseDuration time.Duration,
) (*OutboxClaim, error) {
	now := repository.now()
	var claim OutboxClaim
	var encodedPayload []byte
	claim.LeaseOwner = workerID
	err := repository.pool.QueryRow(ctx, `
		WITH per_domain AS (
			SELECT DISTINCT ON (isolation_domain_id)
			       isolation_domain_id, id, available_at, created_at
			FROM outbox_events
			WHERE status = 'pending' AND available_at <= $1
			  AND (lease_expires_at IS NULL OR lease_expires_at <= $1)
			ORDER BY isolation_domain_id, available_at, created_at, id
		), candidate AS (
			SELECT isolation_domain_id, id FROM per_domain
			ORDER BY available_at, created_at, isolation_domain_id, id
			LIMIT 1
		), claimed AS (
			UPDATE outbox_events AS event
			SET lease_owner = $2, lease_token = event.lease_token + 1,
			    lease_expires_at = $3, attempt = event.attempt + 1
			FROM candidate
			WHERE event.isolation_domain_id = candidate.isolation_domain_id
			  AND event.id = candidate.id
			  AND (event.lease_expires_at IS NULL OR event.lease_expires_at <= $1)
			RETURNING event.id, event.isolation_domain_id, event.aggregate_type,
			          event.aggregate_id, event.event_type, event.payload,
			          event.correlation_id, event.attempt, event.lease_token
		)
		SELECT * FROM claimed
	`, now, workerID, now.Add(leaseDuration)).Scan(
		&claim.ID, &claim.IsolationDomainID, &claim.AggregateType, &claim.AggregateID,
		&claim.EventType, &encodedPayload, &claim.CorrelationID, &claim.Attempt, &claim.FencingToken,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim outbox event: %w", err)
	}
	if err := json.Unmarshal(encodedPayload, &claim.Payload); err != nil {
		return nil, fmt.Errorf("decode outbox payload: %w", err)
	}
	return &claim, nil
}

func (repository *Repository) CompleteOutbox(ctx context.Context, claim OutboxClaim) error {
	now := repository.now()
	result, err := repository.pool.Exec(ctx, `
		UPDATE outbox_events
		SET status = 'delivered', delivered_at = $6,
		    lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1 AND isolation_domain_id = $2
		  AND status = 'pending' AND lease_owner = $3 AND lease_token = $4
		  AND lease_expires_at > $5
	`, claim.ID, claim.IsolationDomainID, claim.LeaseOwner, claim.FencingToken, now, now)
	if err != nil {
		return fmt.Errorf("complete outbox event: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (repository *Repository) RetryOutbox(ctx context.Context, claim OutboxClaim, dueAt time.Time) error {
	now := repository.now()
	status := "pending"
	if claim.Attempt >= 20 {
		status = "dead_letter"
	}
	result, err := repository.pool.Exec(ctx, `
		UPDATE outbox_events
		SET status = $6, available_at = $7,
		    lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1 AND isolation_domain_id = $2
		  AND status = 'pending' AND lease_owner = $3 AND lease_token = $4
		  AND lease_expires_at > $5
	`, claim.ID, claim.IsolationDomainID, claim.LeaseOwner, claim.FencingToken, now, status, dueAt)
	if err != nil {
		return fmt.Errorf("retry outbox event: %w", err)
	}
	if result.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}
