package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestInvocationArtifactCatalogIsFencedAtomicAuditedAndReplayable(t *testing.T) {
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

	now := time.Now().UTC()
	domainID := identity.New("iso")
	serviceID := identity.New("svc")
	revisionID := identity.New("rev")
	invocationID := identity.New("inv")
	operationID := identity.New("op")
	effectID := identity.Derived(
		"eff",
		domainID+":"+persistence.OperationKindInvocation+":"+operationID+":run-invocation",
	)
	actorID := "runtime:artifact-finalizer"
	correlationID := "correlation:artifact-finalizer"
	leaseOwner := "worker:artifact-finalizer"
	const fencingToken int64 = 7

	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_services (
			isolation_domain_id, id, name, description, created_at, updated_at, created_by
		) VALUES ($1, $2, 'artifact fixture', '', $3, $3, 'test:author')
	`, domainID, serviceID, now); err != nil {
		t.Fatalf("insert service fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_revisions (
			isolation_domain_id, id, service_id, revision_number, state,
			runtime_profile, required_capabilities, created_at, updated_at, created_by
		) VALUES (
			$1, $2, $3, 1, 'published',
			'codex.app-server/v1', ARRAY['runtime.codex'], $4, $4, 'test:author'
		)
	`, domainID, revisionID, serviceID, now); err != nil {
		t.Fatalf("insert revision fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO invocations (
			isolation_domain_id, id, service_id, revision_id, alias, state,
			input, correlation_id, operation_id, created_at, updated_at, created_by
		) VALUES (
			$1, $2, $3, $4, 'stable', 'running',
			'{}', $5, $6, $7, $7, 'test:requester'
		)
	`, domainID, invocationID, serviceID, revisionID, correlationID, operationID, now); err != nil {
		t.Fatalf("insert invocation fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO invocation_execution_operations (
			isolation_domain_id, id, invocation_id, command, desired_state,
			observed_state, state_machine_version, attempt, lease_owner,
			lease_token, lease_expires_at, due_at, deadline_at,
			correlation_id, actor_id, effect_correlation_id, effect_actor_id,
			last_transition_at, created_at, updated_at
		) VALUES (
			$1, $2, $3, 'invoke', 'succeeded',
			'running', 2, 1, $4,
			$5, $6, $7, $8,
			'correlation:request', 'test:requester', $9, $10,
			$7, $7, $7
		)
	`,
		domainID,
		operationID,
		invocationID,
		leaseOwner,
		fencingToken,
		now.Add(5*time.Minute),
		now,
		now.Add(10*time.Minute),
		correlationID,
		actorID,
	); err != nil {
		t.Fatalf("insert operation fixture: %v", err)
	}
	requestDigest := sha256.Sum256([]byte("run-invocation"))
	if _, err := pool.Exec(ctx, `
		INSERT INTO external_effects (
			isolation_domain_id, effect_id, operation_kind, operation_id,
			phase, request_digest, status, created_at, updated_at
		) VALUES (
			$1, $2, 'invocation-execution', $3,
			'run-invocation', $4, 'prepared', $5, $5
		)
	`, domainID, effectID, operationID, requestDigest[:], now); err != nil {
		t.Fatalf("insert effect fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO invocation_runtime_attempts (
			isolation_domain_id, operation_id, effect_id, lease_owner,
			fencing_token, status, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, 'reserved', $6, $6)
	`, domainID, operationID, effectID, leaseOwner, fencingToken, now); err != nil {
		t.Fatalf("insert runtime attempt fixture: %v", err)
	}

	content := []byte("artifact content")
	digest := sha256.Sum256(content)
	record := artifact.Record{
		SchemaVersion:     artifact.InvocationArtifactSchemaV1,
		IsolationDomainID: domainID,
		ID:                identity.New("art"),
		InvocationID:      invocationID,
		OperationID:       operationID,
		EffectID:          effectID,
		Name:              "result.txt",
		Kind:              "file",
		MediaType:         "text/plain",
		SizeBytes:         int64(len(content)),
		Digest:            "sha256:" + hex.EncodeToString(digest[:]),
		Sensitive:         true,
	}
	record.ObjectKey = artifact.ObjectKey(record)
	binding := artifact.Binding{
		Record:              record,
		ActorID:             actorID,
		CorrelationID:       correlationID,
		LeaseOwner:          leaseOwner,
		FencingToken:        fencingToken,
		StateMachineVersion: artifact.InvocationArtifactStateMachine,
	}
	repository := persistence.NewRepository(pool)
	bound, err := repository.BindInvocationArtifact(ctx, binding)
	if err != nil || !artifact.EqualRecords(bound, record) {
		t.Fatalf("bind invocation artifact = (%#v, %v)", bound, err)
	}
	restored, err := repository.GetInvocationArtifactRecord(ctx, domainID, record.ID)
	if err != nil || !artifact.EqualRecords(restored, record) {
		t.Fatalf("restore invocation artifact = (%#v, %v)", restored, err)
	}
	public, err := repository.GetArtifact(ctx, domainID, invocationID, record.ID)
	if err != nil ||
		public.State != "available" ||
		public.Name != record.Name ||
		public.Digest != record.Digest ||
		public.Sensitive != record.Sensitive {
		t.Fatalf("public invocation artifact = (%#v, %v)", public, err)
	}

	if endpoint, bucket := os.Getenv("DATAGROUND_TEST_S3_ENDPOINT"), os.Getenv("DATAGROUND_TEST_S3_BUCKET"); endpoint != "" || bucket != "" {
		if endpoint == "" || bucket == "" {
			t.Fatal("invocation artifact S3 conformance configuration is incomplete")
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		defer transport.CloseIdleConnections()
		store, err := s3store.New(s3store.Config{
			Endpoint:             endpoint,
			Bucket:               bucket,
			AddressingStyle:      s3store.PathStyle,
			AllowHTTPForLoopback: true,
			HTTPClient:           &http.Client{Transport: transport, Timeout: 10 * time.Second},
		})
		if err != nil {
			t.Fatalf("construct invocation artifact S3 store: %v", err)
		}
		objects, err := s3store.NewArtifactStore(store, 1024*1024)
		if err != nil {
			t.Fatalf("construct bounded invocation artifact store: %v", err)
		}
		finalizer, err := artifact.NewFinalizer(
			repository,
			objects,
			objects,
			artifact.FinalizerConfig{MaximumBytes: 1024 * 1024},
		)
		if err != nil {
			t.Fatalf("construct invocation artifact finalizer: %v", err)
		}
		liveContent := []byte("composed invocation artifact recovery")
		liveDigest := sha256.Sum256(liveContent)
		liveRecord := record
		liveRecord.ID = identity.New("art")
		liveRecord.Name = "composed-recovery.txt"
		liveRecord.SizeBytes = int64(len(liveContent))
		liveRecord.Digest = "sha256:" + hex.EncodeToString(liveDigest[:])
		liveRecord.ObjectKey = artifact.ObjectKey(liveRecord)
		liveBinding := binding
		liveBinding.Record = liveRecord
		ambiguous := &acknowledgementLossCatalog{Catalog: repository}
		uncertain, err := artifact.NewFinalizer(
			ambiguous,
			objects,
			objects,
			artifact.FinalizerConfig{MaximumBytes: 1024 * 1024},
		)
		if err != nil {
			t.Fatalf("construct acknowledgement-loss finalizer: %v", err)
		}
		if _, err := uncertain.Finalize(ctx, artifact.Finalization{
			Binding: liveBinding,
			Content: liveContent,
		}); !errors.Is(err, artifact.ErrInvocationArtifactUnavailable) {
			t.Fatalf("lost catalog acknowledgement = %v, want unavailable", err)
		}
		if !ambiguous.lost {
			t.Fatal("catalog acknowledgement was not lost after a durable bind")
		}
		const concurrentReplays = 8
		results := make(chan error, concurrentReplays)
		var group sync.WaitGroup
		for range concurrentReplays {
			group.Add(1)
			go func() {
				defer group.Done()
				observed, err := finalizer.Finalize(ctx, artifact.Finalization{
					Binding: liveBinding,
					Content: liveContent,
				})
				if err == nil && !artifact.EqualRecords(observed, liveRecord) {
					err = artifact.ErrInvocationArtifactConflict
				}
				results <- err
			}()
		}
		group.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatalf("concurrent artifact recovery: %v", err)
			}
		}
		conflictingFinalization := artifact.Finalization{
			Binding: liveBinding,
			Content: []byte("different"),
		}
		if _, err := finalizer.Finalize(ctx, conflictingFinalization); !errors.Is(
			err,
			artifact.ErrInvocationArtifactInvalid,
		) {
			t.Fatalf("conflicting artifact recovery = %v, want invalid", err)
		}
		var liveObjects, liveDescriptors, liveAudits int
		if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT count(*) FROM invocation_artifact_objects
				 WHERE isolation_domain_id = $1 AND id = $2),
				(SELECT count(*) FROM artifacts
				 WHERE isolation_domain_id = $1 AND id = $2),
				(SELECT count(*) FROM audit_records
				 WHERE isolation_domain_id = $1
				   AND resource_type = 'invocation-artifact'
				   AND resource_id = $2
				   AND action = 'invocation-artifact.bind')
		`, domainID, liveRecord.ID).Scan(&liveObjects, &liveDescriptors, &liveAudits); err != nil {
			t.Fatalf("inspect composed invocation artifact recovery: %v", err)
		}
		if liveObjects != 1 || liveDescriptors != 1 || liveAudits != 1 {
			t.Fatalf(
				"composed invocation artifact recovery = objects %d, descriptors %d, audits %d",
				liveObjects,
				liveDescriptors,
				liveAudits,
			)
		}
	}

	if _, err := pool.Exec(ctx, `
		UPDATE invocation_execution_operations
		SET lease_expires_at = $3
		WHERE isolation_domain_id = $1 AND id = $2
	`, domainID, operationID, now.Add(-time.Minute)); err != nil {
		t.Fatalf("expire invocation artifact claim: %v", err)
	}
	replayed, err := repository.BindInvocationArtifact(ctx, binding)
	if err != nil || !artifact.EqualRecords(replayed, record) {
		t.Fatalf("replay committed invocation artifact = (%#v, %v)", replayed, err)
	}
	conflicting := binding
	conflicting.Record.Name = "replacement.txt"
	if _, err := repository.BindInvocationArtifact(ctx, conflicting); !errors.Is(
		err,
		artifact.ErrInvocationArtifactConflict,
	) {
		t.Fatalf("replace invocation artifact = %v, want conflict", err)
	}

	newRecord := record
	newRecord.ID = identity.New("art")
	newRecord.ObjectKey = artifact.ObjectKey(newRecord)
	staleBinding := binding
	staleBinding.Record = newRecord
	if _, err := repository.BindInvocationArtifact(ctx, staleBinding); !errors.Is(
		err,
		persistence.ErrLeaseLost,
	) {
		t.Fatalf("bind with expired claim = %v, want lease loss", err)
	}
	if _, err := repository.GetInvocationArtifactRecord(
		ctx,
		identity.New("iso"),
		record.ID,
	); !errors.Is(err, artifact.ErrInvocationArtifactMissing) {
		t.Fatalf("cross-domain artifact lookup = %v, want missing", err)
	}

	var auditCount int
	var auditActor, auditCorrelation, auditMetadata string
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(actor_id), min(correlation_id), min(safe_metadata::text)
		FROM audit_records
		WHERE isolation_domain_id = $1
		  AND resource_type = 'invocation-artifact'
		  AND resource_id = $2
		  AND action = 'invocation-artifact.bind'
	`, domainID, record.ID).Scan(
		&auditCount,
		&auditActor,
		&auditCorrelation,
		&auditMetadata,
	); err != nil {
		t.Fatalf("read invocation artifact audit: %v", err)
	}
	if auditCount != 1 || auditActor != actorID || auditCorrelation != correlationID {
		t.Fatalf(
			"invocation artifact audit = count %d, actor %q, correlation %q",
			auditCount,
			auditActor,
			auditCorrelation,
		)
	}
	if strings.Contains(auditMetadata, record.ObjectKey) ||
		strings.Contains(auditMetadata, record.Name) {
		t.Fatalf("audit metadata exposes protected artifact routing or name: %s", auditMetadata)
	}

	if _, err := pool.Exec(ctx, `
		DELETE FROM invocations
		WHERE isolation_domain_id = $1 AND id = $2
	`, domainID, invocationID); err != nil {
		t.Fatalf("delete invocation fixture: %v", err)
	}
	if _, err := repository.GetInvocationArtifactRecord(
		ctx,
		domainID,
		record.ID,
	); !errors.Is(err, artifact.ErrInvocationArtifactMissing) {
		t.Fatalf("artifact after invocation deletion = %v, want missing", err)
	}
}

type acknowledgementLossCatalog struct {
	artifact.Catalog
	lost bool
}

func (catalog *acknowledgementLossCatalog) BindInvocationArtifact(
	ctx context.Context,
	binding artifact.Binding,
) (artifact.Record, error) {
	bound, err := catalog.Catalog.BindInvocationArtifact(ctx, binding)
	if err != nil {
		return artifact.Record{}, err
	}
	if !catalog.lost {
		catalog.lost = true
		return artifact.Record{}, errors.New("catalog acknowledgement lost")
	}
	return bound, nil
}
