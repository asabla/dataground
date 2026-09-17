package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5"
)

func TestGovernedWorkerWaitsForDatabasePublicationThenRequiresCertification(t *testing.T) {
	databaseURL := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("DATAGROUND_REQUIRE_TEST_DATABASE") == "true" {
			t.Fatal("DATAGROUND_TEST_DATABASE_URL is required")
		}
		t.Skip("DATAGROUND_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	// A separate disposable database prevents this command test from sharing
	// schema resets with the persistence package's concurrent test process.
	databaseName := "dataground_worker_" + identity.New("wait")
	quoted := pgx.Identifier{databaseName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
	}()
	address, err := url.Parse(databaseURL)
	if err != nil || address.Scheme != "postgres" && address.Scheme != "postgresql" {
		t.Fatal("test database URL must use PostgreSQL URI syntax")
	}
	address.Path = "/" + databaseName
	database, err := persistence.OpenSQL(ctx, address.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	pool, err := persistence.OpenPool(ctx, address.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := persistence.NewRepository(pool)
	target, revision := publicationWaitFixture()
	idem := persistence.Idempotency{IsolationDomainID: target.isolationDomainID, Method: "POST", Path: "/fixture", Key: "worker-wait-service", RequestDigest: [32]byte{1}}
	if _, err := repo.CreateService(ctx, idem, persistence.CreateServiceInput{ID: target.serviceID, Name: "publication wait", ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	idem.Key = "worker-wait-revision"
	if _, err := repo.CreateRevision(ctx, idem, persistence.CreateRevisionInput{ID: target.revisionID, ServiceID: target.serviceID, RuntimeProfile: revision.RuntimeProfile, RequiredCapabilities: revision.RequiredCapabilities, ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	observedDraft := make(chan struct{})
	var observed sync.Once
	reader := publicationWaitReader(func(ctx context.Context, scope, id string) (domain.ServiceRevision, error) {
		result, err := repo.GetServiceRevision(ctx, scope, id)
		if err == nil && result.State == "draft" {
			observed.Do(func() { close(observedDraft) })
		}
		return result, err
	})
	stopped := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); stopped <- awaitGovernedRevisionPublication(ctx, reader, target) }()
	defer func() { cancel(); <-finished }()
	select {
	case <-observedDraft:
	case err := <-stopped:
		t.Fatal("wait ended before reading draft", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-stopped:
		t.Fatal("draft released worker startup", err)
	default:
	}
	// This fixture changes only the persisted state observed by startup. It is
	// not runtime publication evidence and must not enable execution by itself.
	if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='published',published_at=clock_timestamp(),version=version+1 WHERE isolation_domain_id=$1 AND id=$2`, target.isolationDomainID, target.revisionID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	config := workerConfig{mode: workerModeGovernedDevelopment, waitForPublication: true, certification: runtimeCertificationConfig{target: target}}
	driver, resources, err := composeWorkerDriver(ctx, pool, repo, config)
	if !errors.Is(err, ErrRuntimeCertificationUnavailable) || driver != nil || resources != nil {
		t.Fatal("publication bypassed missing certification", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='retired' WHERE isolation_domain_id=$1 AND id=$2`, target.isolationDomainID, target.revisionID); err != nil {
		t.Fatal(err)
	}
	if err := awaitGovernedRevisionPublication(ctx, repo, target); !errors.Is(err, ErrRuntimeCertificationScopeMismatch) {
		t.Fatal("retired revision released startup", err)
	}
}
