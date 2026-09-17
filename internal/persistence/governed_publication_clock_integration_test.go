package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
)

func TestDevelopmentPublicationConsumerIgnoresWorkerClockForLease(t *testing.T) {
	url := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("DATAGROUND_REQUIRE_TEST_DATABASE") == "true" {
			t.Fatal("test database required")
		}
		t.Skip("DATAGROUND_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := OpenSQL(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := MigrateUp(ctx, db); err != nil {
		t.Fatal(err)
	}
	pool, err := OpenPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := NewRepository(pool)
	scope, service, revision := identity.New("iso"), identity.New("svc"), identity.New("rev")
	defer func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE governed_publication_requests; DELETE FROM service_publication_operations WHERE state_machine_version=3; TRUNCATE audit_records`); err != nil {
			t.Error(err)
		}
	}()
	idem := func(key string) Idempotency {
		return Idempotency{IsolationDomainID: scope, Method: "POST", Path: "/fixture", Key: key, RequestDigest: sha256.Sum256([]byte(key))}
	}
	if _, err := repo.CreateService(ctx, idem("create-service"), CreateServiceInput{ID: service, Name: "clock fixture", ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRevision(ctx, idem("create-revision"), CreateRevisionInput{ID: revision, ServiceID: service, RuntimeProfile: GovernedInvocationRuntimeProfile, RequiredCapabilities: []string{GovernedInvocationRuntimeProfile}, ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	input := DevelopmentPublicationInput{Contract: DevelopmentPublicationContract, Target: InvocationDispatchTarget{IsolationDomainID: scope, ServiceID: service, RevisionID: revision, RuntimeProfile: GovernedInvocationRuntimeProfile}, ExpectedVersion: 1, PlanDigest: "sha256:" + strings.Repeat("a", 64), PolicyDigest: "sha256:" + strings.Repeat("b", 64), VerificationDigest: "sha256:" + strings.Repeat("c", 64), ActorID: "operator", CorrelationID: identity.New("cor")}
	result, err := repo.QueueDevelopmentPublication(ctx, idem("queue-publication"), input, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var operation domain.Operation
	if json.Unmarshal(result.Body, &operation) != nil {
		t.Fatal("invalid acceptance")
	}
	repo.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	claim, err := repo.ClaimDevelopmentPublication(ctx, operation.Metadata.ID, input, "ahead-worker", time.Minute)
	if err != nil || claim == nil {
		t.Fatal("database-time claim failed", err)
	}
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	if !claim.LeaseExpiresAt.After(now) || claim.LeaseExpiresAt.After(now.Add(time.Minute)) {
		t.Fatal("worker clock controlled lease expiry")
	}
	if next, err := repo.ClaimDevelopmentPublication(ctx, operation.Metadata.ID, input, "ahead-contender", time.Minute); err != nil || next != nil {
		t.Fatal("ahead worker reclaimed active database lease", err)
	}
	repo.now = func() time.Time { return time.Now().Add(-24 * time.Hour) }
	if _, err := pool.Exec(ctx, `UPDATE service_publication_operations SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, scope, operation.Metadata.ID); err != nil {
		t.Fatal(err)
	}
	next, err := repo.ClaimDevelopmentPublication(ctx, operation.Metadata.ID, input, "behind-replacement", time.Minute)
	if err != nil || next == nil || next.FencingToken <= claim.FencingToken {
		t.Fatal("behind worker could not reclaim expired database lease", err)
	}
}
