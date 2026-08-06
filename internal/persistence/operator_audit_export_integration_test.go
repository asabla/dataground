package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOperatorAuditExportFreezesScopeAndAdvancesSequence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	domainID := "iso_00000000000000000001"
	otherDomainID := "iso_00000000000000000002"

	insertOperatorAuditRecord(t, ctx, pool, domainID, "cor_00000000000000000001", "oidc-provider-credential.activate")
	insertOperatorAuditRecord(t, ctx, pool, domainID, "correlation-lifecycle-retry", "oidc-provider-credential.revoke")
	insertOperatorAuditRecord(t, ctx, pool, otherDomainID, "cor_00000000000000000003", "oidc-provider-credential.activate")

	first, err := repository.ExportOperatorAuditRecords(ctx, domainID, "", 1)
	if err != nil {
		t.Fatalf("export first page: %v", err)
	}
	if first.SchemaVersion != persistence.OperatorAuditExportSchema || first.IsolationDomainID != domainID ||
		first.Cursor != "" || first.NextCursor == "" || first.Complete || len(first.Records) != 1 {
		t.Fatalf("first page = %#v", first)
	}
	insertOperatorAuditRecord(t, ctx, pool, domainID, "cor_00000000000000000004", "oidc-provider-credential.activate")
	second, err := repository.ExportOperatorAuditRecords(ctx, domainID, first.NextCursor, 10)
	if err != nil {
		t.Fatalf("export second page: %v", err)
	}
	if !second.Complete || second.Cursor != first.NextCursor || len(second.Records) != 1 {
		t.Fatalf("second page = %#v", second)
	}
	for _, record := range append(first.Records, second.Records...) {
		if record.CorrelationID == "cor_00000000000000000004" || record.CorrelationID == "cor_00000000000000000003" {
			t.Fatalf("frozen export crossed its bound or scope: %#v", record)
		}
		if record.Sequence == "" || record.AuditID == "" || len(record.SafeMetadata) == 0 {
			t.Fatalf("incomplete export record: %#v", record)
		}
	}
	if second.Records[0].CorrelationID != "correlation-lifecycle-retry" {
		t.Fatalf("recorded correlation = %q", second.Records[0].CorrelationID)
	}
	fresh, err := repository.ExportOperatorAuditRecords(ctx, domainID, "", 10)
	if err != nil {
		t.Fatalf("export fresh snapshot: %v", err)
	}
	if !fresh.Complete || len(fresh.Records) != 3 {
		t.Fatalf("fresh export = %#v", fresh)
	}

	for name, candidate := range map[string]struct {
		domain string
		cursor string
		limit  int
	}{
		"invalid domain": {domain: "iso_invalid", limit: 1},
		"invalid cursor": {domain: domainID, cursor: "v1.invalid", limit: 1},
		"zero limit":     {domain: domainID, limit: 0},
		"large limit":    {domain: domainID, limit: 1001},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := repository.ExportOperatorAuditRecords(ctx, candidate.domain, candidate.cursor, candidate.limit); !errors.Is(err, persistence.ErrOperatorAuditExportInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestAuditedOperatorExportIsRepeatableAndAppendOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	domainID := "iso_00000000000000000001"
	insertOperatorAuditRecord(t, ctx, pool, domainID, "cor_00000000000000000001", "oidc-provider-credential.activate")
	reasonDigest := sha256.Sum256([]byte("incident export"))
	request := persistence.OperatorAuditExportRequest{
		ExportID:          "oax_00000000000000000001",
		IsolationDomainID: domainID,
		RequestedBy:       "operator@example.invalid",
		ReasonDigest:      reasonDigest[:],
		CorrelationID:     "cor_00000000000000000002",
		Limit:             10,
	}
	repository := persistence.NewRepository(pool)
	first, err := repository.ExportOperatorAuditRecordsAudited(ctx, request)
	if err != nil {
		t.Fatalf("export operator audit: %v", err)
	}
	replayed, err := repository.ExportOperatorAuditRecordsAudited(ctx, request)
	if err != nil {
		t.Fatalf("replay operator audit export: %v", err)
	}
	if string(first) != string(replayed) {
		t.Fatal("identical operator audit export changed bytes")
	}
	var document persistence.OperatorAuditExportDocument
	if err := json.Unmarshal(first, &document); err != nil {
		t.Fatalf("decode operator audit export: %v", err)
	}
	content, err := json.Marshal(document.Content)
	if err != nil {
		t.Fatalf("encode operator audit content: %v", err)
	}
	digest := sha256.Sum256(content)
	if document.ContentSHA256 != "sha256:"+hex.EncodeToString(digest[:]) ||
		!document.Content.Complete || len(document.Content.Records) != 1 {
		t.Fatalf("operator audit document = %#v", document)
	}
	changed := request
	changed.RequestedBy = "different-operator@example.invalid"
	if _, err := repository.ExportOperatorAuditRecordsAudited(ctx, changed); !errors.Is(err, persistence.ErrOperatorAuditExportConflict) {
		t.Fatalf("changed replay error = %v", err)
	}
	concurrentRequest := request
	concurrentRequest.ExportID = "oax_00000000000000000002"
	concurrentRequest.CorrelationID = "cor_00000000000000000003"
	start := make(chan struct{})
	results := make([][]byte, 2)
	errorsByCall := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results[index], errorsByCall[index] = repository.ExportOperatorAuditRecordsAudited(ctx, concurrentRequest)
		}()
	}
	close(start)
	wait.Wait()
	for index, callErr := range errorsByCall {
		if callErr != nil {
			t.Fatalf("concurrent export %d: %v", index, callErr)
		}
	}
	if string(results[0]) != string(results[1]) {
		t.Fatal("concurrent identical exports produced different bytes")
	}
	if _, err := pool.Exec(ctx, `UPDATE operator_audit_exports SET record_count = 0 WHERE export_id = $1`, request.ExportID); err == nil {
		t.Fatal("operator audit export receipt update was accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM operator_audit_exports WHERE export_id = $1`, request.ExportID); err == nil {
		t.Fatal("operator audit export receipt deletion was accepted")
	}
}

func resetOperatorAuditDatabase(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	databaseURL := testDatabaseURL(t)
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, clearProtectedAuditExportFixturesSQL); err != nil {
		database.Close()
		t.Fatalf("clear protected audit export fixtures: %v", err)
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
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cleanupDatabase, cleanupErr := persistence.OpenSQL(cleanupCtx, databaseURL)
		if cleanupErr != nil {
			t.Errorf("open protected audit export fixture cleanup: %v", cleanupErr)
			return
		}
		defer cleanupDatabase.Close()
		if _, cleanupErr := cleanupDatabase.ExecContext(cleanupCtx, clearProtectedAuditExportFixturesSQL); cleanupErr != nil {
			t.Errorf("clear protected audit export fixtures: %v", cleanupErr)
		}
	})
	return pool
}

const clearProtectedAuditExportFixturesSQL = `
	DO $$
	BEGIN
		IF to_regclass('audit_export_workload_identity_revocations') IS NOT NULL THEN
			EXECUTE 'TRUNCATE audit_export_workload_identity_revocations';
		END IF;
		IF to_regclass('audit_export_recipient_proof_revocations') IS NOT NULL THEN
			EXECUTE 'TRUNCATE audit_export_recipient_proof_revocations';
		END IF;
		IF to_regclass('audit_export_recipient_trust_events') IS NOT NULL THEN
			EXECUTE 'TRUNCATE audit_export_recipient_trust_events, audit_export_deliveries CASCADE';
		END IF;
	END;
	$$;
`

func insertOperatorAuditRecord(t *testing.T, ctx context.Context, pool *pgxpool.Pool, domainID, correlationID, action string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES ($1, $2, 'operator@example.invalid', $3, 'oidc-provider-credential', $4,
		          'succeeded', $5, $6, clock_timestamp())
	`, identity.New("aud"), domainID, action, identity.New("opc"), correlationID,
		[]byte(`{"endpoint":"jwks","generation":1,"providerId":"primary","reasonDigest":"sha256:1111111111111111111111111111111111111111111111111111111111111111"}`)); err != nil {
		t.Fatalf("insert operator audit record: %v", err)
	}
}
