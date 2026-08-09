package persistence_test

import (
	"bytes"
	"context"
	"os"
	"strings"
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
	if err := persistence.MigrateDownTo(ctx, database, 22); err != nil {
		t.Fatalf("migrate to schema 22: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO audit_export_deliveries (
			delivery_id, isolation_domain_id, contract, export_kind, export_id,
			envelope_digest, export_sha256, trust_profile_sha256, signing_key_id,
			recipient_id, destination_digest
		) VALUES
			('adl_00000000000000000001', 'iso_00000000000000000001',
			 'dataground.audit-export-delivery/v1', 'operator', 'oax_00000000000000000001',
			 decode(repeat('11', 32), 'hex'), 'sha256:' || repeat('2', 64),
			 'sha256:' || repeat('3', 64), 'audit_key_01', 'archive.primary',
			 decode(repeat('44', 32), 'hex')),
			('adl_00000000000000000002', 'iso_00000000000000000001',
			 'dataground.audit-export-delivery/v1', 'operator', 'oax_00000000000000000002',
			 decode(repeat('55', 32), 'hex'), 'sha256:' || repeat('6', 64),
			 'sha256:' || repeat('7', 64), 'audit_key_01', 'archive.primary',
			 decode(repeat('88', 32), 'hex'));
		INSERT INTO audit_export_delivery_operations (
			delivery_id, operation, isolation_domain_id, actor_id, correlation_id,
			reason_digest, evidence_digest
		) VALUES
			('adl_00000000000000000001', 'prepare', 'iso_00000000000000000001', 'operator',
			 'cor_00000000000000000011', decode(repeat('99', 32), 'hex'), decode(repeat('11', 32), 'hex')),
			('adl_00000000000000000002', 'prepare', 'iso_00000000000000000001', 'operator',
			 'cor_00000000000000000012', decode(repeat('aa', 32), 'hex'), decode(repeat('55', 32), 'hex')),
			('adl_00000000000000000002', 'acknowledge', 'iso_00000000000000000001', 'operator',
			 'cor_00000000000000000013', decode(repeat('bb', 32), 'hex'), decode(repeat('cc', 32), 'hex'));
		UPDATE audit_export_deliveries
		SET status = 'acknowledged', acknowledgement_digest = decode(repeat('cc', 32), 'hex'),
		    acknowledged_at = clock_timestamp()
		WHERE delivery_id = 'adl_00000000000000000002';
	`); err != nil {
		t.Fatalf("seed schema 22 deliveries: %v", err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		t.Fatalf("upgrade schema 22 deliveries: %v", err)
	}
	var preparedContract, acknowledgedContract string
	var acknowledgedVerificationFields int
	if err := database.QueryRowContext(ctx, `
		SELECT
			(SELECT contract FROM audit_export_deliveries WHERE delivery_id = 'adl_00000000000000000001'),
			(SELECT contract FROM audit_export_deliveries WHERE delivery_id = 'adl_00000000000000000002'),
			(SELECT num_nonnulls(acknowledgement_contract, recipient_trust_profile_sha256,
			                     recipient_signing_key_id, recipient_accepted_at,
			                     recipient_trust_generation)
			 FROM audit_export_deliveries WHERE delivery_id = 'adl_00000000000000000002')
	`).Scan(&preparedContract, &acknowledgedContract, &acknowledgedVerificationFields); err != nil {
		t.Fatalf("inspect schema 24 delivery upgrade: %v", err)
	}
	if preparedContract != "dataground.audit-export-delivery/v3" ||
		acknowledgedContract != "dataground.audit-export-delivery/v1" || acknowledgedVerificationFields != 0 {
		t.Fatalf("upgraded contracts = %q, %q; legacy verification fields = %d",
			preparedContract, acknowledgedContract, acknowledgedVerificationFields)
	}
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open schema 23 replay pool: %v", err)
	}
	legacyReplay := persistence.AuditExportDelivery{
		Contract: persistence.AuditExportDeliveryContract, DeliveryID: "adl_00000000000000000002",
		IsolationDomainID: "iso_00000000000000000001", ExportKind: "operator",
		ExportID: "oax_00000000000000000002", EnvelopeDigest: bytes.Repeat([]byte{0x55}, 32),
		ExportSHA256:       "sha256:" + string(bytes.Repeat([]byte{'6'}, 64)),
		TrustProfileSHA256: "sha256:" + string(bytes.Repeat([]byte{'7'}, 64)),
		SigningKeyID:       "audit_key_01", RecipientID: "archive.primary",
		DestinationDigest: bytes.Repeat([]byte{0x88}, 32),
	}
	legacyAttribution := persistence.AuditExportDeliveryAttribution{
		ActorID: "operator", ReasonDigest: bytes.Repeat([]byte{0xaa}, 32),
		CorrelationID: "cor_00000000000000000012",
	}
	if err := persistence.NewRepository(pool).PrepareAuditExportDelivery(ctx, legacyReplay, legacyAttribution); err != nil {
		pool.Close()
		t.Fatalf("replay completed legacy delivery: %v", err)
	}
	pool.Close()
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
		      'invocation_authorization_policy_withdrawals',
		      'invocation_authorization_entity_generations',
		      'invocation_authorization_entity_activations',
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
		      'operator_audit_exports',
		      'audit_export_deliveries',
		      'audit_export_delivery_operations',
		      'audit_export_workload_identity_revocations',
		      'audit_export_recipient_proof_revocations',
		      'audit_export_revocation_acquisitions',
		      'audit_export_revocation_source_events',
		      'audit_export_recipient_trust_events',
		      'audit_export_recipient_trust_keys',
		      'audit_export_recipient_encryption_keys',
		      'audit_export_proofing_authority_events',
		      'audit_export_proofing_authority_keys',
		      'audit_export_revocation_authority_events',
		      'audit_export_revocation_authority_keys',
		      'provider_credential_grant_events',
		      'provider_credential_authorization_decisions'
		  )
	`).Scan(&tables); err != nil {
		t.Fatalf("inspect migrated tables: %v", err)
	}
	if tables != 42 {
		t.Fatalf("expected 42 representative tables, got %d", tables)
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

func TestRecipientIdentityMigrationPermitsProofUpgradeWithoutKeyRotation(t *testing.T) {
	databaseURL := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("DATAGROUND_REQUIRE_TEST_DATABASE") == "true" {
			t.Fatal("DATAGROUND_TEST_DATABASE_URL is required")
		}
		t.Skip("DATAGROUND_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	pool.Close()
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 24); err != nil {
		t.Fatalf("migrate to legacy recipient trust schema: %v", err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO audit_export_recipient_trust_events (
			isolation_domain_id, recipient_id, generation, operation,
			trust_contract, trust_profile_sha256, actor_id, reason_digest, correlation_id
		) VALUES (
			'iso_00000000000000000001', 'archive.primary', 1, 'activate',
			'dataground.audit-export-recipient-trust/ed25519/v1',
			'sha256:' || repeat('3', 64), 'operator', decode(repeat('4', 64), 'hex'),
			'cor_00000000000000000001'
		);
		INSERT INTO audit_export_recipient_trust_keys (
			isolation_domain_id, recipient_id, generation, key_id
		) VALUES (
			'iso_00000000000000000001', 'archive.primary', 1, 'archive_key_01'
		)
	`); err != nil {
		transaction.Rollback()
		t.Fatalf("seed legacy recipient trust: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit legacy recipient trust: %v", err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		t.Fatalf("upgrade recipient trust schema: %v", err)
	}
	proofPool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer proofPool.Close()
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	activateAuditExportProofingAuthority(
		t, ctx, persistence.NewRepository(proofPool), delivery.IsolationDomainID,
	)
	upgrade := auditExportRecipientTrustChange(
		delivery,
		"activate",
		2,
		"sha256:"+string(bytes.Repeat([]byte{'3'}, 64)),
		"cor_00000000000000000002",
	)
	if err := persistence.NewRepository(proofPool).ChangeAuditExportRecipientTrust(ctx, upgrade); err != nil {
		t.Fatalf("append identity-proven trust upgrade: %v", err)
	}
	var contracts string
	if err := proofPool.QueryRow(ctx, `
		SELECT string_agg(authorization_contract, ',' ORDER BY generation)
		FROM audit_export_recipient_trust_events
		WHERE isolation_domain_id = $1 AND recipient_id = $2
	`, delivery.IsolationDomainID, delivery.RecipientID).Scan(&contracts); err != nil {
		t.Fatal(err)
	}
	if contracts != "dataground.audit-export-recipient-trust-authorization/v1,"+
		"dataground.audit-export-recipient-trust-authorization/v3" {
		t.Fatalf("recipient trust authorization contracts = %q", contracts)
	}
}

func TestRecipientProofRevocationMigrationPreservesEvidence(t *testing.T) {
	databaseURL := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("DATAGROUND_REQUIRE_TEST_DATABASE") == "true" {
			t.Fatal("DATAGROUND_TEST_DATABASE_URL is required")
		}
		t.Skip("DATAGROUND_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	record := auditExportRecipientProofRevocationRecord(
		"iso_00000000000000000001", "profile", "cor_00000000000000000020",
	)
	repository := persistence.NewRepository(pool)
	activateAuditExportRevocationAuthority(
		t, ctx, repository, record.IsolationDomainID,
		persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
		record.RevocationAuthorityID, record.RevocationTrustProfileSHA256,
		record.RevocationSigningKeyID, "cor_00000000000000000021",
	)
	if err := repository.RecordAuditExportRecipientProofRevocation(ctx, record); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	pool.Close()
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, `
		TRUNCATE audit_export_proofing_authority_keys,
		         audit_export_proofing_authority_events,
		         audit_export_revocation_authority_keys,
		         audit_export_revocation_authority_events
	`); err != nil {
		t.Fatalf("clear revocation authority migration fixture: %v", err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 25); err == nil {
		t.Fatal("recipient proof revocation evidence was discarded by schema downgrade")
	}
	if err := persistence.RequireCurrentSchema(ctx, database); err != nil {
		t.Fatalf("failed downgrade changed current schema: %v", err)
	}
}

func TestProofingAuthorityMigrationPreservesEvidence(t *testing.T) {
	databaseURL := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("DATAGROUND_REQUIRE_TEST_DATABASE") == "true" {
			t.Fatal("DATAGROUND_TEST_DATABASE_URL is required")
		}
		t.Skip("DATAGROUND_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	repository := persistence.NewRepository(pool)
	activateAuditExportProofingAuthority(
		t, ctx, repository, "iso_00000000000000000001",
	)
	pool.Close()
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 32); err == nil {
		t.Fatal("proofing authority evidence was discarded by schema downgrade")
	}
	if err := persistence.RequireCurrentSchema(ctx, database); err != nil {
		t.Fatalf("failed downgrade changed current schema: %v", err)
	}
}

func TestRevocationAcquisitionMigrationPreservesEvidence(t *testing.T) {
	databaseURL := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("DATAGROUND_REQUIRE_TEST_DATABASE") == "true" {
			t.Fatal("DATAGROUND_TEST_DATABASE_URL is required")
		}
		t.Skip("DATAGROUND_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	repository := persistence.NewRepository(pool)
	record := auditExportRecipientProofRevocationRecord(
		"iso_00000000000000000001", "profile", "cor_00000000000000000020",
	)
	record.Acquisition = &persistence.AuditExportRevocationAcquisition{
		Contract:                   persistence.AuditExportRevocationAcquisitionContract,
		Purpose:                    persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
		SourceID:                   "archive-revocations.primary",
		SourceRegistrySHA256:       "sha256:" + strings.Repeat("f", 64),
		SourceGeneration:           1,
		NoticeCredentialSHA256:     "sha256:" + strings.Repeat("d", 64),
		NoticeCredentialGeneration: 1,
		TrustCredentialSHA256:      "sha256:" + strings.Repeat("e", 64),
		TrustCredentialGeneration:  1,
	}
	activateAuditExportRevocationAuthority(
		t, ctx, repository, record.IsolationDomainID,
		persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
		record.RevocationAuthorityID, record.RevocationTrustProfileSHA256,
		record.RevocationSigningKeyID, "cor_00000000000000000021",
	)
	activateAuditExportRevocationSource(
		t, ctx, repository, record.IsolationDomainID, record.Acquisition.Purpose,
		record.Acquisition.SourceID, record.Acquisition.SourceRegistrySHA256,
		1, "cor_00000000000000000022",
	)
	activateAuditExportRevocationCredentials(
		t, ctx, repository, record.IsolationDomainID, record.Acquisition,
		"cor_00000000000000000085", "cor_00000000000000000086",
	)
	if err := repository.RecordAuditExportRecipientProofRevocation(ctx, record); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	pool.Close()
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 33); err == nil {
		t.Fatal("revocation acquisition evidence was discarded by schema downgrade")
	}
	if err := persistence.RequireCurrentSchema(ctx, database); err != nil {
		t.Fatalf("failed downgrade changed current schema: %v", err)
	}
}

func TestRevocationSourceMigrationPreservesEvidence(t *testing.T) {
	databaseURL := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("DATAGROUND_REQUIRE_TEST_DATABASE") == "true" {
			t.Fatal("DATAGROUND_TEST_DATABASE_URL is required")
		}
		t.Skip("DATAGROUND_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	repository := persistence.NewRepository(pool)
	activateAuditExportRevocationSource(
		t, ctx, repository, "iso_00000000000000000001",
		persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
		"archive-revocations.primary", "sha256:"+strings.Repeat("f", 64),
		1, "cor_00000000000000000023",
	)
	pool.Close()
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 34); err == nil {
		t.Fatal("revocation source evidence was discarded by schema downgrade")
	}
	if err := persistence.RequireCurrentSchema(ctx, database); err != nil {
		t.Fatalf("failed downgrade changed current schema: %v", err)
	}
}

func TestLegacyRevocationAcquisitionReplaySurvivesGovernanceUpgrade(t *testing.T) {
	databaseURL := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("DATAGROUND_REQUIRE_TEST_DATABASE") == "true" {
			t.Fatal("DATAGROUND_TEST_DATABASE_URL is required")
		}
		t.Skip("DATAGROUND_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	record := auditExportRecipientProofRevocationRecord(
		"iso_00000000000000000001", "profile", "cor_00000000000000000024",
	)
	repository := persistence.NewRepository(pool)
	activateAuditExportRevocationAuthority(
		t, ctx, repository, record.IsolationDomainID,
		persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
		record.RevocationAuthorityID, record.RevocationTrustProfileSHA256,
		record.RevocationSigningKeyID, "cor_00000000000000000025",
	)
	if err := repository.RecordAuditExportRecipientProofRevocation(ctx, record); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	pool.Close()
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 34); err != nil {
		t.Fatalf("migrate to legacy acquisition schema: %v", err)
	}
	registryDigest := "sha256:" + strings.Repeat("f", 64)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO audit_export_revocation_acquisitions (
			contract, purpose, revocation_sha256, isolation_domain_id,
			source_id, source_registry_sha256, trust_profile_sha256, correlation_id
		) VALUES (
			'dataground.audit-export-revocation-acquisition/v1', 'recipient-proof',
			$1, $2, 'archive-revocations.primary', $3, $4, $5
		)
	`, record.RevocationSHA256, record.IsolationDomainID, registryDigest,
		record.RevocationTrustProfileSHA256, record.CorrelationID); err != nil {
		t.Fatalf("insert legacy acquisition receipt: %v", err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		t.Fatalf("upgrade legacy acquisition receipt: %v", err)
	}
	replayPool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer replayPool.Close()
	replayed, err := persistence.NewRepository(replayPool).ReplayAuditExportRevocationAcquisition(
		ctx, persistence.AuditExportRevocationAcquisitionReplay{
			Purpose:           persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
			IsolationDomainID: record.IsolationDomainID, SourceID: "archive-revocations.primary",
			SourceRegistrySHA256: registryDigest, ActorID: record.ActorID,
			ReasonDigest: record.ReasonDigest, CorrelationID: record.CorrelationID,
		},
	)
	if err != nil || !replayed {
		t.Fatalf("legacy acquisition replay = %v, %v", replayed, err)
	}
}
