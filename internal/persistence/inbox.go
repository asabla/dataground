package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	InboxCommand             = "command"
	InboxCallback            = "callback"
	InboxProviderObservation = "provider-observation"
)

// RecordInbox gives command, callback, and provider-observation consumers one
// conflict-detecting deduplication primitive. The caller performs its domain
// transition in the supplied transaction when atomic processing is required.
func RecordInbox(
	ctx context.Context,
	tx pgx.Tx,
	isolationDomainID string,
	sourceKind string,
	deduplicationID string,
	payloadDigest []byte,
	resultValue map[string]any,
	now time.Time,
) (bool, error) {
	if sourceKind != InboxCommand && sourceKind != InboxCallback && sourceKind != InboxProviderObservation {
		return false, fmt.Errorf("unsupported inbox source %q", sourceKind)
	}
	encodedResult, err := json.Marshal(resultValue)
	if err != nil {
		return false, fmt.Errorf("encode inbox result: %w", err)
	}
	inserted, err := tx.Exec(ctx, `
		INSERT INTO inbox_records (
			isolation_domain_id, source_kind, deduplication_id,
			payload_digest, result, processed_at, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $6)
		ON CONFLICT DO NOTHING
	`, isolationDomainID, sourceKind, deduplicationID, payloadDigest, encodedResult, now)
	if err != nil {
		return false, fmt.Errorf("record inbox item: %w", err)
	}
	if inserted.RowsAffected() == 1 {
		return false, nil
	}
	var existingDigest []byte
	if err := tx.QueryRow(ctx, `
		SELECT payload_digest FROM inbox_records
		WHERE isolation_domain_id = $1 AND source_kind = $2 AND deduplication_id = $3
	`, isolationDomainID, sourceKind, deduplicationID).Scan(&existingDigest); err != nil {
		return false, fmt.Errorf("read inbox replay: %w", err)
	}
	if !bytes.Equal(existingDigest, payloadDigest) {
		return false, &DomainError{Code: "INBOX_ID_REUSED", Message: "Inbox deduplication ID was reused with different content."}
	}
	return true, nil
}
