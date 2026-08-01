package persistence

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/asabla/dataground/internal/authn"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrDPoPReplayReservationInvalid = errors.New("DPoP replay reservation is invalid")
	dpopIsolationDomainPattern      = regexp.MustCompile(`^iso_[0-9a-z]{20,32}$`)
)

func (repository *Repository) ReserveDPoPProof(
	ctx context.Context,
	reservation authn.DPoPReplayReservation,
) error {
	if repository == nil || repository.pool == nil || !reservation.Valid() {
		return ErrDPoPReplayReservationInvalid
	}
	if _, err := repository.pool.Exec(ctx, `
		INSERT INTO oidc_dpop_replays (
			isolation_domain_id,
			key_thumbprint_digest,
			proof_id_digest,
			expires_at
		) VALUES ($1, $2, $3, $4)
	`,
		reservation.IsolationDomainID,
		reservation.KeyThumbprintDigest[:],
		reservation.ProofIDDigest[:],
		reservation.ExpiresAt,
	); err != nil {
		var postgresErr *pgconn.PgError
		if errors.As(err, &postgresErr) && postgresErr.Code == "23505" &&
			postgresErr.ConstraintName == "oidc_dpop_replays_proof_key" {
			return authn.ErrDPoPProofReplayed
		}
		return fmt.Errorf("reserve DPoP proof: %w", err)
	}
	return nil
}

func (repository *Repository) DeleteExpiredDPoPProofs(
	ctx context.Context,
	isolationDomainID string,
	limit int,
) (int64, error) {
	if repository == nil || repository.pool == nil ||
		!dpopIsolationDomainPattern.MatchString(isolationDomainID) || limit < 1 || limit > 1000 {
		return 0, ErrDPoPReplayReservationInvalid
	}
	result, err := repository.pool.Exec(ctx, `
		WITH expired AS (
			SELECT sequence
			FROM oidc_dpop_replays
			WHERE isolation_domain_id = $1
			  AND expires_at <= clock_timestamp()
			ORDER BY sequence
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM oidc_dpop_replays AS replay
		USING expired
		WHERE replay.sequence = expired.sequence
	`, isolationDomainID, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired DPoP proofs: %w", err)
	}
	return result.RowsAffected(), nil
}

var _ authn.DPoPReplayStore = (*Repository)(nil)
