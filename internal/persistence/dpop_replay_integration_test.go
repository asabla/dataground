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

func TestDPoPReplayReservationsAreScopedProtectedAndReclaimable(t *testing.T) {
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
	otherDomainID := identity.New("iso")
	keyDigest := sha256.Sum256([]byte("key-thumbprint"))
	proofDigest := sha256.Sum256([]byte("proof-identifier"))
	reservation := authn.DPoPReplayReservation{
		IsolationDomainID: domainID, KeyThumbprintDigest: keyDigest,
		ProofIDDigest: proofDigest, ExpiresAt: time.Now().Add(time.Minute),
	}
	if err := repository.ReserveDPoPProof(ctx, reservation); err != nil {
		t.Fatalf("reserve DPoP proof: %v", err)
	}
	reservation.IsolationDomainID = otherDomainID
	if err := repository.ReserveDPoPProof(ctx, reservation); !errors.Is(err, authn.ErrDPoPProofReplayed) {
		t.Fatalf("cross-domain replay error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE oidc_dpop_replays
		SET expires_at = expires_at + interval '1 minute'
		WHERE isolation_domain_id = $1
	`, domainID); err == nil {
		t.Fatal("replay reservation update was accepted")
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM oidc_dpop_replays
		WHERE isolation_domain_id = $1
	`, domainID); err == nil {
		t.Fatal("active replay reservation deletion was accepted")
	}

	expiredKey := sha256.Sum256([]byte("expired-key"))
	expiredProof := sha256.Sum256([]byte("expired-proof"))
	if _, err := pool.Exec(ctx, `
		INSERT INTO oidc_dpop_replays (
			isolation_domain_id, key_thumbprint_digest, proof_id_digest, expires_at, reserved_at
		) VALUES ($1, $2, $3, clock_timestamp() - interval '1 minute', clock_timestamp() - interval '2 minutes')
	`, domainID, expiredKey[:], expiredProof[:]); err != nil {
		t.Fatalf("insert expired reservation: %v", err)
	}
	deleted, err := repository.DeleteExpiredDPoPProofs(ctx, otherDomainID, 100)
	if err != nil || deleted != 0 {
		t.Fatalf("foreign-domain cleanup = %d, %v", deleted, err)
	}
	deleted, err = repository.DeleteExpiredDPoPProofs(ctx, domainID, 100)
	if err != nil || deleted != 1 {
		t.Fatalf("scope cleanup = %d, %v", deleted, err)
	}

	var remaining int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM oidc_dpop_replays WHERE isolation_domain_id = $1
	`, domainID).Scan(&remaining); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("remaining reservations = %d, want 1 active row", remaining)
	}
}
