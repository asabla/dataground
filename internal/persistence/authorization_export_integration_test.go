package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/persistence"
)

func TestAuthorizationAuditExportFreezesScopeAndAdvancesBothSources(t *testing.T) {
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
	otherDomainID := "iso_00000000000000000002"
	apiCorrelation := "cor_00000000000000000001"
	invocationCorrelation := "cor_00000000000000000002"
	lateCorrelation := "cor_00000000000000000003"

	if err := repository.RecordAuthorizationDecision(ctx, exportAPIDecision(domainID, apiCorrelation)); err != nil {
		t.Fatalf("record API decision: %v", err)
	}
	if err := repository.RecordInvocationAuthorizationDecision(
		ctx,
		exportInvocationDecision(domainID, invocationCorrelation),
	); err != nil {
		t.Fatalf("record invocation decision: %v", err)
	}
	if err := repository.RecordAuthorizationDecision(
		ctx,
		exportAPIDecision(otherDomainID, "cor_00000000000000000004"),
	); err != nil {
		t.Fatalf("record other-domain decision: %v", err)
	}

	first, err := repository.ExportAuthorizationDecisions(ctx, domainID, "", 1)
	if err != nil {
		t.Fatalf("export first page: %v", err)
	}
	if first.SchemaVersion != persistence.AuthorizationAuditExportSchema ||
		first.IsolationDomainID != domainID ||
		first.Cursor != "" ||
		first.NextCursor == "" ||
		first.Complete ||
		len(first.Records) != 1 {
		t.Fatalf("first page = %#v", first)
	}

	if err := repository.RecordAuthorizationDecision(
		ctx,
		exportAPIDecision(domainID, lateCorrelation),
	); err != nil {
		t.Fatalf("record late decision: %v", err)
	}
	second, err := repository.ExportAuthorizationDecisions(ctx, domainID, first.NextCursor, 10)
	if err != nil {
		t.Fatalf("export second page: %v", err)
	}
	if !second.Complete || second.Cursor != first.NextCursor || len(second.Records) != 1 {
		t.Fatalf("second page = %#v", second)
	}
	records := append(append([]persistence.AuthorizationAuditExportRecord{}, first.Records...), second.Records...)
	if len(records) != 2 {
		t.Fatalf("frozen export record count = %d, want 2", len(records))
	}
	sources := map[string]bool{}
	for _, record := range records {
		sources[record.Source] = true
		if record.CorrelationID == lateCorrelation {
			t.Fatal("frozen export admitted a later decision")
		}
	}
	if !sources["api"] || !sources["invocation"] {
		t.Fatalf("export sources = %#v", sources)
	}

	fresh, err := repository.ExportAuthorizationDecisions(ctx, domainID, "", 10)
	if err != nil {
		t.Fatalf("export fresh snapshot: %v", err)
	}
	if !fresh.Complete || len(fresh.Records) != 3 {
		t.Fatalf("fresh export = %#v", fresh)
	}
	for _, record := range fresh.Records {
		if record.CorrelationID == "cor_00000000000000000004" {
			t.Fatal("export crossed isolation-domain scope")
		}
	}
}

func TestAuthorizationAuditExportRejectsInvalidRequestsAndSerializesClosedRecords(t *testing.T) {
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
			if _, err := repository.ExportAuthorizationDecisions(
				ctx,
				candidate.domain,
				candidate.cursor,
				candidate.limit,
			); !errors.Is(err, persistence.ErrAuthorizationExportInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}

	export, err := repository.ExportAuthorizationDecisions(ctx, domainID, "", 10)
	if err != nil {
		t.Fatalf("export records: %v", err)
	}
	encoded, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("encode export: %v", err)
	}
	text := string(encoded)
	for _, forbidden := range []string{
		"prompt",
		"policyBytes",
		"schemaBytes",
		"provider",
		"diagnostic",
		"reason",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("export contains forbidden field %q: %s", forbidden, text)
		}
	}
	for _, record := range export.Records {
		switch record.Source {
		case "api":
			if record.ActorID != "" ||
				record.OperationID != "" ||
				record.InvocationID != "" ||
				record.ServiceID != "" ||
				record.RevisionID != "" {
				t.Fatalf("API export record widened: %#v", record)
			}
		case "invocation":
			if record.PrincipalID != "" ||
				record.PrincipalKind != "" ||
				record.ResourceType != "" ||
				record.ResourceID != "" {
				t.Fatalf("invocation export record widened: %#v", record)
			}
		default:
			t.Fatalf("unknown export source %q", record.Source)
		}
	}
}

func exportAPIDecision(domainID string, correlationID string) authz.DecisionRecord {
	return authz.DecisionRecord{
		PrincipalID:       "usr_00000000000000000001",
		PrincipalKind:     authn.PrincipalHuman,
		IsolationDomainID: domainID,
		Action:            authz.ReadInvocation,
		ResourceType:      authz.Invocation,
		ResourceID:        "inv_00000000000000000001",
		Outcome:           authz.OutcomeAllowed,
		PolicySetID:       "policy.api.integration",
		PolicyDigest:      "sha256:" + strings.Repeat("1", 64),
		CorrelationID:     correlationID,
	}
}

func exportInvocationDecision(domainID string, correlationID string) authz.InvocationDecisionRecord {
	return authz.InvocationDecisionRecord{
		ActorID:           "operator@example.invalid",
		IsolationDomainID: domainID,
		OperationID:       "op_00000000000000000001",
		InvocationID:      "inv_00000000000000000001",
		ServiceID:         "svc_00000000000000000001",
		RevisionID:        "rev_00000000000000000001",
		Action:            authz.InvocationRun,
		Outcome:           authz.OutcomeAllowed,
		PolicySetID:       "policy.invocation.integration",
		PolicyDigest:      "sha256:" + strings.Repeat("2", 64),
		CorrelationID:     correlationID,
	}
}
