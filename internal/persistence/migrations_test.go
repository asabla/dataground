package persistence_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func TestMigrationsRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("DATAGROUND_REQUIRE_TEST_DATABASE") == "true" {
			t.Fatal("DATAGROUND_TEST_DATABASE_URL is required")
		}
		t.Skip("DATAGROUND_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	if err := persistence.MigrateUp(ctx, database); err != nil {
		t.Fatalf("initial migrate up: %v", err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 0); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	if err := persistence.RequireCurrentSchema(ctx, database); err != nil {
		t.Fatalf("require current schema: %v", err)
	}

	var tables int
	if err := database.QueryRowContext(ctx, `
		SELECT count(*)
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		  AND table_name IN (
		      'agent_services',
		      'service_publication_operations',
		      'invocation_execution_operations',
		      'outbox_events',
		      'audit_records',
		      'execution_gateways',
		      'execution_placements',
		      'execution_instances',
		      'service_revision_execution_plans',
		      'service_revision_enforcement_bundles',
		      'invocation_artifact_objects'
		  )
	`).Scan(&tables); err != nil {
		t.Fatalf("inspect migrated tables: %v", err)
	}
	if tables != 12 {
		t.Fatalf("expected 12 representative tables, got %d", tables)
	}
}
