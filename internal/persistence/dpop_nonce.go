package persistence

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/asabla/dataground/internal/authn"
)

const dpopNonceCleanupLimit = 128

var ErrDPoPNonceRequestInvalid = errors.New("DPoP nonce request is invalid")

func (repository *Repository) EvaluateDPoPNonce(
	ctx context.Context,
	request authn.DPoPNonceRequest,
) (authn.DPoPNonceDecision, error) {
	if repository == nil || repository.pool == nil || ctx == nil || !request.Valid() {
		return authn.DPoPNonceDecision{}, ErrDPoPNonceRequestInvalid
	}
	keyDigest := request.KeyThumbprintDigest()
	presentedDigest := request.PresentedNonceDigest()
	if presentedDigest != ([sha256.Size]byte{}) {
		var accepted bool
		if err := repository.pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM oidc_dpop_nonces
				WHERE isolation_domain_id = $1
				  AND key_thumbprint_digest = $2
				  AND nonce_digest = $3
				  AND expires_at > clock_timestamp()
			)
		`,
			request.IsolationDomainID(),
			keyDigest[:],
			presentedDigest[:],
		).Scan(&accepted); err != nil {
			return authn.DPoPNonceDecision{}, fmt.Errorf("validate DPoP nonce: %w", err)
		}
		if accepted {
			return authn.AcceptDPoPNonce(), nil
		}
	}

	rawNonce := make([]byte, 32)
	if _, err := rand.Read(rawNonce); err != nil {
		clear(rawNonce)
		return authn.DPoPNonceDecision{}, errors.New("generate DPoP nonce")
	}
	challenge := base64.RawURLEncoding.EncodeToString(rawNonce)
	clear(rawNonce)
	decision, err := authn.NewDPoPNonceChallengeDecision(challenge)
	if err != nil {
		return authn.DPoPNonceDecision{}, errors.New("generate DPoP nonce")
	}
	nonceDigest := sha256.Sum256([]byte(challenge))
	tx, err := repository.pool.Begin(ctx)
	if err != nil {
		return authn.DPoPNonceDecision{}, fmt.Errorf("begin DPoP nonce issue: %w", err)
	}
	defer tx.Rollback(ctx)
	lockKey := request.IsolationDomainID() + "\n" + hex.EncodeToString(keyDigest[:])
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return authn.DPoPNonceDecision{}, fmt.Errorf("lock DPoP nonce binding: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		WITH expired AS (
			SELECT sequence
			FROM oidc_dpop_nonces
			WHERE expires_at <= clock_timestamp()
			ORDER BY sequence
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM oidc_dpop_nonces AS nonce
		USING expired
		WHERE nonce.sequence = expired.sequence
	`, dpopNonceCleanupLimit); err != nil {
		return authn.DPoPNonceDecision{}, fmt.Errorf("reclaim expired DPoP nonces: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		WITH issued AS (
			SELECT clock_timestamp() AS issued_at
		)
		INSERT INTO oidc_dpop_nonces (
			isolation_domain_id,
			key_thumbprint_digest,
			nonce_digest,
			expires_at,
			issued_at
		)
		SELECT $1, $2, $3,
		       issued_at + $4::bigint * interval '1 microsecond',
		       issued_at
		FROM issued
	`,
		request.IsolationDomainID(),
		keyDigest[:],
		nonceDigest[:],
		request.Lifetime().Microseconds(),
	); err != nil {
		return authn.DPoPNonceDecision{}, fmt.Errorf("issue DPoP nonce: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		WITH surplus AS (
			SELECT sequence
			FROM oidc_dpop_nonces
			WHERE isolation_domain_id = $1
			  AND key_thumbprint_digest = $2
			ORDER BY issued_at DESC, sequence DESC
			OFFSET $3
			FOR UPDATE
		)
		DELETE FROM oidc_dpop_nonces AS nonce
		USING surplus
		WHERE nonce.sequence = surplus.sequence
	`, request.IsolationDomainID(), keyDigest[:], request.MaximumActivePerKey()); err != nil {
		return authn.DPoPNonceDecision{}, fmt.Errorf("bound active DPoP nonces: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return authn.DPoPNonceDecision{}, fmt.Errorf("commit DPoP nonce issue: %w", err)
	}
	return decision, nil
}

var _ authn.DPoPNonceStore = (*Repository)(nil)
