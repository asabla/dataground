package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
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
	if !exported.Complete || len(exported.Records) != 7 ||
		exported.Records[0].Action != "audit-export-proofing-authority.activate" ||
		exported.Records[1].Action != "audit-export-recipient-trust.activate" ||
		exported.Records[2].Action != "audit-export-workload-identity.activate" ||
		exported.Records[3].Action != "audit-export-delivery.prepare" ||
		exported.Records[4].Action != "audit-export-delivery.transport" ||
		exported.Records[5].Action != "audit-export-delivery.transport-complete" ||
		exported.Records[6].Action != "audit-export-delivery.acknowledge" {
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

func TestAuditExportRecipientTrustRequiresActiveProofingAuthority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	change := auditExportRecipientTrustChange(
		delivery, "activate", 1, "sha256:"+strings.Repeat("3", 64),
		"cor_00000000000000000009",
	)
	if err := repository.ChangeAuditExportRecipientTrust(ctx, change); !errors.Is(err, persistence.ErrAuditExportProofingAuthorityUnauthorized) {
		t.Fatalf("recipient activation without authority error = %v", err)
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
			$1, $2, 1, 'dataground.audit-export-recipient-trust-authorization/v3', 'activate',
			'dataground.audit-export-recipient-trust/ed25519-x25519/v2', $3,
			'dataground.audit-export-recipient-identity-proof/ed25519/v1',
			'sha256:' || repeat('4', 64), 'sha256:' || repeat('5', 64),
			'archive-proofing.primary', 'sha256:' || repeat('6', 64), 'proofing_key_01',
			clock_timestamp() - interval '1 hour', clock_timestamp() + interval '1 hour',
			'operator@example.invalid', decode(repeat('5', 64), 'hex'),
			'cor_00000000000000000010'
		)
	`, delivery.IsolationDomainID, delivery.RecipientID, change.TrustProfileSHA256); err == nil {
		t.Fatal("direct recipient activation bypassed proofing authority governance")
	}
	activateAuditExportProofingAuthority(t, ctx, repository, delivery.IsolationDomainID)
	if err := repository.ChangeAuditExportRecipientTrust(ctx, change); err != nil {
		t.Fatalf("recipient activation under active proofing authority: %v", err)
	}
}

func TestAuditExportProofingAuthorityRotationIsSequentialAndReplayable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	domainID := "iso_00000000000000000001"
	activateAuditExportProofingAuthority(t, ctx, repository, domainID)
	first := auditExportProofingAuthorityChange(
		domainID, "activate", 1, "sha256:"+strings.Repeat("6", 64),
		[]string{"proofing_key_01"}, "cor_proofingauthority0001",
	)
	if err := repository.ChangeAuditExportProofingAuthority(ctx, first); err != nil {
		t.Fatalf("replay first proofing authority generation: %v", err)
	}
	rotation := auditExportProofingAuthorityChange(
		domainID, "activate", 2, "sha256:"+strings.Repeat("7", 64),
		[]string{"proofing_key_02"}, "cor_00000000000000000071",
	)
	if err := repository.ChangeAuditExportProofingAuthority(ctx, rotation); err != nil {
		t.Fatalf("rotate proofing authority: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_proofing_authority_keys (
			isolation_domain_id, generation, key_id
		) VALUES ($1, 2, 'proofing_key_late')
	`, domainID); err == nil {
		t.Fatal("proofing authority generation accepted a late signing key")
	}
	if err := repository.ChangeAuditExportProofingAuthority(ctx, first); err != nil {
		t.Fatalf("replay historical proofing authority generation: %v", err)
	}
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	change := auditExportRecipientTrustChange(
		delivery, "activate", 1, "sha256:"+strings.Repeat("3", 64),
		"cor_00000000000000000009",
	)
	if err := repository.ChangeAuditExportRecipientTrust(ctx, change); !errors.Is(err, persistence.ErrAuditExportProofingAuthorityUnauthorized) {
		t.Fatalf("recipient activation under rotated profile error = %v", err)
	}
	withdrawal := auditExportProofingAuthorityChange(
		domainID, "revoke", 3, rotation.TrustProfileSHA256, nil,
		"cor_00000000000000000072",
	)
	if err := repository.ChangeAuditExportProofingAuthority(ctx, withdrawal); err != nil {
		t.Fatalf("withdraw proofing authority: %v", err)
	}
	changedReplay := withdrawal
	changedReplay.ActorID = "other@example.invalid"
	if err := repository.ChangeAuditExportProofingAuthority(ctx, changedReplay); !errors.Is(err, persistence.ErrAuditExportProofingAuthorityConflict) {
		t.Fatalf("changed proofing authority replay error = %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_export_proofing_authority_events SET actor_id = 'other'`); err == nil {
		t.Fatal("proofing authority event mutation was accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_export_proofing_authority_keys`); err == nil {
		t.Fatal("proofing authority key deletion was accepted")
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

func TestAuditExportDeliveryTransportRejectsExternalWorkloadIdentityRevocation(t *testing.T) {
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
	now := time.Now().UTC().Truncate(time.Microsecond)
	revocation := auditExportWorkloadIdentityRevocationRecord(
		delivery.IsolationDomainID, now, "cor_00000000000000000032",
	)
	revocation.Acquisition = &persistence.AuditExportRevocationAcquisition{
		Contract:                   persistence.AuditExportRevocationAcquisitionContract,
		Purpose:                    persistence.AuditExportRevocationAuthorityPurposeWorkloadIdentity,
		SourceID:                   "archive-revocations.primary",
		SourceRegistrySHA256:       "sha256:" + strings.Repeat("f", 64),
		SourceGeneration:           1,
		NoticeCredentialSHA256:     "sha256:" + strings.Repeat("d", 64),
		NoticeCredentialGeneration: 1,
		TrustCredentialSHA256:      "sha256:" + strings.Repeat("e", 64),
		TrustCredentialGeneration:  1,
	}
	activateAuditExportRevocationAuthority(
		t, ctx, repository, delivery.IsolationDomainID,
		persistence.AuditExportRevocationAuthorityPurposeWorkloadIdentity,
		revocation.RevocationAuthorityID, revocation.RevocationTrustProfileSHA256,
		revocation.RevocationSigningKeyID, "cor_00000000000000000038",
	)
	activateAuditExportRevocationSource(
		t, ctx, repository, revocation.IsolationDomainID, revocation.Acquisition.Purpose,
		revocation.Acquisition.SourceID, revocation.Acquisition.SourceRegistrySHA256,
		1, "cor_00000000000000000039",
	)
	activateAuditExportRevocationCredentials(
		t, ctx, repository, revocation.IsolationDomainID, revocation.Acquisition,
		"cor_00000000000000000080", "cor_00000000000000000081",
	)
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(ctx, revocation); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(ctx, revocation); err != nil {
		t.Fatalf("replay external workload identity revocation: %v", err)
	}
	sourceWithdrawalReason := sha256.Sum256([]byte("withdraw acquired workload revocation source"))
	sourceWithdrawal := persistence.AuditExportRevocationSourceChange{
		Contract:  persistence.AuditExportRevocationSourceAuthorizationContract,
		Operation: "revoke", IsolationDomainID: revocation.IsolationDomainID,
		Purpose: revocation.Acquisition.Purpose, SourceID: revocation.Acquisition.SourceID,
		Generation: 2, SourceRegistrySHA256: revocation.Acquisition.SourceRegistrySHA256,
		ActorID: "operator@example.invalid", ReasonDigest: sourceWithdrawalReason[:],
		CorrelationID: "cor_00000000000000000040",
	}
	if err := repository.ChangeAuditExportRevocationSource(ctx, sourceWithdrawal); err != nil {
		t.Fatalf("withdraw acquired workload revocation source: %v", err)
	}
	credentialRevocationReason := sha256.Sum256(
		[]byte("record remote workload notice credential revocation"),
	)
	credentialRevocation := persistence.AuditExportRevocationCredentialChange{
		Contract:  persistence.AuditExportRevocationCredentialAuthorizationContract,
		Operation: "revoke", IsolationDomainID: revocation.IsolationDomainID,
		Purpose: revocation.Acquisition.Purpose, SourceID: revocation.Acquisition.SourceID,
		SourceRegistrySHA256: revocation.Acquisition.SourceRegistrySHA256,
		Endpoint:             "notice", Generation: 2,
		CredentialSHA256: revocation.Acquisition.NoticeCredentialSHA256,
		ActorID:          "operator@example.invalid", ReasonDigest: credentialRevocationReason[:],
		CorrelationID: "cor_00000000000000000084",
	}
	if err := repository.ChangeAuditExportRevocationCredential(
		ctx, credentialRevocation,
	); err != nil {
		t.Fatalf("revoke acquired workload notice credential: %v", err)
	}
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(ctx, revocation); err != nil {
		t.Fatalf("replay acquired revocation after source and credential withdrawal: %v", err)
	}
	changedRevocation := revocation
	changedRevocation.ActorID = "other-operator@example.invalid"
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(
		ctx, changedRevocation,
	); !errors.Is(err, persistence.ErrAuditExportWorkloadIdentityRevocationConflict) {
		t.Fatalf("changed external workload identity revocation error = %v", err)
	}
	changedSource := revocation
	changedSource.Acquisition = &persistence.AuditExportRevocationAcquisition{
		Contract:                   persistence.AuditExportRevocationAcquisitionContract,
		Purpose:                    persistence.AuditExportRevocationAuthorityPurposeWorkloadIdentity,
		SourceID:                   "archive-revocations.mirror",
		SourceRegistrySHA256:       revocation.Acquisition.SourceRegistrySHA256,
		SourceGeneration:           revocation.Acquisition.SourceGeneration,
		NoticeCredentialSHA256:     revocation.Acquisition.NoticeCredentialSHA256,
		NoticeCredentialGeneration: revocation.Acquisition.NoticeCredentialGeneration,
		TrustCredentialSHA256:      revocation.Acquisition.TrustCredentialSHA256,
		TrustCredentialGeneration:  revocation.Acquisition.TrustCredentialGeneration,
	}
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(
		ctx, changedSource,
	); !errors.Is(err, persistence.ErrAuditExportWorkloadIdentityRevocationConflict) {
		t.Fatalf("changed workload revocation acquisition error = %v", err)
	}
	rotationReason := sha256.Sum256([]byte("rotate externally revoked workload issuer key"))
	rotation := persistence.AuditExportWorkloadIdentityChange{
		Contract:  persistence.AuditExportWorkloadIdentityAuthorizationContract,
		Operation: "activate", IsolationDomainID: delivery.IsolationDomainID,
		WorkloadID: "audit-export.dispatcher", Generation: 2,
		GrantContract:            "dataground.audit-export-workload-identity-grant/ed25519/v1",
		GrantSHA256:              "sha256:" + strings.Repeat("d", 64),
		Audience:                 "dataground.audit-export-transport",
		ClientCertificateSHA256:  "sha256:" + strings.Repeat("e", 64),
		AuthorityID:              "workload-issuer.primary",
		IssuerTrustProfileSHA256: "sha256:" + strings.Repeat("9", 64),
		IssuerSigningKeyID:       "issuer_key_01",
		IssuedAt:                 now.Add(-time.Minute),
		NotBefore:                now.Add(-30 * time.Second),
		ExpiresAt:                now.Add(time.Hour),
		ActorID:                  "operator@example.invalid",
		ReasonDigest:             rotationReason[:],
		CorrelationID:            "cor_00000000000000000034",
	}
	if err := repository.ChangeAuditExportWorkloadIdentity(
		ctx, rotation,
	); !errors.Is(err, persistence.ErrAuditExportWorkloadIdentityUnauthorized) {
		t.Fatalf("rotation under externally revoked issuer key error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_workload_identity_events (
			isolation_domain_id, workload_id, generation, authorization_contract, operation,
			grant_contract, grant_sha256, audience, client_certificate_sha256,
			authority_id, issuer_trust_profile_sha256, issuer_signing_key_id,
			issued_at, not_before, expires_at, actor_id, reason_digest, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
	`, rotation.IsolationDomainID, rotation.WorkloadID, rotation.Generation, rotation.Contract,
		rotation.Operation, rotation.GrantContract, rotation.GrantSHA256, rotation.Audience,
		rotation.ClientCertificateSHA256, rotation.AuthorityID,
		rotation.IssuerTrustProfileSHA256, rotation.IssuerSigningKeyID,
		rotation.IssuedAt, rotation.NotBefore, rotation.ExpiresAt, rotation.ActorID,
		rotation.ReasonDigest, "cor_00000000000000000035"); err == nil {
		t.Fatal("direct SQL activated an externally revoked workload issuer key")
	}
	attribution := auditExportDeliveryAttribution(
		"transport delivery", "cor_00000000000000000033",
	)
	if err := repository.ReserveAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
		auditExportWorkloadIdentityAuthorization(), attribution,
	); !errors.Is(err, persistence.ErrAuditExportWorkloadIdentityUnauthorized) {
		t.Fatalf("transport under externally revoked workload identity error = %v", err)
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
		t.Fatal("direct SQL transport bypassed external workload identity revocation")
	}
}

func TestFutureExternalWorkloadIdentityRevocationDoesNotBlockEarly(t *testing.T) {
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
	now := time.Now().UTC().Truncate(time.Microsecond)
	revocation := auditExportWorkloadIdentityRevocationRecord(
		delivery.IsolationDomainID, now, "cor_00000000000000000036",
	)
	activateAuditExportRevocationAuthority(
		t, ctx, repository, delivery.IsolationDomainID,
		persistence.AuditExportRevocationAuthorityPurposeWorkloadIdentity,
		revocation.RevocationAuthorityID, revocation.RevocationTrustProfileSHA256,
		revocation.RevocationSigningKeyID, "cor_00000000000000000038",
	)
	revocation.EffectiveAt = now.Add(time.Hour)
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(ctx, revocation); err != nil {
		t.Fatal(err)
	}
	if err := repository.ReserveAuditExportDeliveryTransportWithWorkloadIdentity(
		ctx, delivery, persistence.AuditExportDeliveryWorkloadTransportContract,
		auditExportWorkloadIdentityAuthorization(),
		auditExportDeliveryAttribution("transport delivery", "cor_00000000000000000037"),
	); err != nil {
		t.Fatalf("future-effective workload identity revocation blocked early: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE audit_export_workload_identity_revocations
		SET effective_at = clock_timestamp() - interval '1 second'
	`); err == nil {
		t.Fatal("future workload identity revocation was mutable")
	}
}

func TestAuditExportRevocationAuthorityGovernsNoticeIntakeAndReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	domainID := "iso_00000000000000000001"
	revocation := auditExportWorkloadIdentityRevocationRecord(
		domainID, time.Now().UTC().Truncate(time.Microsecond),
		"cor_00000000000000000032",
	)
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(
		ctx, revocation,
	); !errors.Is(err, persistence.ErrAuditExportRevocationAuthorityUnauthorized) {
		t.Fatalf("ungoverned workload revocation error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_workload_identity_revocations (
			record_contract, revocation_contract, revocation_sha256, isolation_domain_id,
			scope, workload_identity_authority_id, workload_identity_trust_profile_sha256,
			workload_identity_signing_key_id, external_reason_sha256, revocation_authority_id,
			revocation_trust_profile_sha256, revocation_signing_key_id,
			issued_at, effective_at, actor_id, reason_digest, correlation_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`, revocation.Contract, revocation.RevocationContract, revocation.RevocationSHA256,
		revocation.IsolationDomainID, revocation.Scope, revocation.WorkloadIdentityAuthorityID,
		revocation.WorkloadIdentityTrustProfileSHA256, revocation.WorkloadIdentitySigningKeyID,
		revocation.ExternalReasonSHA256, revocation.RevocationAuthorityID,
		revocation.RevocationTrustProfileSHA256, revocation.RevocationSigningKeyID,
		revocation.IssuedAt, revocation.EffectiveAt, revocation.ActorID,
		revocation.ReasonDigest, revocation.CorrelationID); err == nil {
		t.Fatal("direct SQL installed a revocation under an ungoverned authority")
	}
	activateAuditExportRevocationAuthority(
		t, ctx, repository, domainID,
		persistence.AuditExportRevocationAuthorityPurposeWorkloadIdentity,
		revocation.RevocationAuthorityID, revocation.RevocationTrustProfileSHA256,
		revocation.RevocationSigningKeyID, "cor_00000000000000000038",
	)
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(ctx, revocation); err != nil {
		t.Fatalf("record governed workload revocation: %v", err)
	}
	reasonDigest := sha256.Sum256([]byte("rotate workload revocation authority"))
	rotation := persistence.AuditExportRevocationAuthorityChange{
		Contract:  persistence.AuditExportRevocationAuthorityAuthorizationContract,
		Operation: "activate", IsolationDomainID: domainID,
		Purpose:     persistence.AuditExportRevocationAuthorityPurposeWorkloadIdentity,
		AuthorityID: revocation.RevocationAuthorityID, Generation: 2,
		TrustContract:      "dataground.audit-export-workload-identity-revocation-trust/ed25519/v1",
		TrustProfileSHA256: "sha256:" + strings.Repeat("f", 64),
		KeyIDs:             []string{"revocation_key_02"},
		ActorID:            "operator@example.invalid", ReasonDigest: reasonDigest[:],
		CorrelationID: "cor_00000000000000000039",
	}
	if err := repository.ChangeAuditExportRevocationAuthority(ctx, rotation); err != nil {
		t.Fatalf("rotate revocation authority: %v", err)
	}
	if err := repository.ChangeAuditExportRevocationAuthority(ctx, rotation); err != nil {
		t.Fatalf("replay revocation authority rotation: %v", err)
	}
	competingAuthority := rotation
	competingAuthority.AuthorityID = "competing-revocation.primary"
	competingAuthority.Generation = 1
	competingAuthority.CorrelationID = "cor_00000000000000000045"
	if err := repository.ChangeAuditExportRevocationAuthority(
		ctx, competingAuthority,
	); !errors.Is(err, persistence.ErrAuditExportRevocationAuthorityConflict) {
		t.Fatalf("competing revocation authority error = %v", err)
	}
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(ctx, revocation); err != nil {
		t.Fatalf("replay historical notice after authority rotation: %v", err)
	}
	oldProfileRevocation := revocation
	oldProfileRevocation.RevocationSHA256 = "sha256:" + strings.Repeat("d", 64)
	oldProfileRevocation.CorrelationID = "cor_00000000000000000040"
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(
		ctx, oldProfileRevocation,
	); !errors.Is(err, persistence.ErrAuditExportRevocationAuthorityUnauthorized) {
		t.Fatalf("new notice under rotated profile error = %v", err)
	}
	newProfileRevocation := oldProfileRevocation
	newProfileRevocation.RevocationSHA256 = "sha256:" + strings.Repeat("e", 64)
	newProfileRevocation.RevocationTrustProfileSHA256 = rotation.TrustProfileSHA256
	newProfileRevocation.RevocationSigningKeyID = rotation.KeyIDs[0]
	newProfileRevocation.CorrelationID = "cor_00000000000000000041"
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(
		ctx, newProfileRevocation,
	); err != nil {
		t.Fatalf("record notice under rotated authority: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_revocation_authority_keys (
			isolation_domain_id, purpose, generation, key_id
		) VALUES ($1, $2, 2, 'revocation_key_late')
	`, domainID, persistence.AuditExportRevocationAuthorityPurposeWorkloadIdentity); err == nil {
		t.Fatal("revocation authority generation accepted a late signing key")
	}
	withdrawalReason := sha256.Sum256([]byte("withdraw workload revocation authority"))
	withdrawal := rotation
	withdrawal.Operation = "revoke"
	withdrawal.Generation = 3
	withdrawal.KeyIDs = nil
	withdrawal.ReasonDigest = withdrawalReason[:]
	withdrawal.CorrelationID = "cor_00000000000000000042"
	if err := repository.ChangeAuditExportRevocationAuthority(ctx, withdrawal); err != nil {
		t.Fatalf("withdraw revocation authority: %v", err)
	}
	afterWithdrawal := newProfileRevocation
	afterWithdrawal.RevocationSHA256 = "sha256:" + strings.Repeat("0", 64)
	afterWithdrawal.CorrelationID = "cor_00000000000000000043"
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(
		ctx, afterWithdrawal,
	); !errors.Is(err, persistence.ErrAuditExportRevocationAuthorityUnauthorized) {
		t.Fatalf("new notice after authority withdrawal error = %v", err)
	}
	crossDomain := newProfileRevocation
	crossDomain.IsolationDomainID = "iso_00000000000000000002"
	crossDomain.RevocationSHA256 = "sha256:" + strings.Repeat("1", 64)
	crossDomain.CorrelationID = "cor_00000000000000000044"
	if err := repository.RecordAuditExportWorkloadIdentityRevocation(
		ctx, crossDomain,
	); !errors.Is(err, persistence.ErrAuditExportRevocationAuthorityUnauthorized) {
		t.Fatalf("cross-domain revocation authority error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE audit_export_revocation_authority_events SET actor_id = 'other'
	`); err == nil {
		t.Fatal("revocation authority event mutation was accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_export_revocation_authority_keys`); err == nil {
		t.Fatal("revocation authority key deletion was accepted")
	}
	exported, err := repository.ExportOperatorAuditRecords(ctx, domainID, "", 20)
	if err != nil {
		t.Fatalf("export revocation authority audit: %v", err)
	}
	authorityActions := make(map[string]int)
	for _, record := range exported.Records {
		if record.ResourceType == "audit-export-revocation-authority" {
			authorityActions[record.Action]++
		}
	}
	if authorityActions["audit-export-revocation-authority.activate"] != 2 ||
		authorityActions["audit-export-revocation-authority.revoke"] != 1 {
		t.Fatalf("revocation authority audit actions = %#v", authorityActions)
	}
	database, err := persistence.OpenSQL(ctx, os.Getenv("DATAGROUND_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 31); err == nil {
		t.Fatal("revocation authority evidence was discarded by schema downgrade")
	}
	if err := persistence.RequireCurrentSchema(ctx, database); err != nil {
		t.Fatalf("failed downgrade changed current schema: %v", err)
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
	revocation.Acquisition = &persistence.AuditExportRevocationAcquisition{
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
		t, ctx, repository, delivery.IsolationDomainID,
		persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
		revocation.RevocationAuthorityID, revocation.RevocationTrustProfileSHA256,
		revocation.RevocationSigningKeyID, "cor_00000000000000000022",
	)
	if err := repository.RecordAuditExportRecipientProofRevocation(
		ctx, revocation,
	); !errors.Is(err, persistence.ErrAuditExportRevocationSourceUnauthorized) {
		t.Fatalf("ungoverned recipient revocation source error = %v", err)
	}
	activateAuditExportRevocationSource(
		t, ctx, repository, revocation.IsolationDomainID, revocation.Acquisition.Purpose,
		revocation.Acquisition.SourceID, revocation.Acquisition.SourceRegistrySHA256,
		1, "cor_00000000000000000023",
	)
	if err := repository.RecordAuditExportRecipientProofRevocation(
		ctx, revocation,
	); !errors.Is(err, persistence.ErrAuditExportRevocationCredentialUnauthorized) {
		t.Fatalf("ungoverned recipient revocation credentials error = %v", err)
	}
	activateAuditExportRevocationCredentials(
		t, ctx, repository, revocation.IsolationDomainID, revocation.Acquisition,
		"cor_00000000000000000082", "cor_00000000000000000083",
	)
	if err := repository.RecordAuditExportRecipientProofRevocation(ctx, revocation); err != nil {
		t.Fatalf("record external recipient proof revocation: %v", err)
	}
	auditPage, err := repository.ExportOperatorAuditRecords(
		ctx, delivery.IsolationDomainID, "", 1000,
	)
	if err != nil {
		t.Fatalf("export acquired revocation audit: %v", err)
	}
	foundAcquisition := false
	for _, record := range auditPage.Records {
		if record.CorrelationID == revocation.CorrelationID {
			var metadata map[string]any
			if err := json.Unmarshal(record.SafeMetadata, &metadata); err != nil {
				t.Fatalf("decode acquired revocation audit metadata: %v", err)
			}
			foundAcquisition = metadata["revocationSourceId"] == "archive-revocations.primary" &&
				metadata["revocationSourceRegistrySha256"] == revocation.Acquisition.SourceRegistrySHA256 &&
				metadata["revocationSourceGeneration"] == float64(1) &&
				metadata["revocationSourceNoticeCredentialSha256"] ==
					revocation.Acquisition.NoticeCredentialSHA256 &&
				metadata["revocationSourceNoticeCredentialGeneration"] == float64(1) &&
				metadata["revocationSourceTrustCredentialSha256"] ==
					revocation.Acquisition.TrustCredentialSHA256 &&
				metadata["revocationSourceTrustCredentialGeneration"] == float64(1)
		}
	}
	if !foundAcquisition {
		t.Fatal("acquired revocation source provenance was not safely exportable")
	}
	replayed, err := repository.ReplayAuditExportRevocationAcquisition(
		ctx, persistence.AuditExportRevocationAcquisitionReplay{
			Purpose:              revocation.Acquisition.Purpose,
			IsolationDomainID:    revocation.IsolationDomainID,
			SourceID:             revocation.Acquisition.SourceID,
			SourceRegistrySHA256: revocation.Acquisition.SourceRegistrySHA256,
			ActorID:              revocation.ActorID, ReasonDigest: revocation.ReasonDigest,
			CorrelationID: revocation.CorrelationID,
		},
	)
	if err != nil || !replayed {
		t.Fatalf("observe exact acquired revocation replay = %v, %v", replayed, err)
	}
	if replayed, err := repository.ReplayAuditExportRevocationAcquisition(
		ctx, persistence.AuditExportRevocationAcquisitionReplay{
			Purpose:              revocation.Acquisition.Purpose,
			IsolationDomainID:    revocation.IsolationDomainID,
			SourceID:             "archive-revocations.mirror",
			SourceRegistrySHA256: revocation.Acquisition.SourceRegistrySHA256,
			ActorID:              revocation.ActorID, ReasonDigest: revocation.ReasonDigest,
			CorrelationID: revocation.CorrelationID,
		},
	); !errors.Is(err, persistence.ErrAuditExportRevocationAcquisitionConflict) || replayed {
		t.Fatalf("changed acquired revocation replay = %v, %v", replayed, err)
	}
	if err := repository.RecordAuditExportRecipientProofRevocation(ctx, revocation); err != nil {
		t.Fatalf("replay external recipient proof revocation: %v", err)
	}
	otherDomainDelivery := delivery
	otherDomainDelivery.IsolationDomainID = "iso_00000000000000000002"
	activateAuditExportProofingAuthority(
		t, ctx, repository, otherDomainDelivery.IsolationDomainID,
	)
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
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_revocation_acquisitions (
			contract, purpose, revocation_sha256, isolation_domain_id,
			source_id, source_registry_sha256, trust_profile_sha256, correlation_id
		) VALUES (
			'dataground.audit-export-revocation-acquisition/v1', 'recipient-proof',
			'sha256:' || repeat('9', 64), $1, 'archive-revocations.primary',
			'sha256:' || repeat('8', 64), 'sha256:' || repeat('7', 64),
			'cor_00000000000000000029'
		)
	`, delivery.IsolationDomainID); err == nil {
		t.Fatal("unbound revocation acquisition bypassed repository enforcement")
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
	activateAuditExportRevocationAuthority(
		t, ctx, repository, delivery.IsolationDomainID,
		persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
		revocation.RevocationAuthorityID, revocation.RevocationTrustProfileSHA256,
		revocation.RevocationSigningKeyID, "cor_00000000000000000022",
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
	activateAuditExportRevocationAuthority(
		t, ctx, repository, delivery.IsolationDomainID,
		persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
		revocation.RevocationAuthorityID, revocation.RevocationTrustProfileSHA256,
		revocation.RevocationSigningKeyID, "cor_00000000000000000022",
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
	activateAuditExportRevocationAuthority(
		t, ctx, repository, delivery.IsolationDomainID,
		persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
		revocation.RevocationAuthorityID, revocation.RevocationTrustProfileSHA256,
		revocation.RevocationSigningKeyID, "cor_00000000000000000022",
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

func TestAuditExportRevocationSourceGovernanceSerializesRotationAndWithdrawal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	domainID := "iso_00000000000000000001"
	purpose := persistence.AuditExportRevocationAuthorityPurposeRecipientProof
	reasonDigest := sha256.Sum256([]byte("govern audit export revocation source"))
	activation := persistence.AuditExportRevocationSourceChange{
		Contract:  persistence.AuditExportRevocationSourceAuthorizationContract,
		Operation: "activate", IsolationDomainID: domainID, Purpose: purpose,
		SourceID: "archive-revocations.primary", Generation: 1,
		SourceRegistrySHA256: "sha256:" + strings.Repeat("1", 64),
		ActorID:              "operator@example.invalid", ReasonDigest: reasonDigest[:],
		CorrelationID: "cor_00000000000000000051",
	}
	if err := repository.ChangeAuditExportRevocationSource(ctx, activation); err != nil {
		t.Fatal(err)
	}
	if err := repository.ChangeAuditExportRevocationSource(ctx, activation); err != nil {
		t.Fatalf("exact source activation replay: %v", err)
	}
	generation, err := repository.AuthorizeAuditExportRevocationSource(
		ctx, domainID, purpose, activation.SourceID, activation.SourceRegistrySHA256,
	)
	if err != nil || generation != 1 {
		t.Fatalf("active source = %d, %v", generation, err)
	}
	if _, err := repository.AuthorizeAuditExportRevocationSource(
		ctx, "iso_00000000000000000002", purpose,
		activation.SourceID, activation.SourceRegistrySHA256,
	); !errors.Is(err, persistence.ErrAuditExportRevocationSourceUnauthorized) {
		t.Fatalf("cross-domain source authorization error = %v", err)
	}
	if _, err := repository.AuthorizeAuditExportRevocationSource(
		ctx, domainID, persistence.AuditExportRevocationAuthorityPurposeWorkloadIdentity,
		activation.SourceID, activation.SourceRegistrySHA256,
	); !errors.Is(err, persistence.ErrAuditExportRevocationSourceUnauthorized) {
		t.Fatalf("cross-purpose source authorization error = %v", err)
	}
	changedReplay := activation
	changedReplay.ActorID = "other-operator@example.invalid"
	if err := repository.ChangeAuditExportRevocationSource(
		ctx, changedReplay,
	); !errors.Is(err, persistence.ErrAuditExportRevocationSourceConflict) {
		t.Fatalf("changed source replay error = %v", err)
	}
	rotation := activation
	rotation.Generation = 2
	rotation.SourceRegistrySHA256 = "sha256:" + strings.Repeat("2", 64)
	rotation.CorrelationID = "cor_00000000000000000052"
	if err := repository.ChangeAuditExportRevocationSource(ctx, rotation); err != nil {
		t.Fatalf("rotate revocation source: %v", err)
	}
	if _, err := repository.AuthorizeAuditExportRevocationSource(
		ctx, domainID, purpose, activation.SourceID, activation.SourceRegistrySHA256,
	); !errors.Is(err, persistence.ErrAuditExportRevocationSourceUnauthorized) {
		t.Fatalf("stale source authorization error = %v", err)
	}
	generation, err = repository.AuthorizeAuditExportRevocationSource(
		ctx, domainID, purpose, rotation.SourceID, rotation.SourceRegistrySHA256,
	)
	if err != nil || generation != 2 {
		t.Fatalf("rotated source = %d, %v", generation, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_export_revocation_source_events (
			authorization_contract, isolation_domain_id, purpose, source_id,
			generation, operation, source_registry_sha256, actor_id,
			reason_digest, correlation_id
		) VALUES ($1, $2, $3, $4, 3, 'activate', $5, $6, $7, $8)
	`, persistence.AuditExportRevocationSourceAuthorizationContract, domainID, purpose,
		rotation.SourceID, rotation.SourceRegistrySHA256, rotation.ActorID,
		rotation.ReasonDigest, "cor_00000000000000000053"); err == nil {
		t.Fatal("direct SQL reused the active revocation source")
	}
	withdrawal := rotation
	withdrawal.Operation = "revoke"
	withdrawal.Generation = 3
	withdrawal.CorrelationID = "cor_00000000000000000054"
	if err := repository.ChangeAuditExportRevocationSource(ctx, withdrawal); err != nil {
		t.Fatalf("withdraw revocation source: %v", err)
	}
	if _, err := repository.AuthorizeAuditExportRevocationSource(
		ctx, domainID, purpose, rotation.SourceID, rotation.SourceRegistrySHA256,
	); !errors.Is(err, persistence.ErrAuditExportRevocationSourceUnauthorized) {
		t.Fatalf("withdrawn source authorization error = %v", err)
	}
	if err := repository.ChangeAuditExportRevocationSource(ctx, activation); err != nil {
		t.Fatalf("historical source replay after withdrawal: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_export_revocation_source_events SET actor_id = 'other'`); err == nil {
		t.Fatal("revocation source mutation was accepted")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_export_revocation_source_events`); err == nil {
		t.Fatal("revocation source deletion was accepted")
	}
	exported, err := repository.ExportOperatorAuditRecords(ctx, domainID, "", 20)
	if err != nil {
		t.Fatalf("export revocation source audit: %v", err)
	}
	actions := make(map[string]int)
	for _, record := range exported.Records {
		if record.ResourceType == "audit-export-revocation-source" {
			actions[record.Action]++
		}
	}
	if actions["audit-export-revocation-source.activate"] != 2 ||
		actions["audit-export-revocation-source.revoke"] != 1 {
		t.Fatalf("revocation source audit actions = %#v", actions)
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
	activateAuditExportProofingAuthority(t, ctx, repository, delivery.IsolationDomainID)
	change := auditExportRecipientTrustChange(delivery, "activate", generation, profileDigest, correlationID)
	if err := repository.ChangeAuditExportRecipientTrust(ctx, change); err != nil {
		t.Fatalf("activate audit export recipient trust: %v", err)
	}
	if generation == 1 {
		activateAuditExportWorkloadIdentity(t, ctx, repository, delivery)
	}
}

func activateAuditExportProofingAuthority(
	t *testing.T,
	ctx context.Context,
	repository *persistence.Repository,
	isolationDomainID string,
) {
	t.Helper()
	correlationID := "cor_proofingauthority" + isolationDomainID[len(isolationDomainID)-4:]
	change := auditExportProofingAuthorityChange(
		isolationDomainID, "activate", 1, "sha256:"+strings.Repeat("6", 64),
		[]string{"proofing_key_01"}, correlationID,
	)
	if err := repository.ChangeAuditExportProofingAuthority(ctx, change); err != nil {
		t.Fatalf("activate audit export proofing authority: %v", err)
	}
}

func auditExportProofingAuthorityChange(
	isolationDomainID string,
	operation string,
	generation int64,
	profileDigest string,
	keyIDs []string,
	correlationID string,
) persistence.AuditExportProofingAuthorityChange {
	reasonDigest := sha256.Sum256([]byte(operation + " audit export proofing authority"))
	return persistence.AuditExportProofingAuthorityChange{
		Contract:  persistence.AuditExportProofingAuthorityAuthorizationContract,
		Operation: operation, IsolationDomainID: isolationDomainID,
		AuthorityID: "archive-proofing.primary", Generation: generation,
		TrustContract:      "dataground.audit-export-recipient-proofing-trust/ed25519/v1",
		TrustProfileSHA256: profileDigest, KeyIDs: keyIDs,
		ActorID: "operator@example.invalid", ReasonDigest: reasonDigest[:],
		CorrelationID: correlationID,
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

func activateAuditExportRevocationAuthority(
	t *testing.T,
	ctx context.Context,
	repository *persistence.Repository,
	isolationDomainID string,
	purpose string,
	authorityID string,
	profileDigest string,
	keyID string,
	correlationID string,
) {
	t.Helper()
	trustContract := "dataground.audit-export-recipient-revocation-trust/ed25519/v1"
	if purpose == persistence.AuditExportRevocationAuthorityPurposeWorkloadIdentity {
		trustContract = "dataground.audit-export-workload-identity-revocation-trust/ed25519/v1"
	}
	reasonDigest := sha256.Sum256([]byte("authorize audit export revocation authority"))
	change := persistence.AuditExportRevocationAuthorityChange{
		Contract:  persistence.AuditExportRevocationAuthorityAuthorizationContract,
		Operation: "activate", IsolationDomainID: isolationDomainID,
		Purpose: purpose, AuthorityID: authorityID, Generation: 1,
		TrustContract: trustContract, TrustProfileSHA256: profileDigest,
		KeyIDs: []string{keyID}, ActorID: "operator@example.invalid",
		ReasonDigest: reasonDigest[:], CorrelationID: correlationID,
	}
	if err := repository.ChangeAuditExportRevocationAuthority(ctx, change); err != nil {
		t.Fatalf("activate audit export revocation authority: %v", err)
	}
}

func activateAuditExportRevocationSource(
	t *testing.T,
	ctx context.Context,
	repository *persistence.Repository,
	isolationDomainID string,
	purpose string,
	sourceID string,
	registryDigest string,
	generation int64,
	correlationID string,
) {
	t.Helper()
	reasonDigest := sha256.Sum256([]byte("authorize audit export revocation source"))
	change := persistence.AuditExportRevocationSourceChange{
		Contract:  persistence.AuditExportRevocationSourceAuthorizationContract,
		Operation: "activate", IsolationDomainID: isolationDomainID,
		Purpose: purpose, SourceID: sourceID, Generation: generation,
		SourceRegistrySHA256: registryDigest, ActorID: "operator@example.invalid",
		ReasonDigest: reasonDigest[:], CorrelationID: correlationID,
	}
	if err := repository.ChangeAuditExportRevocationSource(ctx, change); err != nil {
		t.Fatalf("activate audit export revocation source: %v", err)
	}
}

func activateAuditExportRevocationCredentials(
	t *testing.T,
	ctx context.Context,
	repository *persistence.Repository,
	isolationDomainID string,
	acquisition *persistence.AuditExportRevocationAcquisition,
	noticeCorrelationID string,
	trustCorrelationID string,
) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for endpoint, values := range map[string]struct {
		digest        string
		generation    int64
		correlationID string
	}{
		"notice": {
			digest:        acquisition.NoticeCredentialSHA256,
			generation:    acquisition.NoticeCredentialGeneration,
			correlationID: noticeCorrelationID,
		},
		"trust": {
			digest:        acquisition.TrustCredentialSHA256,
			generation:    acquisition.TrustCredentialGeneration,
			correlationID: trustCorrelationID,
		},
	} {
		reasonDigest := sha256.Sum256([]byte("authorize " + endpoint + " acquisition credential"))
		change := persistence.AuditExportRevocationCredentialChange{
			Contract:  persistence.AuditExportRevocationCredentialAuthorizationContract,
			Operation: "activate", IsolationDomainID: isolationDomainID,
			Purpose: acquisition.Purpose, SourceID: acquisition.SourceID,
			SourceRegistrySHA256: acquisition.SourceRegistrySHA256,
			Endpoint:             endpoint, Generation: values.generation,
			CredentialSHA256: values.digest,
			ActivatedAt:      now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
			ActorID: "operator@example.invalid", ReasonDigest: reasonDigest[:],
			CorrelationID: values.correlationID,
		}
		if err := repository.ChangeAuditExportRevocationCredential(ctx, change); err != nil {
			t.Fatalf("activate %s audit export revocation credential: %v", endpoint, err)
		}
	}
}

func auditExportWorkloadIdentityRevocationRecord(
	isolationDomainID string,
	now time.Time,
	correlationID string,
) persistence.AuditExportWorkloadIdentityRevocationRecord {
	reasonDigest := sha256.Sum256([]byte("record external workload issuer revocation"))
	return persistence.AuditExportWorkloadIdentityRevocationRecord{
		Contract:                           persistence.AuditExportWorkloadIdentityRevocationRecordContract,
		RevocationContract:                 "dataground.audit-export-workload-identity-revocation/ed25519/v1",
		RevocationSHA256:                   "sha256:" + strings.Repeat("a", 64),
		IsolationDomainID:                  isolationDomainID,
		Scope:                              "key",
		WorkloadIdentityAuthorityID:        "workload-issuer.primary",
		WorkloadIdentityTrustProfileSHA256: "sha256:" + strings.Repeat("9", 64),
		WorkloadIdentitySigningKeyID:       "issuer_key_01",
		ExternalReasonSHA256:               "sha256:" + strings.Repeat("b", 64),
		RevocationAuthorityID:              "workload-revocation.primary",
		RevocationTrustProfileSHA256:       "sha256:" + strings.Repeat("c", 64),
		RevocationSigningKeyID:             "revocation_key_01",
		IssuedAt:                           now.Add(-time.Minute),
		EffectiveAt:                        now.Add(-30 * time.Second),
		ActorID:                            "operator@example.invalid",
		ReasonDigest:                       reasonDigest[:],
		CorrelationID:                      correlationID,
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
