package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestDevelopmentEnforcementLiveInstallation(t *testing.T) {
	compilerEndpoint := os.Getenv("DATAGROUND_ROSETTA_CONFORMANCE_ENDPOINT")
	databaseURL := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	s3Endpoint, bucket := os.Getenv("DATAGROUND_TEST_S3_ENDPOINT"), os.Getenv("DATAGROUND_TEST_S3_BUCKET")
	if compilerEndpoint == "" || s3Endpoint == "" || bucket == "" || databaseURL == "" {
		t.Skip("live Rosetta, PostgreSQL and S3 endpoints are required")
	}
	t.Setenv("DATAGROUND_DATABASE_URL", databaseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateUp(ctx, db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	config := testConfiguration(t)
	config.rosettaEndpoint = compilerEndpoint
	config.s3Endpoint = s3Endpoint
	config.bucket = bucket
	config.domainID = identity.New("iso")
	config.revisionID = identity.New("rev")
	config.correlationID = identity.New("cor")
	serviceID := identity.New("svc")
	create := func(domainID, profile string) {
		t.Helper()
		idempotency := func(key string) persistence.Idempotency {
			return persistence.Idempotency{IsolationDomainID: domainID, Method: "POST", Path: "/integration/" + key, Key: key, RequestDigest: sha256.Sum256([]byte(key))}
		}
		if _, err := repository.CreateService(ctx, idempotency("create-service"), persistence.CreateServiceInput{ID: serviceID, Name: "development enforcement", ActorID: config.actorID, CorrelationID: config.correlationID}); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.CreateRevision(ctx, idempotency("create-revision"), persistence.CreateRevisionInput{ID: config.revisionID, ServiceID: serviceID, RuntimeProfile: profile, RequiredCapabilities: []string{profile}, ActorID: config.actorID, CorrelationID: config.correlationID}); err != nil {
			t.Fatal(err)
		}
	}
	create(config.domainID, developmentRuntimeProfile)
	var first bytes.Buffer
	if err := run(ctx, arguments(config), &first); err != nil {
		t.Fatal("live materialization and installation", err)
	}
	var receipt map[string]any
	if json.Unmarshal(first.Bytes(), &receipt) != nil || len(receipt) != 7 || receipt["certificationEligible"] != false || receipt["digest"] != developmentPolicyDigest {
		t.Fatal("unsafe installation receipt")
	}
	var second bytes.Buffer
	if err := run(ctx, arguments(config), &second); err != nil || !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("exact replay changed receipt", err)
	}
	store := executionpostgres.New(pool)
	record, err := store.GetEnforcementBundleRecord(ctx, config.domainID, receipt["bundleId"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='enforcement-bundle.bind' AND resource_id=$2 AND actor_id=$3 AND correlation_id=$4`, config.domainID, record.ID, config.actorID, config.correlationID).Scan(&count); err != nil || count != 1 {
		t.Fatal("binding audit not atomic or replayable", count, err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	objects, err := s3store.New(s3store.Config{Endpoint: s3Endpoint, Bucket: bucket, AddressingStyle: s3store.PathStyle, AllowHTTPForLoopback: true, HTTPClient: &http.Client{Transport: transport, Timeout: 10 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	source, err := execution.NewObjectEnforcementBundleSource(store, objects)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := source.GetEnforcementBundle(ctx, config.domainID, record.ID)
	if err != nil || execution.VerifyEnforcementPolicy(bundle.Content, developmentPolicyDigest) != nil {
		t.Fatal("installed bytes unavailable to admission", err)
	}
	clear(bundle.Content)
	foreign := config
	foreign.domainID = identity.New("iso")
	create(foreign.domainID, developmentRuntimeProfile)
	var foreignOutput bytes.Buffer
	if err := run(ctx, arguments(foreign), &foreignOutput); err != nil || bytes.Equal(first.Bytes(), foreignOutput.Bytes()) {
		t.Fatal("foreign scope reused installation", err)
	}
	if _, err := store.GetEnforcementBundleRecord(ctx, foreign.domainID, record.ID); err != execution.ErrEnforcementBundleMissing {
		t.Fatal("foreign scope read original binding", err)
	}
	unsupported := config
	unsupported.domainID = identity.New("iso")
	create(unsupported.domainID, "reference/v1")
	if err := run(ctx, arguments(unsupported), &bytes.Buffer{}); err != errInstallation {
		t.Fatal("reference revision admitted strict material", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM service_revision_enforcement_bundles WHERE isolation_domain_id=$1`, unsupported.domainID).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected revision acquired bundle", err)
	}
}
