package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReferenceDriver persists provider-side receipts separately from operation
// state. It is a deterministic conformance adapter, not a production harness.
type ReferenceDriver struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewReferenceDriver(pool *pgxpool.Pool) *ReferenceDriver {
	return &ReferenceDriver{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

func (driver *ReferenceDriver) Observe(
	ctx context.Context,
	effect persistence.EffectRecord,
) (map[string]any, bool, error) {
	var encoded []byte
	err := driver.pool.QueryRow(ctx, `
		SELECT result
		FROM reference_runtime_receipts
		WHERE isolation_domain_id = $1 AND effect_id = $2
	`, effect.IsolationDomainID, effect.EffectID).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("observe reference runtime receipt: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, false, fmt.Errorf("decode reference runtime receipt: %w", err)
	}
	return result, true, nil
}

func (driver *ReferenceDriver) Apply(
	ctx context.Context,
	effect persistence.EffectRecord,
) (map[string]any, error) {
	result := map[string]any{
		"effectId": effect.EffectID,
		"phase":    effect.Phase,
		"status":   "succeeded",
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode reference runtime result: %w", err)
	}
	_, err = driver.pool.Exec(ctx, `
		INSERT INTO reference_runtime_receipts (
			isolation_domain_id, effect_id, phase, result, applied_at
		) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (isolation_domain_id, effect_id) DO NOTHING
	`, effect.IsolationDomainID, effect.EffectID, effect.Phase, encoded, driver.now())
	if err != nil {
		return nil, fmt.Errorf("apply reference runtime effect: %w", err)
	}
	observed, found, err := driver.Observe(ctx, effect)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("reference runtime receipt was not visible after apply")
	}
	return observed, nil
}
