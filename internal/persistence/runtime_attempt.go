package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/asabla/dataground/internal/identity"
	"github.com/jackc/pgx/v5"
)

const maximumInvocationRuntimeResultBytes = 256 << 10

var (
	ErrInvocationRuntimeAttemptMissing   = errors.New("invocation runtime attempt is missing")
	ErrInvocationRuntimeAttemptInvalid   = errors.New("invocation runtime attempt is invalid")
	ErrInvocationRuntimeAttemptAmbiguous = errors.New("invocation runtime attempt outcome is ambiguous")
	ErrInvocationRuntimeAttemptConflict  = errors.New("invocation runtime attempt conflicts with persisted state")
)

type InvocationRuntimeAttempt struct {
	IsolationDomainID string
	OperationID       string
	EffectID          string
	LeaseOwner        string
	FencingToken      int64
	Status            string
	Result            map[string]any
}

// BeginInvocationRuntimeAttempt consumes the single-use permission to start one
// native runtime turn. Any replay is ambiguous because the caller may have
// started the turn after the reservation committed.
func (repository *Repository) BeginInvocationRuntimeAttempt(
	ctx context.Context,
	claim OperationClaim,
	effect EffectRecord,
) (InvocationRuntimeAttempt, error) {
	if !validInvocationRuntimeAttempt(claim, effect) {
		return InvocationRuntimeAttempt{}, ErrInvocationRuntimeAttemptInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationRuntimeAttempt{}, fmt.Errorf("begin invocation runtime attempt: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := lockInvocationRuntimeClaim(ctx, tx, claim, repository.now()); err != nil {
		return InvocationRuntimeAttempt{}, err
	}
	if err := verifyInvocationRuntimeEffect(ctx, tx, effect); err != nil {
		return InvocationRuntimeAttempt{}, err
	}
	now := repository.now()
	result, err := tx.Exec(ctx, `
		INSERT INTO invocation_runtime_attempts (
			isolation_domain_id, operation_id, effect_id, lease_owner,
			fencing_token, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, 'reserved', $6, $6)
		ON CONFLICT DO NOTHING
	`, effect.IsolationDomainID, effect.OperationID, effect.EffectID,
		claim.LeaseOwner, claim.FencingToken, now)
	if err != nil {
		return InvocationRuntimeAttempt{}, fmt.Errorf("reserve invocation runtime attempt: %w", err)
	}
	attempt, readErr := getInvocationRuntimeAttempt(
		ctx, tx, effect.IsolationDomainID, effect.OperationID,
	)
	if readErr != nil {
		return InvocationRuntimeAttempt{}, readErr
	}
	if attempt.EffectID != effect.EffectID {
		return InvocationRuntimeAttempt{}, ErrInvocationRuntimeAttemptConflict
	}
	if result.RowsAffected() != 1 {
		return InvocationRuntimeAttempt{}, ErrInvocationRuntimeAttemptAmbiguous
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeAttempt{}, fmt.Errorf("commit invocation runtime attempt: %w", err)
	}
	return attempt, nil
}

// CompleteInvocationRuntimeAttempt records a bounded terminal result under the
// same active claim. Exact completion replay is read-only.
func (repository *Repository) CompleteInvocationRuntimeAttempt(
	ctx context.Context,
	claim OperationClaim,
	effect EffectRecord,
	result map[string]any,
) (InvocationRuntimeAttempt, error) {
	if !validInvocationRuntimeAttempt(claim, effect) || result == nil {
		return InvocationRuntimeAttempt{}, ErrInvocationRuntimeAttemptInvalid
	}
	encodedResult, err := json.Marshal(result)
	if err != nil || len(encodedResult) > maximumInvocationRuntimeResultBytes {
		return InvocationRuntimeAttempt{}, ErrInvocationRuntimeAttemptInvalid
	}
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return InvocationRuntimeAttempt{}, fmt.Errorf("begin invocation runtime completion: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := lockInvocationRuntimeClaim(ctx, tx, claim, repository.now()); err != nil {
		return InvocationRuntimeAttempt{}, err
	}
	if err := verifyInvocationRuntimeEffect(ctx, tx, effect); err != nil {
		return InvocationRuntimeAttempt{}, err
	}
	now := repository.now()
	update, err := tx.Exec(ctx, `
		UPDATE invocation_runtime_attempts
		SET status = 'succeeded', result = $6, completed_at = $7, updated_at = $7
		WHERE isolation_domain_id = $1
		  AND operation_id = $2
		  AND effect_id = $3
		  AND lease_owner = $4
		  AND fencing_token = $5
		  AND status = 'reserved'
	`, effect.IsolationDomainID, effect.OperationID, effect.EffectID,
		claim.LeaseOwner, claim.FencingToken, encodedResult, now)
	if err != nil {
		return InvocationRuntimeAttempt{}, fmt.Errorf("complete invocation runtime attempt: %w", err)
	}
	attempt, readErr := getInvocationRuntimeAttempt(
		ctx, tx, effect.IsolationDomainID, effect.OperationID,
	)
	if readErr != nil {
		return InvocationRuntimeAttempt{}, readErr
	}
	if attempt.EffectID != effect.EffectID {
		return InvocationRuntimeAttempt{}, ErrInvocationRuntimeAttemptConflict
	}
	if update.RowsAffected() != 1 {
		if attempt.Status != "succeeded" {
			return InvocationRuntimeAttempt{}, ErrInvocationRuntimeAttemptAmbiguous
		}
		persisted, encodeErr := json.Marshal(attempt.Result)
		if encodeErr != nil {
			return InvocationRuntimeAttempt{}, fmt.Errorf("encode persisted invocation runtime result: %w", encodeErr)
		}
		if !bytes.Equal(persisted, encodedResult) {
			return InvocationRuntimeAttempt{}, ErrInvocationRuntimeAttemptConflict
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return InvocationRuntimeAttempt{}, fmt.Errorf("commit invocation runtime completion: %w", err)
	}
	return attempt, nil
}

func (repository *Repository) GetInvocationRuntimeAttempt(
	ctx context.Context,
	isolationDomainID string,
	operationID string,
) (InvocationRuntimeAttempt, error) {
	if isolationDomainID == "" || operationID == "" {
		return InvocationRuntimeAttempt{}, ErrInvocationRuntimeAttemptInvalid
	}
	return getInvocationRuntimeAttempt(ctx, repository.pool, isolationDomainID, operationID)
}

func getInvocationRuntimeAttempt(
	ctx context.Context,
	querier operationQuerier,
	isolationDomainID string,
	operationID string,
) (InvocationRuntimeAttempt, error) {
	var attempt InvocationRuntimeAttempt
	var encodedResult []byte
	err := querier.QueryRow(ctx, `
		SELECT isolation_domain_id, operation_id, effect_id, lease_owner,
		       fencing_token, status, result
		FROM invocation_runtime_attempts
		WHERE isolation_domain_id = $1 AND operation_id = $2
	`, isolationDomainID, operationID).Scan(
		&attempt.IsolationDomainID,
		&attempt.OperationID,
		&attempt.EffectID,
		&attempt.LeaseOwner,
		&attempt.FencingToken,
		&attempt.Status,
		&encodedResult,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InvocationRuntimeAttempt{}, ErrInvocationRuntimeAttemptMissing
	}
	if err != nil {
		return InvocationRuntimeAttempt{}, fmt.Errorf("read invocation runtime attempt: %w", err)
	}
	if len(encodedResult) > 0 {
		if err := json.Unmarshal(encodedResult, &attempt.Result); err != nil {
			return InvocationRuntimeAttempt{}, fmt.Errorf("decode invocation runtime result: %w", err)
		}
	}
	if attempt.IsolationDomainID != isolationDomainID ||
		attempt.OperationID != operationID ||
		attempt.EffectID == "" ||
		attempt.LeaseOwner == "" ||
		attempt.FencingToken <= 0 ||
		(attempt.Status != "reserved" && attempt.Status != "succeeded") ||
		(attempt.Status == "reserved" && attempt.Result != nil) ||
		(attempt.Status == "succeeded" && attempt.Result == nil) {
		return InvocationRuntimeAttempt{}, ErrInvocationRuntimeAttemptConflict
	}
	return attempt, nil
}

func validInvocationRuntimeAttempt(claim OperationClaim, effect EffectRecord) bool {
	expectedEffectID := identity.Derived(
		"eff",
		claim.IsolationDomainID+":"+claim.Kind+":"+claim.ID+":run-invocation",
	)
	return validInvocationRuntimeClaim(claim) &&
		effect.IsolationDomainID == claim.IsolationDomainID &&
		effect.OperationKind == claim.Kind &&
		effect.OperationID == claim.ID &&
		effect.Phase == "run-invocation" &&
		effect.EffectID == expectedEffectID &&
		effect.Status == "prepared"
}

func verifyInvocationRuntimeEffect(
	ctx context.Context,
	querier operationQuerier,
	effect EffectRecord,
) error {
	persisted, err := getEffect(ctx, querier, effect.IsolationDomainID, effect.EffectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvocationRuntimeAttemptInvalid
	}
	if err != nil {
		return err
	}
	if persisted.OperationKind != effect.OperationKind ||
		persisted.OperationID != effect.OperationID ||
		persisted.Phase != effect.Phase ||
		persisted.RequestDigest != effect.RequestDigest ||
		persisted.Status != "prepared" {
		return ErrInvocationRuntimeAttemptConflict
	}
	return nil
}
