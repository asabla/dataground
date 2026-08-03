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
	if err := persistence.MigrateDownTo(ctx, database, 20); err != nil {
		t.Fatalf("migrate to schema 20: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO audit_records (
			id, isolation_domain_id, actor_id, action, resource_type, resource_id,
			outcome, correlation_id, safe_metadata, occurred_at
		) VALUES (
			'aud_00000000000000000001', 'iso_00000000000000000001', 'operator',
			'test.audit', 'test-resource', 'tst_00000000000000000001', 'accepted',
			'cor_00000000000000000001', '{}', clock_timestamp()
		)
	`); err != nil {
		t.Fatalf("seed schema 20 audit record: %v", err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		t.Fatalf("upgrade schema 20 audit records: %v", err)
	}
	var auditSequence int64
	if err := database.QueryRowContext(ctx, `
		SELECT sequence FROM audit_records WHERE id = 'aud_00000000000000000001'
	`).Scan(&auditSequence); err != nil {
		t.Fatalf("read backfilled audit sequence: %v", err)
	}
	if auditSequence < 1 {
		t.Fatalf("backfilled audit sequence = %d", auditSequence)
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
		      'invocation_artifact_objects',
		      'api_authorization_decisions',
		      'invocation_authorization_policies',
		      'invocation_authorization_decisions',
		      'authorization_audit_exports',
		      'oidc_identity_bindings',
		      'oidc_identity_revocations',
		      'authentication_attempts',
		      'oidc_dpop_replays',
		      'oidc_dpop_nonces',
		      'authentication_rate_limit_buckets',
		      'authentication_rate_limit_policy_activations',
		      'oidc_provider_credential_operations',
		      'operator_audit_exports'
		  )
	`).Scan(&tables); err != nil {
		t.Fatalf("inspect migrated tables: %v", err)
	}
	if tables != 24 {
		t.Fatalf("expected 24 representative tables, got %d", tables)
	}

	var rateLimitBucketPrimaryKey string
	if err := database.QueryRowContext(ctx, `
		SELECT string_agg(attribute.attname, ',' ORDER BY key.ordinality)
		FROM pg_constraint AS policy_constraint
		CROSS JOIN LATERAL unnest(policy_constraint.conkey)
		    WITH ORDINALITY AS key(attribute_number, ordinality)
		JOIN pg_attribute AS attribute
		  ON attribute.attrelid = policy_constraint.conrelid
		 AND attribute.attnum = key.attribute_number
		WHERE policy_constraint.conrelid = 'authentication_rate_limit_buckets'::regclass
		  AND policy_constraint.contype = 'p'
	`).Scan(&rateLimitBucketPrimaryKey); err != nil {
		t.Fatalf("inspect authentication rate limit bucket identity: %v", err)
	}
	if rateLimitBucketPrimaryKey != "policy_generation,scope,subject_digest" {
		t.Fatalf("authentication rate limit bucket identity = %q", rateLimitBucketPrimaryKey)
	}
}
