package persistence_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestDPoPNoncesAreUnpredictableScopedBoundedAndReclaimable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL := testDatabaseURL(t)
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 0); err != nil {
		database.Close()
		t.Fatalf("reset schema: %v", err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		database.Close()
		t.Fatalf("migrate schema: %v", err)
	}
	database.Close()
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository := persistence.NewRepository(pool)

	domainID := identity.New("iso")
	keyDigest := sha256.Sum256([]byte("nonce-bound-key"))
	request := newDPoPNonceRequest(t, domainID, keyDigest, [sha256.Size]byte{}, 2)
	first, err := repository.EvaluateDPoPNonce(ctx, request)
	if err != nil || !first.Valid() || first.Accepted() || first.Challenge() == "" {
		t.Fatalf("first nonce decision = %#v, %v", first, err)
	}
	second, err := repository.EvaluateDPoPNonce(ctx, request)
	if err != nil || !second.Valid() || second.Accepted() ||
		second.Challenge() == first.Challenge() {
		t.Fatalf("second nonce decision = %#v, %v", second, err)
	}

	presented := sha256.Sum256([]byte(first.Challenge()))
	accepted, err := repository.EvaluateDPoPNonce(
		ctx,
		newDPoPNonceRequest(t, domainID, keyDigest, presented, 2),
	)
	if err != nil || !accepted.Valid() || !accepted.Accepted() {
		t.Fatalf("accepted nonce decision = %#v, %v", accepted, err)
	}
	accepted, err = repository.EvaluateDPoPNonce(
		ctx,
		newDPoPNonceRequest(t, domainID, keyDigest, presented, 2),
	)
	if err != nil || !accepted.Valid() || !accepted.Accepted() {
		t.Fatalf("reused nonce decision = %#v, %v", accepted, err)
	}
	otherKey := sha256.Sum256([]byte("other-nonce-bound-key"))
	foreign, err := repository.EvaluateDPoPNonce(
		ctx,
		newDPoPNonceRequest(t, domainID, otherKey, presented, 2),
	)
	if err != nil || !foreign.Valid() || foreign.Accepted() {
		t.Fatalf("foreign-key nonce decision = %#v, %v", foreign, err)
	}

	for range 4 {
		if _, err := repository.EvaluateDPoPNonce(ctx, request); err != nil {
			t.Fatalf("rotate nonce: %v", err)
		}
	}
	var active int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM oidc_dpop_nonces
		WHERE isolation_domain_id = $1
		  AND key_thumbprint_digest = $2
		  AND expires_at > clock_timestamp()
	`, domainID, keyDigest[:]).Scan(&active); err != nil {
		t.Fatalf("count active nonces: %v", err)
	}
	if active != 2 {
		t.Fatalf("active nonces = %d, want bounded overlap 2", active)
	}
	maximumLifetimeRequest, err := authn.NewDPoPNonceRequest(
		domainID,
		otherKey,
		[sha256.Size]byte{},
		5*time.Minute,
		2,
	)
	if err != nil {
		t.Fatalf("create maximum-lifetime nonce request: %v", err)
	}
	if decision, err := repository.EvaluateDPoPNonce(ctx, maximumLifetimeRequest); err != nil ||
		!decision.Valid() || decision.Accepted() {
		t.Fatalf("maximum-lifetime nonce decision = %#v, %v", decision, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE oidc_dpop_nonces
		SET expires_at = expires_at + interval '1 minute'
		WHERE isolation_domain_id = $1
	`, domainID); err == nil {
		t.Fatal("nonce mutation was accepted")
	}

	expiredDigest := sha256.Sum256([]byte("expired-nonce"))
	if _, err := pool.Exec(ctx, `
		INSERT INTO oidc_dpop_nonces (
			isolation_domain_id,
			key_thumbprint_digest,
			nonce_digest,
			expires_at,
			issued_at
		) VALUES (
			$1, $2, $3,
			clock_timestamp() - interval '1 minute',
			clock_timestamp() - interval '2 minutes'
		)
	`, domainID, otherKey[:], expiredDigest[:]); err != nil {
		t.Fatalf("insert expired nonce: %v", err)
	}
	if _, err := repository.EvaluateDPoPNonce(ctx, request); err != nil {
		t.Fatalf("issue nonce with reclamation: %v", err)
	}
	var expired int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM oidc_dpop_nonces
		WHERE nonce_digest = $1
	`, expiredDigest[:]).Scan(&expired); err != nil {
		t.Fatalf("count expired nonce: %v", err)
	}
	if expired != 0 {
		t.Fatalf("expired nonces = %d, want reclaimed", expired)
	}

	if _, err := authn.NewDPoPNonceRequest("invalid", keyDigest, presented, time.Minute, 2); err == nil {
		t.Fatal("invalid nonce request was accepted")
	}
	cancelled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if _, err := repository.EvaluateDPoPNonce(cancelled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled nonce evaluation = %v", err)
	}
}

func newDPoPNonceRequest(
	t *testing.T,
	domainID string,
	keyDigest [sha256.Size]byte,
	presentedDigest [sha256.Size]byte,
	maximumActive uint32,
) authn.DPoPNonceRequest {
	t.Helper()
	request, err := authn.NewDPoPNonceRequest(
		domainID,
		keyDigest,
		presentedDigest,
		time.Minute,
		maximumActive,
	)
	if err != nil {
		t.Fatalf("create DPoP nonce request: %v", err)
	}
	return request
}
