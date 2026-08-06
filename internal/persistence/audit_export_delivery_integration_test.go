package persistence_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func TestAuditExportDeliveryAcknowledgementIsScopedReplayableAndAudited(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	activateAuditExportRecipientTrust(t, ctx, repository, delivery, 1, "sha256:"+strings.Repeat("3", 64),
		"cor_00000000000000000009")
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_deliveries (
			delivery_id, isolation_domain_id, contract, export_kind, export_id,
			envelope_digest, export_sha256, trust_profile_sha256, signing_key_id,
			recipient_id, destination_digest, encrypted_package_digest,
			recipient_trust_profile_sha256, recipient_encryption_key_id,
			recipient_trust_generation
		) VALUES (
			'adl_00000000000000000099', $1, 'dataground.audit-export-delivery/v4',
			'operator', 'oax_00000000000000000001', decode(repeat('11', 32), 'hex'),
			'sha256:' || repeat('1', 64), 'sha256:' || repeat('2', 64), 'audit_key_01',
			'archive.primary', decode(repeat('22', 32), 'hex'), decode(repeat('33', 32), 'hex'),
			'sha256:' || repeat('3', 64), 'archive_encryption_key_02', 1
		)
	`, delivery.IsolationDomainID); err == nil {
		t.Fatal("direct delivery preparation bypassed recipient encryption authorization")
	}
	prepare := auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001")
	if err := repository.PrepareAuditExportDelivery(ctx, delivery, prepare); err != nil {
		t.Fatalf("prepare delivery: %v", err)
	}
	if err := repository.PrepareAuditExportDelivery(ctx, delivery, prepare); err != nil {
		t.Fatalf("replay preparation: %v", err)
	}
	completeAuditExportDeliveryTransport(t, ctx, repository, delivery)

	changed := delivery
	changed.DestinationDigest = append([]byte(nil), delivery.DestinationDigest...)
	changed.DestinationDigest[0] ^= 0xff
	if err := repository.PrepareAuditExportDelivery(ctx, changed, prepare); !errors.Is(err, persistence.ErrAuditExportDeliveryConflict) {
		t.Fatalf("changed preparation error = %v", err)
	}
	other := auditExportDeliveryFixture("adl_00000000000000000002")
	if err := repository.PrepareAuditExportDelivery(ctx, other, prepare); !errors.Is(err, persistence.ErrAuditExportDeliveryConflict) {
		t.Fatalf("correlation reuse error = %v", err)
	}

	acknowledgementDigest := sha256.Sum256([]byte("archive receipt"))
	acknowledgement := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       acknowledgementDigest[:],
		DeliveryContract:            persistence.AuditExportWorkloadDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v5",
		RecipientTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		RecipientSigningKeyID:       "archive_key_01",
		RecipientTrustGeneration:    1,
		AcceptedAt:                  time.Date(2026, 8, 3, 15, 30, 0, 123000, time.UTC),
		Attribution: auditExportDeliveryAttribution(
			"record archive receipt",
			"cor_00000000000000000002",
		),
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement); err != nil {
		t.Fatalf("acknowledge delivery: %v", err)
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement); err != nil {
		t.Fatalf("replay acknowledgement: %v", err)
	}
	changedAcknowledgement := acknowledgement
	changedAcknowledgement.AcknowledgementDigest = append([]byte(nil), acknowledgement.AcknowledgementDigest...)
	changedAcknowledgement.AcknowledgementDigest[0] ^= 0xff
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, changedAcknowledgement); !errors.Is(err, persistence.ErrAuditExportDeliveryConflict) {
		t.Fatalf("changed acknowledgement error = %v", err)
	}
	crossDomain := delivery
	crossDomain.IsolationDomainID = "iso_00000000000000000002"
	if err := repository.AcknowledgeAuditExportDelivery(ctx, crossDomain, acknowledgement); !errors.Is(err, persistence.ErrAuditExportDeliveryConflict) {
		t.Fatalf("cross-domain acknowledgement error = %v", err)
	}

	var status, receiptContract, recipientTrustProfileSHA256, recipientSigningKeyID string
	var recipientAcceptedAt time.Time
	var recipientTrustGeneration int64
	var operationCount, auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT status, acknowledgement_contract, recipient_trust_profile_sha256,
		       recipient_signing_key_id, recipient_accepted_at, recipient_trust_generation,
		       (SELECT count(*) FROM audit_export_delivery_operations WHERE delivery_id = $1),
		       (SELECT count(*) FROM audit_records WHERE resource_type = 'audit-export-delivery' AND resource_id = $1)
		FROM audit_export_deliveries WHERE delivery_id = $1
	`, delivery.DeliveryID).Scan(
		&status, &receiptContract, &recipientTrustProfileSHA256, &recipientSigningKeyID,
		&recipientAcceptedAt, &recipientTrustGeneration, &operationCount, &auditCount,
	); err != nil {
		t.Fatalf("inspect delivery: %v", err)
	}
	if status != "acknowledged" || receiptContract != acknowledgement.ReceiptContract ||
		recipientTrustProfileSHA256 != acknowledgement.RecipientTrustProfileSHA256 ||
		recipientSigningKeyID != acknowledgement.RecipientSigningKeyID ||
		!recipientAcceptedAt.Equal(acknowledgement.AcceptedAt) || recipientTrustGeneration != 1 ||
		operationCount != 3 || auditCount != 4 {
		t.Fatalf("delivery state = %q, %q, %q, %q, %v, trust generation %d; operations = %d, audits = %d",
			status, receiptContract, recipientTrustProfileSHA256, recipientSigningKeyID,
			recipientAcceptedAt, recipientTrustGeneration, operationCount, auditCount)
	}

	exported, err := repository.ExportOperatorAuditRecords(ctx, delivery.IsolationDomainID, "", 10)
	if err != nil {
		t.Fatalf("export delivery audit: %v", err)
	}
	if !exported.Complete || len(exported.Records) != 6 ||
		exported.Records[0].Action != "audit-export-recipient-trust.activate" ||
		exported.Records[1].Action != "audit-export-workload-identity.activate" ||
		exported.Records[2].Action != "audit-export-delivery.prepare" ||
		exported.Records[3].Action != "audit-export-delivery.transport" ||
		exported.Records[4].Action != "audit-export-delivery.transport-complete" ||
		exported.Records[5].Action != "audit-export-delivery.acknowledge" {
		t.Fatalf("delivery audit export = %#v", exported)
	}
	revocation := auditExportRecipientTrustChange(delivery, "revoke", 2,
		acknowledgement.RecipientTrustProfileSHA256, "cor_00000000000000000010")
	if err := repository.ChangeAuditExportRecipientTrust(ctx, revocation); err != nil {
		t.Fatalf("revoke acknowledged recipient trust: %v", err)
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement); err != nil {
		t.Fatalf("replay acknowledgement after revocation: %v", err)
	}
}

func TestAuditExportDeliveryTablesRejectMutation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	activateAuditExportRecipientTrust(t, ctx, repository, delivery, 1, "sha256:"+strings.Repeat("3", 64),
		"cor_00000000000000000009")
	prepare := auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001")
	if err := repository.PrepareAuditExportDelivery(ctx, delivery, prepare); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_delivery_transports (
			delivery_id, isolation_domain_id, transport_contract,
			destination_digest, encrypted_package_digest
		) VALUES ($1, $2, 'dataground.audit-export-transport/s3-immutable/v1', $3, $4)
	`, delivery.DeliveryID, delivery.IsolationDomainID, delivery.DestinationDigest,
		delivery.EncryptedPackageDigest); err == nil {
		t.Fatal("direct transport reservation without an append-only operation was accepted")
	}
	completeAuditExportDeliveryTransport(t, ctx, repository, delivery)
	if _, err := pool.Exec(ctx, `UPDATE audit_export_deliveries SET recipient_id = 'archive.changed' WHERE delivery_id = $1`, delivery.DeliveryID); err == nil {
		t.Fatal("delivery identity mutation was accepted")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE audit_export_deliveries
		SET status = 'acknowledged', acknowledgement_digest = decode(repeat('11', 32), 'hex'),
		    acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v2',
		    recipient_trust_profile_sha256 = 'sha256:' || repeat('2', 64),
		    recipient_signing_key_id = 'archive_key_01',
		    recipient_accepted_at = clock_timestamp(), recipient_trust_generation = 1,
		    acknowledged_at = clock_timestamp()
		WHERE delivery_id = $1
	`, delivery.DeliveryID); err == nil {
		t.Fatal("acknowledgement without an append-only operation was accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_export_deliveries WHERE delivery_id = $1`, delivery.DeliveryID); err == nil {
		t.Fatal("delivery deletion was accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_export_delivery_operations SET actor_id = 'other' WHERE delivery_id = $1`, delivery.DeliveryID); err == nil {
		t.Fatal("delivery operation mutation was accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_export_delivery_transports SET destination_digest = decode(repeat('11', 32), 'hex') WHERE delivery_id = $1`, delivery.DeliveryID); err == nil {
		t.Fatal("delivery transport mutation was accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_export_delivery_transports WHERE delivery_id = $1`, delivery.DeliveryID); err == nil {
		t.Fatal("delivery transport deletion was accepted")
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_export_recipient_trust_events SET actor_id = 'other'`); err == nil {
		t.Fatal("recipient trust event mutation was accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_export_recipient_trust_keys`); err == nil {
		t.Fatal("recipient trust key deletion was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_recipient_trust_events (
			isolation_domain_id, recipient_id, generation, operation,
			trust_contract, trust_profile_sha256, actor_id, reason_digest, correlation_id
		) VALUES (
			'iso_00000000000000000001', 'archive.secondary', 1, 'activate',
			'dataground.audit-export-recipient-trust/ed25519/v1',
			'sha256:' || repeat('4', 64), 'operator@example.invalid',
			decode(repeat('5', 64), 'hex'), 'cor_00000000000000000020'
		)
	`); err == nil {
		t.Fatal("recipient trust activation without keys was accepted")
	}
}

func TestAuditExportDeliveryRequiresCompletedTransportBeforeAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	activateAuditExportRecipientTrust(t, ctx, repository, delivery, 1,
		"sha256:"+strings.Repeat("3", 64), "cor_00000000000000000009")
	if err := repository.PrepareAuditExportDelivery(
		ctx, delivery, auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001"),
	); err != nil {
		t.Fatal(err)
	}
	acknowledgementDigest := sha256.Sum256([]byte("archive receipt"))
	acknowledgement := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       acknowledgementDigest[:],
		DeliveryContract:            persistence.AuditExportWorkloadDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v5",
		RecipientTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		RecipientSigningKeyID:       "archive_key_01", RecipientTrustGeneration: 1,
		AcceptedAt: time.Now().UTC().Truncate(time.Microsecond),
		Attribution: auditExportDeliveryAttribution(
			"record archive receipt", "cor_00000000000000000002",
		),
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement); !errors.Is(err, persistence.ErrAuditExportDeliveryConflict) {
		t.Fatalf("acknowledgement before transport error = %v", err)
	}
	transportAttribution := auditExportDeliveryAttribution(
		"transport delivery", "cor_00000000000000000030",
	)
	if err := repository.ReserveAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
		auditExportWorkloadIdentityAuthorization(), transportAttribution,
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement); !errors.Is(err, persistence.ErrAuditExportDeliveryConflict) {
		t.Fatalf("acknowledgement during transport error = %v", err)
	}
	if err := repository.CompleteAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
		auditExportWorkloadIdentityAuthorization(), transportAttribution,
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement); err != nil {
		t.Fatalf("acknowledgement after transport: %v", err)
	}
}

func TestAuditExportDeliveryTransportRequiresCurrentRecipientTrust(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	profileDigest := "sha256:" + strings.Repeat("3", 64)
	activateAuditExportRecipientTrust(
		t, ctx, repository, delivery, 1, profileDigest, "cor_00000000000000000009",
	)
	if err := repository.PrepareAuditExportDelivery(
		ctx,
		delivery,
		auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001"),
	); err != nil {
		t.Fatal(err)
	}
	if err := repository.ChangeAuditExportRecipientTrust(
		ctx,
		auditExportRecipientTrustChange(
			delivery, "revoke", 2, profileDigest, "cor_00000000000000000010",
		),
	); err != nil {
		t.Fatal(err)
	}
	transport := auditExportDeliveryAttribution(
		"transport delivery", "cor_00000000000000000030",
	)
	if err := repository.ReserveAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
		auditExportWorkloadIdentityAuthorization(), transport,
	); !errors.Is(err, persistence.ErrAuditExportRecipientTrustUnauthorized) {
		t.Fatalf("transport under revoked trust error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_delivery_operations (
			delivery_id, operation, isolation_domain_id, actor_id, correlation_id,
			reason_digest, evidence_digest
		) VALUES ($1, 'transport', $2, $3, $4, $5, $6)
	`, delivery.DeliveryID, delivery.IsolationDomainID, transport.ActorID,
		transport.CorrelationID, transport.ReasonDigest, delivery.EncryptedPackageDigest); err != nil {
		t.Fatalf("install direct transport operation fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_delivery_transports (
			delivery_id, isolation_domain_id, transport_contract,
			destination_digest, encrypted_package_digest
		) VALUES ($1, $2, 'dataground.audit-export-transport/s3-immutable/v1', $3, $4)
	`, delivery.DeliveryID, delivery.IsolationDomainID, delivery.DestinationDigest,
		delivery.EncryptedPackageDigest); err == nil {
		t.Fatal("direct transport reservation bypassed revoked recipient trust")
	}
}

func TestAuditExportDeliveryTransportRequiresCurrentWorkloadIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	activateAuditExportRecipientTrust(
		t, ctx, repository, delivery, 1, "sha256:"+strings.Repeat("3", 64),
		"cor_00000000000000000009",
	)
	if err := repository.PrepareAuditExportDelivery(
		ctx, delivery,
		auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001"),
	); err != nil {
		t.Fatal(err)
	}
	reasonDigest := sha256.Sum256([]byte("revoke audit export workload identity"))
	revocation := persistence.AuditExportWorkloadIdentityChange{
		Contract:  persistence.AuditExportWorkloadIdentityAuthorizationContract,
		Operation: "revoke", IsolationDomainID: delivery.IsolationDomainID,
		WorkloadID: "audit-export.dispatcher", Generation: 2,
		GrantSHA256:             "sha256:" + strings.Repeat("8", 64),
		ClientCertificateSHA256: "sha256:" + strings.Repeat("6", 64),
		ActorID:                 "operator@example.invalid", ReasonDigest: reasonDigest[:],
		CorrelationID: "cor_00000000000000000029",
	}
	if err := repository.ChangeAuditExportWorkloadIdentity(ctx, revocation); err != nil {
		t.Fatal(err)
	}
	attribution := auditExportDeliveryAttribution(
		"transport delivery", "cor_00000000000000000030",
	)
	if err := repository.ReserveAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
		auditExportWorkloadIdentityAuthorization(), attribution,
	); !errors.Is(err, persistence.ErrAuditExportWorkloadIdentityUnauthorized) {
		t.Fatalf("transport under revoked workload identity error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_delivery_operations (
			delivery_id, operation, isolation_domain_id, actor_id, correlation_id,
			reason_digest, evidence_digest
		) VALUES ($1, 'transport', $2, $3, $4, $5, $6)
	`, delivery.DeliveryID, delivery.IsolationDomainID, attribution.ActorID,
		attribution.CorrelationID, attribution.ReasonDigest, delivery.EncryptedPackageDigest); err != nil {
		t.Fatalf("install direct transport operation fixture: %v", err)
	}
	authorization := auditExportWorkloadIdentityAuthorization()
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_delivery_transports (
			delivery_id, isolation_domain_id, transport_contract, destination_digest,
			encrypted_package_digest, workload_id, workload_identity_grant_sha256,
			workload_identity_generation, client_certificate_sha256
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, delivery.DeliveryID, delivery.IsolationDomainID,
		persistence.AuditExportDeliveryWorkloadTransportContract, delivery.DestinationDigest,
		delivery.EncryptedPackageDigest, authorization.WorkloadID, authorization.GrantSHA256,
		authorization.Generation, authorization.ClientCertificateSHA256); err == nil {
		t.Fatal("direct SQL transport bypassed workload identity revocation")
	}
}

func TestAuditExportWorkloadIdentityRejectsIncompleteDirectActivation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	reasonDigest := sha256.Sum256([]byte("incomplete workload identity"))
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_workload_identity_events (
			isolation_domain_id, workload_id, generation, authorization_contract, operation,
			grant_sha256, client_certificate_sha256, actor_id, reason_digest, correlation_id
		) VALUES ($1, $2, 1, $3, 'activate', $4, $5, $6, $7, $8)
	`, "iso_00000000000000000001", "audit-export.dispatcher",
		persistence.AuditExportWorkloadIdentityAuthorizationContract,
		"sha256:"+strings.Repeat("8", 64), "sha256:"+strings.Repeat("6", 64),
		"operator@example.invalid", reasonDigest[:], "cor_00000000000000000031"); err == nil {
		t.Fatal("direct SQL installed an incomplete workload identity activation")
	}
}

func TestAuditExportDeliveryMTLSTransportPersistsExactContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	activateAuditExportRecipientTrust(
		t, ctx, repository, delivery, 1, "sha256:"+strings.Repeat("3", 64),
		"cor_00000000000000000009",
	)
	if err := repository.PrepareAuditExportDelivery(
		ctx, delivery,
		auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001"),
	); err != nil {
		t.Fatal(err)
	}
	attribution := auditExportDeliveryAttribution(
		"transport delivery", "cor_00000000000000000030",
	)
	if err := repository.ReserveAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
		auditExportWorkloadIdentityAuthorization(), attribution,
	); err != nil {
		t.Fatal(err)
	}
	changedIdentity := auditExportWorkloadIdentityAuthorization()
	changedIdentity.GrantSHA256 = "sha256:" + strings.Repeat("7", 64)
	if err := repository.ReserveAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
		changedIdentity, attribution,
	); !errors.Is(err, persistence.ErrAuditExportDeliveryConflict) {
		t.Fatalf("transport contract substitution error = %v", err)
	}
	if err := repository.CompleteAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
		auditExportWorkloadIdentityAuthorization(), attribution,
	); err != nil {
		t.Fatal(err)
	}
	var contract, state string
	if err := pool.QueryRow(ctx, `
		SELECT transport_contract, state
		FROM audit_export_delivery_transports
		WHERE delivery_id = $1
	`, delivery.DeliveryID).Scan(&contract, &state); err != nil {
		t.Fatal(err)
	}
	if contract != persistence.AuditExportDeliveryWorkloadTransportContract || state != "completed" {
		t.Fatalf("transport = %q %q", contract, state)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE audit_export_delivery_transports
		SET transport_contract = 'dataground.audit-export-transport/s3-immutable/v1'
		WHERE delivery_id = $1
	`, delivery.DeliveryID); err == nil {
		t.Fatal("direct valid transport contract substitution was accepted")
	}
	pool.Close()
	database, err := persistence.OpenSQL(ctx, os.Getenv("DATAGROUND_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 28); err == nil {
		t.Fatal("authenticated transport evidence was discarded by schema downgrade")
	}
	if err := persistence.RequireCurrentSchema(ctx, database); err != nil {
		t.Fatalf("failed downgrade changed current schema: %v", err)
	}
}

func TestAuditExportDeliveryConcurrentReplayConverges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	activateAuditExportRecipientTrust(t, ctx, repository, delivery, 1, "sha256:"+strings.Repeat("3", 64),
		"cor_00000000000000000009")
	prepare := auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001")
	runConcurrent := func(action func() error) {
		t.Helper()
		start := make(chan struct{})
		errorsByCall := make([]error, 4)
		var wait sync.WaitGroup
		for index := range errorsByCall {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				errorsByCall[index] = action()
			}()
		}
		close(start)
		wait.Wait()
		for index, err := range errorsByCall {
			if err != nil {
				t.Fatalf("concurrent call %d: %v", index, err)
			}
		}
	}
	runConcurrent(func() error {
		return repository.PrepareAuditExportDelivery(ctx, delivery, prepare)
	})
	transportAttribution := auditExportDeliveryAttribution(
		"transport delivery", "cor_00000000000000000030",
	)
	runConcurrent(func() error {
		return repository.ReserveAuditExportDeliveryTransportWithWorkloadIdentity(
			ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
			auditExportWorkloadIdentityAuthorization(), transportAttribution,
		)
	})
	runConcurrent(func() error {
		return repository.CompleteAuditExportDeliveryTransportWithWorkloadIdentity(
			ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
			auditExportWorkloadIdentityAuthorization(), transportAttribution,
		)
	})
	acknowledgementDigest := sha256.Sum256([]byte("archive receipt"))
	acknowledgement := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       acknowledgementDigest[:],
		DeliveryContract:            persistence.AuditExportWorkloadDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v5",
		RecipientTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		RecipientSigningKeyID:       "archive_key_01",
		RecipientTrustGeneration:    1,
		AcceptedAt:                  time.Date(2026, 8, 3, 15, 30, 0, 123000, time.UTC),
		Attribution: auditExportDeliveryAttribution(
			"record archive receipt",
			"cor_00000000000000000002",
		),
	}
	runConcurrent(func() error {
		return repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement)
	})
	var deliveryCount, operationCount, auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM audit_export_deliveries),
		       (SELECT count(*) FROM audit_export_delivery_operations),
		       (SELECT count(*) FROM audit_records WHERE resource_type = 'audit-export-delivery')
	`).Scan(&deliveryCount, &operationCount, &auditCount); err != nil {
		t.Fatal(err)
	}
	if deliveryCount != 1 || operationCount != 3 || auditCount != 4 {
		t.Fatalf("deliveries = %d, operations = %d, audits = %d", deliveryCount, operationCount, auditCount)
	}
}

func TestAuditExportRecipientTrustRevocationBlocksNewAcknowledgements(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	profileDigest := "sha256:" + strings.Repeat("3", 64)
	activateAuditExportRecipientTrust(t, ctx, repository, delivery, 1, profileDigest,
		"cor_00000000000000000009")
	if err := repository.PrepareAuditExportDelivery(
		ctx, delivery, auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001"),
	); err != nil {
		t.Fatal(err)
	}
	completeAuditExportDeliveryTransport(t, ctx, repository, delivery)
	wrongKeyDigest := sha256.Sum256([]byte("wrong-key archive receipt"))
	wrongKey := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       wrongKeyDigest[:],
		DeliveryContract:            persistence.AuditExportWorkloadDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v5",
		RecipientTrustProfileSHA256: profileDigest,
		RecipientSigningKeyID:       "archive_key_02",
		RecipientTrustGeneration:    1,
		AcceptedAt:                  time.Date(2026, 8, 3, 15, 30, 0, 123000, time.UTC),
		Attribution: auditExportDeliveryAttribution(
			"record archive receipt", "cor_00000000000000000002",
		),
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, wrongKey); !errors.Is(err, persistence.ErrAuditExportRecipientTrustUnauthorized) {
		t.Fatalf("unauthorized signing key error = %v", err)
	}
	revocation := auditExportRecipientTrustChange(delivery, "revoke", 2, profileDigest,
		"cor_00000000000000000010")
	if err := repository.ChangeAuditExportRecipientTrust(ctx, revocation); err != nil {
		t.Fatalf("revoke recipient trust: %v", err)
	}
	if err := repository.ChangeAuditExportRecipientTrust(ctx, revocation); err != nil {
		t.Fatalf("replay recipient trust revocation: %v", err)
	}
	acknowledgementDigest := sha256.Sum256([]byte("archive receipt"))
	acknowledgement := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       acknowledgementDigest[:],
		DeliveryContract:            persistence.AuditExportWorkloadDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v5",
		RecipientTrustProfileSHA256: profileDigest,
		RecipientSigningKeyID:       "archive_key_01",
		RecipientTrustGeneration:    1,
		AcceptedAt:                  time.Date(2026, 8, 3, 15, 30, 0, 123000, time.UTC),
		Attribution: auditExportDeliveryAttribution(
			"record archive receipt", "cor_00000000000000000002",
		),
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement); !errors.Is(err, persistence.ErrAuditExportRecipientTrustUnauthorized) {
		t.Fatalf("revoked acknowledgement error = %v", err)
	}
	rotation := auditExportRecipientTrustChange(delivery, "activate", 3,
		"sha256:"+strings.Repeat("4", 64), "cor_00000000000000000011")
	if err := repository.ChangeAuditExportRecipientTrust(ctx, rotation); err != nil {
		t.Fatalf("rotate recipient trust: %v", err)
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement); !errors.Is(err, persistence.ErrAuditExportRecipientTrustUnauthorized) {
		t.Fatalf("superseded acknowledgement error = %v", err)
	}
	changedReplay := rotation
	changedReplay.ActorID = "other@example.invalid"
	if err := repository.ChangeAuditExportRecipientTrust(ctx, changedReplay); !errors.Is(err, persistence.ErrAuditExportRecipientTrustConflict) {
		t.Fatalf("changed trust replay error = %v", err)
	}
	duplicateActivation := auditExportRecipientTrustChange(delivery, "activate", 4,
		rotation.TrustProfileSHA256, "cor_00000000000000000012")
	if err := repository.ChangeAuditExportRecipientTrust(ctx, duplicateActivation); !errors.Is(err, persistence.ErrAuditExportRecipientTrustConflict) {
		t.Fatalf("duplicate active profile error = %v", err)
	}
	database, err := persistence.OpenSQL(ctx, os.Getenv("DATAGROUND_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 23); err == nil {
		t.Fatal("recipient trust evidence was discarded by schema downgrade")
	}
	if err := persistence.RequireCurrentSchema(ctx, database); err != nil {
		t.Fatalf("failed downgrade changed current schema: %v", err)
	}
}

func TestExternalRecipientProofRevocationBlocksActivationAndAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	profileDigest := "sha256:" + strings.Repeat("3", 64)
	activateAuditExportRecipientTrust(t, ctx, repository, delivery, 1, profileDigest,
		"cor_00000000000000000009")
	if err := repository.PrepareAuditExportDelivery(
		ctx, delivery, auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001"),
	); err != nil {
		t.Fatal(err)
	}
	completeAuditExportDeliveryTransport(t, ctx, repository, delivery)
	revocation := auditExportRecipientProofRevocationRecord(
		delivery.IsolationDomainID, "key", "cor_00000000000000000020",
	)
	if err := repository.RecordAuditExportRecipientProofRevocation(ctx, revocation); err != nil {
		t.Fatalf("record external recipient proof revocation: %v", err)
	}
	if err := repository.RecordAuditExportRecipientProofRevocation(ctx, revocation); err != nil {
		t.Fatalf("replay external recipient proof revocation: %v", err)
	}
	otherDomainDelivery := delivery
	otherDomainDelivery.IsolationDomainID = "iso_00000000000000000002"
	otherDomainActivation := auditExportRecipientTrustChange(
		otherDomainDelivery, "activate", 1, profileDigest, "cor_00000000000000000021",
	)
	if err := repository.ChangeAuditExportRecipientTrust(ctx, otherDomainActivation); err != nil {
		t.Fatalf("recipient proof revocation crossed isolation domains: %v", err)
	}
	changedReplay := revocation
	changedReplay.ActorID = "other@example.invalid"
	if err := repository.RecordAuditExportRecipientProofRevocation(ctx, changedReplay); !errors.Is(err, persistence.ErrAuditExportRecipientProofRevocationConflict) {
		t.Fatalf("changed recipient proof revocation replay error = %v", err)
	}
	acknowledgementDigest := sha256.Sum256([]byte("archive receipt"))
	acknowledgement := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       acknowledgementDigest[:],
		DeliveryContract:            persistence.AuditExportWorkloadDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v5",
		RecipientTrustProfileSHA256: profileDigest,
		RecipientSigningKeyID:       "archive_key_01",
		RecipientTrustGeneration:    1,
		AcceptedAt:                  time.Now().UTC().Truncate(time.Microsecond),
		Attribution: auditExportDeliveryAttribution(
			"record archive receipt", "cor_00000000000000000002",
		),
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement); !errors.Is(err, persistence.ErrAuditExportRecipientTrustUnauthorized) {
		t.Fatalf("externally revoked acknowledgement error = %v", err)
	}
	manualRevocation := auditExportRecipientTrustChange(
		delivery, "revoke", 2, profileDigest, "cor_00000000000000000010",
	)
	if err := repository.ChangeAuditExportRecipientTrust(ctx, manualRevocation); err != nil {
		t.Fatalf("revoke recipient trust after external notice: %v", err)
	}
	reactivation := auditExportRecipientTrustChange(
		delivery, "activate", 3, profileDigest, "cor_00000000000000000011",
	)
	if err := repository.ChangeAuditExportRecipientTrust(ctx, reactivation); !errors.Is(err, persistence.ErrAuditExportRecipientTrustUnauthorized) {
		t.Fatalf("externally revoked proof reactivation error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_recipient_trust_events (
			isolation_domain_id, recipient_id, generation, authorization_contract, operation,
			trust_contract, trust_profile_sha256, identity_proof_contract,
			identity_proof_sha256, identity_proof_evidence_sha256, proofing_authority_id,
			proofing_trust_profile_sha256, proofing_signing_key_id,
			identity_proof_verified_at, identity_proof_expires_at,
			actor_id, reason_digest, correlation_id
		) VALUES (
			$1, $2, 3, 'dataground.audit-export-recipient-trust-authorization/v2', 'activate',
			'dataground.audit-export-recipient-trust/ed25519/v1', $3,
			'dataground.audit-export-recipient-identity-proof/ed25519/v1',
			'sha256:' || repeat('4', 64), 'sha256:' || repeat('5', 64),
			'archive-proofing.primary', 'sha256:' || repeat('6', 64), 'proofing_key_01',
			clock_timestamp() - interval '1 hour', clock_timestamp() + interval '1 hour',
			'operator@example.invalid', decode(repeat('5', 64), 'hex'),
			'cor_00000000000000000012'
		)
	`, delivery.IsolationDomainID, delivery.RecipientID, profileDigest); err == nil {
		t.Fatal("direct activation bypassed external recipient proof revocation")
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_export_recipient_proof_revocations SET actor_id = 'other'`); err == nil {
		t.Fatal("recipient proof revocation mutation was accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_export_recipient_proof_revocations`); err == nil {
		t.Fatal("recipient proof revocation deletion was accepted")
	}

	exported, err := repository.ExportOperatorAuditRecords(ctx, delivery.IsolationDomainID, "", 10)
	if err != nil {
		t.Fatalf("export recipient proof revocation audit: %v", err)
	}
	foundRevocation := false
	for _, record := range exported.Records {
		if record.Action == "audit-export-recipient-proof-revocation.record" {
			foundRevocation = true
			break
		}
	}
	if !foundRevocation {
		t.Fatalf("recipient proof revocation audit export = %#v", exported)
	}
}

func TestCompletedAcknowledgementSurvivesExternalRecipientProofRevocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	profileDigest := "sha256:" + strings.Repeat("3", 64)
	activateAuditExportRecipientTrust(t, ctx, repository, delivery, 1, profileDigest,
		"cor_00000000000000000009")
	if err := repository.PrepareAuditExportDelivery(
		ctx, delivery, auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001"),
	); err != nil {
		t.Fatal(err)
	}
	completeAuditExportDeliveryTransport(t, ctx, repository, delivery)
	acknowledgementDigest := sha256.Sum256([]byte("archive receipt"))
	acknowledgement := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       acknowledgementDigest[:],
		DeliveryContract:            persistence.AuditExportWorkloadDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v5",
		RecipientTrustProfileSHA256: profileDigest, RecipientSigningKeyID: "archive_key_01",
		RecipientTrustGeneration: 1,
		AcceptedAt:               time.Now().UTC().Truncate(time.Microsecond),
		Attribution: auditExportDeliveryAttribution(
			"record archive receipt", "cor_00000000000000000002",
		),
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement); err != nil {
		t.Fatal(err)
	}
	revocation := auditExportRecipientProofRevocationRecord(
		delivery.IsolationDomainID, "profile", "cor_00000000000000000020",
	)
	if err := repository.RecordAuditExportRecipientProofRevocation(ctx, revocation); err != nil {
		t.Fatal(err)
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement); err != nil {
		t.Fatalf("replay completed acknowledgement after external revocation: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM audit_export_deliveries WHERE delivery_id = $1
	`, delivery.DeliveryID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "acknowledged" {
		t.Fatalf("delivery status after external revocation = %q", status)
	}
}

func TestFutureRecipientProofRevocationActivatesOnDatabaseClock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	revocation := auditExportRecipientProofRevocationRecord(
		delivery.IsolationDomainID, "profile", "cor_00000000000000000020",
	)
	revocation.EffectiveAt = time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour)
	if err := repository.RecordAuditExportRecipientProofRevocation(ctx, revocation); err != nil {
		t.Fatal(err)
	}
	activateAuditExportRecipientTrust(t, ctx, repository, delivery, 1,
		"sha256:"+strings.Repeat("3", 64), "cor_00000000000000000009")
	if err := repository.PrepareAuditExportDelivery(
		ctx, delivery, auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001"),
	); err != nil {
		t.Fatal(err)
	}
	completeAuditExportDeliveryTransport(t, ctx, repository, delivery)
	acknowledgementDigest := sha256.Sum256([]byte("archive receipt"))
	acknowledgement := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       acknowledgementDigest[:],
		DeliveryContract:            persistence.AuditExportWorkloadDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v5",
		RecipientTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		RecipientSigningKeyID:       "archive_key_01",
		RecipientTrustGeneration:    1,
		AcceptedAt:                  time.Now().UTC().Truncate(time.Microsecond),
		Attribution: auditExportDeliveryAttribution(
			"record archive receipt", "cor_00000000000000000002",
		),
	}
	if err := repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement); err != nil {
		t.Fatalf("future-effective recipient proof revocation blocked early: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE audit_export_recipient_proof_revocations
		SET effective_at = clock_timestamp() - interval '1 second'
	`); err == nil {
		t.Fatal("future recipient proof revocation was mutable")
	}
}

func TestExternalRecipientProofRevocationSerializesWithAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	profileDigest := "sha256:" + strings.Repeat("3", 64)
	activateAuditExportRecipientTrust(t, ctx, repository, delivery, 1, profileDigest,
		"cor_00000000000000000009")
	if err := repository.PrepareAuditExportDelivery(
		ctx, delivery, auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001"),
	); err != nil {
		t.Fatal(err)
	}
	completeAuditExportDeliveryTransport(t, ctx, repository, delivery)
	acknowledgementDigest := sha256.Sum256([]byte("archive receipt"))
	acknowledgement := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       acknowledgementDigest[:],
		DeliveryContract:            persistence.AuditExportWorkloadDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v5",
		RecipientTrustProfileSHA256: profileDigest, RecipientSigningKeyID: "archive_key_01",
		RecipientTrustGeneration: 1,
		AcceptedAt:               time.Now().UTC().Truncate(time.Microsecond),
		Attribution: auditExportDeliveryAttribution(
			"record archive receipt", "cor_00000000000000000002",
		),
	}
	revocation := auditExportRecipientProofRevocationRecord(
		delivery.IsolationDomainID, "profile", "cor_00000000000000000020",
	)
	type outcome struct {
		operation string
		err       error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	go func() {
		<-start
		results <- outcome{"acknowledge", repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement)}
	}()
	go func() {
		<-start
		results <- outcome{"revoke", repository.RecordAuditExportRecipientProofRevocation(ctx, revocation)}
	}()
	close(start)
	first, second := <-results, <-results
	for _, result := range []outcome{first, second} {
		if result.operation == "revoke" && result.err != nil {
			t.Fatalf("concurrent revocation error = %v", result.err)
		}
		if result.operation == "acknowledge" && result.err != nil &&
			!errors.Is(result.err, persistence.ErrAuditExportRecipientTrustUnauthorized) {
			t.Fatalf("concurrent acknowledgement error = %v", result.err)
		}
	}
	var status string
	var revocationCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT status FROM audit_export_deliveries WHERE delivery_id = $1),
			(SELECT count(*) FROM audit_export_recipient_proof_revocations)
	`, delivery.DeliveryID).Scan(&status, &revocationCount); err != nil {
		t.Fatal(err)
	}
	if revocationCount != 1 || (status != "prepared" && status != "acknowledged") {
		t.Fatalf("delivery status = %q; revocations = %d", status, revocationCount)
	}
}

func TestAuditExportRecipientTrustRevocationSerializesWithAcknowledgement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	profileDigest := "sha256:" + strings.Repeat("3", 64)
	activateAuditExportRecipientTrust(t, ctx, repository, delivery, 1, profileDigest,
		"cor_00000000000000000009")
	if err := repository.PrepareAuditExportDelivery(
		ctx, delivery, auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001"),
	); err != nil {
		t.Fatal(err)
	}
	completeAuditExportDeliveryTransport(t, ctx, repository, delivery)
	acknowledgementDigest := sha256.Sum256([]byte("archive receipt"))
	acknowledgement := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       acknowledgementDigest[:],
		DeliveryContract:            persistence.AuditExportWorkloadDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v5",
		RecipientTrustProfileSHA256: profileDigest,
		RecipientSigningKeyID:       "archive_key_01",
		RecipientTrustGeneration:    1,
		AcceptedAt:                  time.Date(2026, 8, 3, 15, 30, 0, 123000, time.UTC),
		Attribution: auditExportDeliveryAttribution(
			"record archive receipt", "cor_00000000000000000002",
		),
	}
	revocation := auditExportRecipientTrustChange(delivery, "revoke", 2, profileDigest,
		"cor_00000000000000000010")
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		results <- repository.AcknowledgeAuditExportDelivery(ctx, delivery, acknowledgement)
	}()
	go func() {
		<-start
		results <- repository.ChangeAuditExportRecipientTrust(ctx, revocation)
	}()
	close(start)
	first, second := <-results, <-results
	if first != nil && !errors.Is(first, persistence.ErrAuditExportRecipientTrustUnauthorized) {
		t.Fatalf("first concurrent result = %v", first)
	}
	if second != nil && !errors.Is(second, persistence.ErrAuditExportRecipientTrustUnauthorized) {
		t.Fatalf("second concurrent result = %v", second)
	}
	if errors.Is(first, persistence.ErrAuditExportRecipientTrustUnauthorized) &&
		errors.Is(second, persistence.ErrAuditExportRecipientTrustUnauthorized) {
		t.Fatal("both revocation and acknowledgement were rejected")
	}
	var latestOperation, deliveryStatus string
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT operation FROM audit_export_recipient_trust_events
			 WHERE isolation_domain_id = $1 AND recipient_id = $2 ORDER BY generation DESC LIMIT 1),
			(SELECT status FROM audit_export_deliveries WHERE delivery_id = $3)
	`, delivery.IsolationDomainID, delivery.RecipientID, delivery.DeliveryID).Scan(
		&latestOperation, &deliveryStatus,
	); err != nil {
		t.Fatal(err)
	}
	if latestOperation != "revoke" || (deliveryStatus != "prepared" && deliveryStatus != "acknowledged") {
		t.Fatalf("latest trust operation = %q; delivery status = %q", latestOperation, deliveryStatus)
	}
}

func auditExportDeliveryFixture(deliveryID string) persistence.AuditExportDelivery {
	envelopeDigest := sha256.Sum256([]byte("sealed envelope"))
	destinationDigest := sha256.Sum256([]byte("archive.primary\nobject-prefix"))
	packageDigest := sha256.Sum256([]byte("recipient encrypted package"))
	return persistence.AuditExportDelivery{
		Contract: persistence.AuditExportWorkloadDeliveryContract, DeliveryID: deliveryID,
		IsolationDomainID: "iso_00000000000000000001", ExportKind: "operator",
		ExportID: "oax_00000000000000000001", EnvelopeDigest: envelopeDigest[:],
		ExportSHA256:       "sha256:" + strings.Repeat("1", 64),
		TrustProfileSHA256: "sha256:" + strings.Repeat("2", 64), SigningKeyID: "audit_key_01",
		RecipientID: "archive.primary", DestinationDigest: destinationDigest[:],
		EncryptedPackageDigest:      packageDigest[:],
		RecipientTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		RecipientEncryptionKeyID:    "archive_encryption_key_01",
	}
}

func auditExportDeliveryAttribution(reason string, correlationID string) persistence.AuditExportDeliveryAttribution {
	digest := sha256.Sum256([]byte(reason))
	return persistence.AuditExportDeliveryAttribution{
		ActorID: "operator@example.invalid", ReasonDigest: digest[:], CorrelationID: correlationID,
	}
}

func completeAuditExportDeliveryTransport(
	t *testing.T,
	ctx context.Context,
	repository *persistence.Repository,
	delivery persistence.AuditExportDelivery,
) {
	t.Helper()
	attribution := auditExportDeliveryAttribution(
		"transport delivery", "cor_00000000000000000030",
	)
	if err := repository.ReserveAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
		auditExportWorkloadIdentityAuthorization(), attribution,
	); err != nil {
		t.Fatalf("reserve audit export delivery transport: %v", err)
	}
	if err := repository.CompleteAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
		auditExportWorkloadIdentityAuthorization(), attribution,
	); err != nil {
		t.Fatalf("complete audit export delivery transport: %v", err)
	}
}

func activateAuditExportRecipientTrust(
	t *testing.T,
	ctx context.Context,
	repository *persistence.Repository,
	delivery persistence.AuditExportDelivery,
	generation int64,
	profileDigest string,
	correlationID string,
) {
	t.Helper()
	change := auditExportRecipientTrustChange(delivery, "activate", generation, profileDigest, correlationID)
	if err := repository.ChangeAuditExportRecipientTrust(ctx, change); err != nil {
		t.Fatalf("activate audit export recipient trust: %v", err)
	}
	if generation == 1 {
		activateAuditExportWorkloadIdentity(t, ctx, repository, delivery)
	}
}

func activateAuditExportWorkloadIdentity(
	t *testing.T,
	ctx context.Context,
	repository *persistence.Repository,
	delivery persistence.AuditExportDelivery,
) {
	t.Helper()
	reasonDigest := sha256.Sum256([]byte("authorize audit export workload identity"))
	issuedAt := time.Now().UTC().Truncate(time.Microsecond).Add(-2 * time.Minute)
	change := persistence.AuditExportWorkloadIdentityChange{
		Contract:  persistence.AuditExportWorkloadIdentityAuthorizationContract,
		Operation: "activate", IsolationDomainID: delivery.IsolationDomainID,
		WorkloadID: "audit-export.dispatcher", Generation: 1,
		GrantContract:            "dataground.audit-export-workload-identity-grant/ed25519/v1",
		GrantSHA256:              "sha256:" + strings.Repeat("8", 64),
		Audience:                 "dataground.audit-export-transport",
		ClientCertificateSHA256:  "sha256:" + strings.Repeat("6", 64),
		AuthorityID:              "workload-issuer.primary",
		IssuerTrustProfileSHA256: "sha256:" + strings.Repeat("9", 64),
		IssuerSigningKeyID:       "issuer_key_01", IssuedAt: issuedAt,
		NotBefore: issuedAt.Add(time.Minute), ExpiresAt: issuedAt.Add(time.Hour),
		ActorID: "operator@example.invalid", ReasonDigest: reasonDigest[:],
		CorrelationID: "cor_00000000000000000028",
	}
	if err := repository.ChangeAuditExportWorkloadIdentity(ctx, change); err != nil {
		t.Fatalf("activate audit export workload identity: %v", err)
	}
}

func auditExportWorkloadIdentityAuthorization() persistence.AuditExportWorkloadIdentityAuthorization {
	return persistence.AuditExportWorkloadIdentityAuthorization{
		WorkloadID:              "audit-export.dispatcher",
		GrantSHA256:             "sha256:" + strings.Repeat("8", 64),
		ClientCertificateSHA256: "sha256:" + strings.Repeat("6", 64),
		Generation:              1,
	}
}

func auditExportRecipientTrustChange(
	delivery persistence.AuditExportDelivery,
	operation string,
	generation int64,
	profileDigest string,
	correlationID string,
) persistence.AuditExportRecipientTrustChange {
	reasonDigest := sha256.Sum256([]byte(operation + " archive trust"))
	var keyIDs []string
	var encryptionKeyIDs []string
	if operation == "activate" {
		keyIDs = []string{"archive_key_01"}
		encryptionKeyIDs = []string{"archive_encryption_key_01"}
	}
	change := persistence.AuditExportRecipientTrustChange{
		Contract:           persistence.AuditExportRecipientEncryptionAuthorizationContract,
		Operation:          operation,
		IsolationDomainID:  delivery.IsolationDomainID,
		RecipientID:        delivery.RecipientID,
		Generation:         generation,
		TrustContract:      "dataground.audit-export-recipient-trust/ed25519-x25519/v2",
		TrustProfileSHA256: profileDigest,
		KeyIDs:             keyIDs,
		EncryptionKeyIDs:   encryptionKeyIDs,
		ActorID:            "operator@example.invalid",
		ReasonDigest:       reasonDigest[:],
		CorrelationID:      correlationID,
	}
	if operation == "activate" {
		verifiedAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
		change.IdentityProofContract = "dataground.audit-export-recipient-identity-proof/ed25519/v1"
		change.IdentityProofSHA256 = "sha256:" + strings.Repeat("4", 64)
		change.IdentityProofEvidenceSHA256 = "sha256:" + strings.Repeat("5", 64)
		change.ProofingAuthorityID = "archive-proofing.primary"
		change.ProofingTrustProfileSHA256 = "sha256:" + strings.Repeat("6", 64)
		change.ProofingSigningKeyID = "proofing_key_01"
		change.IdentityProofVerifiedAt = verifiedAt
		change.IdentityProofExpiresAt = verifiedAt.Add(24 * time.Hour)
	}
	return change
}

func auditExportRecipientProofRevocationRecord(
	isolationDomainID string,
	scope string,
	correlationID string,
) persistence.AuditExportRecipientProofRevocationRecord {
	reasonDigest := sha256.Sum256([]byte("record external proof revocation"))
	issuedAt := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	record := persistence.AuditExportRecipientProofRevocationRecord{
		Contract:           persistence.AuditExportRecipientProofRevocationRecordContract,
		RevocationContract: "dataground.audit-export-recipient-proof-revocation/ed25519/v1",
		RevocationSHA256:   "sha256:" + strings.Repeat("7", 64),
		IsolationDomainID:  isolationDomainID, Scope: scope,
		ProofingAuthorityID:          "archive-proofing.primary",
		ProofingTrustProfileSHA256:   "sha256:" + strings.Repeat("6", 64),
		ExternalReasonSHA256:         "sha256:" + strings.Repeat("8", 64),
		RevocationAuthorityID:        "archive-revocation.primary",
		RevocationTrustProfileSHA256: "sha256:" + strings.Repeat("9", 64),
		RevocationSigningKeyID:       "revocation_key_01", IssuedAt: issuedAt,
		EffectiveAt: issuedAt.Add(-time.Hour), ActorID: "operator@example.invalid",
		ReasonDigest: reasonDigest[:], CorrelationID: correlationID,
	}
	if scope == "key" {
		record.ProofingSigningKeyID = "proofing_key_01"
	}
	return record
}
