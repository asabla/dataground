package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func TestAuditedAuthorizationExportIsRepeatableAndAppendOnly(t *testing.T) {
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
	domainID := "iso_00000000000000000001"
	if err := repository.RecordAuthorizationDecision(
		ctx,
		exportAPIDecision(domainID, "cor_00000000000000000001"),
	); err != nil {
		t.Fatalf("record API decision: %v", err)
	}
	if err := repository.RecordInvocationAuthorizationDecision(
		ctx,
		exportInvocationDecision(domainID, "cor_00000000000000000002"),
	); err != nil {
		t.Fatalf("record invocation decision: %v", err)
	}
	reasonDigest := sha256.Sum256([]byte("incident export"))
	request := persistence.AuthorizationAuditExportRequest{
		ExportID:          "aex_00000000000000000001",
		IsolationDomainID: domainID,
		RequestedBy:       "operator@example.invalid",
		ReasonDigest:      reasonDigest[:],
		CorrelationID:     "cor_00000000000000000003",
		Limit:             1,
	}
	firstBytes, err := repository.ExportAuthorizationDecisionsAudited(ctx, request)
	if err != nil {
		t.Fatalf("export first page: %v", err)
	}
	var first persistence.AuthorizationAuditExportDocument
	if err := json.Unmarshal(firstBytes, &first); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	assertAuthorizationAuditExportDigest(t, first)
	if first.Content.Complete ||
		len(first.Content.Records) != 1 ||
		first.Content.Records[0].Source != "api" ||
		first.Content.NextCursor == "" {
		t.Fatalf("first page = %#v", first.Content)
	}
	replayed, err := repository.ExportAuthorizationDecisionsAudited(ctx, request)
	if err != nil {
		t.Fatalf("replay first page: %v", err)
	}
	if string(replayed) != string(firstBytes) {
		t.Fatal("identical export replay changed bytes")
	}
	changedRequest := request
	changedRequest.RequestedBy = "different-operator@example.invalid"
	if _, err := repository.ExportAuthorizationDecisionsAudited(ctx, changedRequest); !errors.Is(
		err,
		persistence.ErrAuthorizationExportConflict,
	) {
		t.Fatalf("changed replay error = %v, want conflict", err)
	}

	if err := repository.RecordAuthorizationDecision(
		ctx,
		exportAPIDecision(domainID, "cor_00000000000000000004"),
	); err != nil {
		t.Fatalf("record late API decision: %v", err)
	}
	secondRequest := request
	secondRequest.ExportID = "aex_00000000000000000002"
	secondRequest.CorrelationID = "cor_00000000000000000005"
	secondRequest.Cursor = first.Content.NextCursor
	secondBytes, err := repository.ExportAuthorizationDecisionsAudited(ctx, secondRequest)
	if err != nil {
		t.Fatalf("export second page: %v", err)
	}
	var second persistence.AuthorizationAuditExportDocument
	if err := json.Unmarshal(secondBytes, &second); err != nil {
		t.Fatalf("decode second page: %v", err)
	}
	assertAuthorizationAuditExportDigest(t, second)
	if !second.Content.Complete ||
		len(second.Content.Records) != 1 ||
		second.Content.Records[0].Source != "invocation" {
		t.Fatalf("second page = %#v", second.Content)
	}
	for _, record := range second.Content.Records {
		if record.CorrelationID == "cor_00000000000000000004" {
			t.Fatal("frozen export admitted a later decision")
		}
	}

	var receipts int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM authorization_audit_exports
		WHERE isolation_domain_id = $1
	`, domainID).Scan(&receipts); err != nil {
		t.Fatalf("count export receipts: %v", err)
	}
	if receipts != 2 {
		t.Fatalf("receipt count = %d, want 2", receipts)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE authorization_audit_exports
		SET record_count = 0
		WHERE export_id = $1
	`, request.ExportID); err == nil {
		t.Fatal("export receipt update was accepted")
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM authorization_audit_exports
		WHERE export_id = $1
	`, request.ExportID); err == nil {
		t.Fatal("export receipt deletion was accepted")
	}
}

func assertAuthorizationAuditExportDigest(
	t *testing.T,
	document persistence.AuthorizationAuditExportDocument,
) {
	t.Helper()
	content, err := json.Marshal(document.Content)
	if err != nil {
		t.Fatalf("encode export content: %v", err)
	}
	digest := sha256.Sum256(content)
	want := "sha256:" + hex.EncodeToString(digest[:])
	if document.ContentSHA256 != want {
		t.Fatalf("content digest = %q, want %q", document.ContentSHA256, want)
	}
}
