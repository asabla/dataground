package persistence_test

import (
	"context"
	"crypto/sha256"
	"errors"
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
	prepare := auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001")
	if err := repository.PrepareAuditExportDelivery(ctx, delivery, prepare); err != nil {
		t.Fatalf("prepare delivery: %v", err)
	}
	if err := repository.PrepareAuditExportDelivery(ctx, delivery, prepare); err != nil {
		t.Fatalf("replay preparation: %v", err)
	}

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
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v1",
		RecipientTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		RecipientSigningKeyID:       "archive_key_01",
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
	var operationCount, auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT status, acknowledgement_contract, recipient_trust_profile_sha256,
		       recipient_signing_key_id, recipient_accepted_at,
		       (SELECT count(*) FROM audit_export_delivery_operations WHERE delivery_id = $1),
		       (SELECT count(*) FROM audit_records WHERE resource_type = 'audit-export-delivery' AND resource_id = $1)
		FROM audit_export_deliveries WHERE delivery_id = $1
	`, delivery.DeliveryID).Scan(
		&status, &receiptContract, &recipientTrustProfileSHA256, &recipientSigningKeyID,
		&recipientAcceptedAt, &operationCount, &auditCount,
	); err != nil {
		t.Fatalf("inspect delivery: %v", err)
	}
	if status != "acknowledged" || receiptContract != acknowledgement.ReceiptContract ||
		recipientTrustProfileSHA256 != acknowledgement.RecipientTrustProfileSHA256 ||
		recipientSigningKeyID != acknowledgement.RecipientSigningKeyID ||
		!recipientAcceptedAt.Equal(acknowledgement.AcceptedAt) || operationCount != 2 || auditCount != 2 {
		t.Fatalf("delivery state = %q, %q, %q, %q, %v; operations = %d, audits = %d",
			status, receiptContract, recipientTrustProfileSHA256, recipientSigningKeyID,
			recipientAcceptedAt, operationCount, auditCount)
	}

	exported, err := repository.ExportOperatorAuditRecords(ctx, delivery.IsolationDomainID, "", 10)
	if err != nil {
		t.Fatalf("export delivery audit: %v", err)
	}
	if !exported.Complete || len(exported.Records) != 2 ||
		exported.Records[0].Action != "audit-export-delivery.prepare" ||
		exported.Records[1].Action != "audit-export-delivery.acknowledge" {
		t.Fatalf("delivery audit export = %#v", exported)
	}
}

func TestAuditExportDeliveryTablesRejectMutation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
	prepare := auditExportDeliveryAttribution("prepare delivery", "cor_00000000000000000001")
	if err := repository.PrepareAuditExportDelivery(ctx, delivery, prepare); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE audit_export_deliveries SET recipient_id = 'archive.changed' WHERE delivery_id = $1`, delivery.DeliveryID); err == nil {
		t.Fatal("delivery identity mutation was accepted")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE audit_export_deliveries
		SET status = 'acknowledged', acknowledgement_digest = decode(repeat('11', 32), 'hex'),
		    acknowledgement_contract = 'dataground.audit-export-delivery-receipt/ed25519/v1',
		    recipient_trust_profile_sha256 = 'sha256:' || repeat('2', 64),
		    recipient_signing_key_id = 'archive_key_01',
		    recipient_accepted_at = clock_timestamp(), acknowledged_at = clock_timestamp()
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
}

func TestAuditExportDeliveryConcurrentReplayConverges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	delivery := auditExportDeliveryFixture("adl_00000000000000000001")
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
	acknowledgementDigest := sha256.Sum256([]byte("archive receipt"))
	acknowledgement := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       acknowledgementDigest[:],
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v1",
		RecipientTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		RecipientSigningKeyID:       "archive_key_01",
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
	if deliveryCount != 1 || operationCount != 2 || auditCount != 2 {
		t.Fatalf("deliveries = %d, operations = %d, audits = %d", deliveryCount, operationCount, auditCount)
	}
}

func auditExportDeliveryFixture(deliveryID string) persistence.AuditExportDelivery {
	envelopeDigest := sha256.Sum256([]byte("sealed envelope"))
	destinationDigest := sha256.Sum256([]byte("archive.primary\nobject-prefix"))
	return persistence.AuditExportDelivery{
		Contract: persistence.AuditExportDeliveryContract, DeliveryID: deliveryID,
		IsolationDomainID: "iso_00000000000000000001", ExportKind: "operator",
		ExportID: "oax_00000000000000000001", EnvelopeDigest: envelopeDigest[:],
		ExportSHA256:       "sha256:" + strings.Repeat("1", 64),
		TrustProfileSHA256: "sha256:" + strings.Repeat("2", 64), SigningKeyID: "audit_key_01",
		RecipientID: "archive.primary", DestinationDigest: destinationDigest[:],
	}
}

func auditExportDeliveryAttribution(reason string, correlationID string) persistence.AuditExportDeliveryAttribution {
	digest := sha256.Sum256([]byte(reason))
	return persistence.AuditExportDeliveryAttribution{
		ActorID: "operator@example.invalid", ReasonDigest: digest[:], CorrelationID: correlationID,
	}
}
