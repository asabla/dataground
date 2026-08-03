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
		DeliveryContract:            persistence.AuditExportDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v2",
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
		operationCount != 2 || auditCount != 2 {
		t.Fatalf("delivery state = %q, %q, %q, %q, %v, trust generation %d; operations = %d, audits = %d",
			status, receiptContract, recipientTrustProfileSHA256, recipientSigningKeyID,
			recipientAcceptedAt, recipientTrustGeneration, operationCount, auditCount)
	}

	exported, err := repository.ExportOperatorAuditRecords(ctx, delivery.IsolationDomainID, "", 10)
	if err != nil {
		t.Fatalf("export delivery audit: %v", err)
	}
	if !exported.Complete || len(exported.Records) != 3 ||
		exported.Records[0].Action != "audit-export-recipient-trust.activate" ||
		exported.Records[1].Action != "audit-export-delivery.prepare" ||
		exported.Records[2].Action != "audit-export-delivery.acknowledge" {
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
	acknowledgementDigest := sha256.Sum256([]byte("archive receipt"))
	acknowledgement := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       acknowledgementDigest[:],
		DeliveryContract:            persistence.AuditExportDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v2",
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
	wrongKeyDigest := sha256.Sum256([]byte("wrong-key archive receipt"))
	wrongKey := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       wrongKeyDigest[:],
		DeliveryContract:            persistence.AuditExportDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v2",
		RecipientTrustProfileSHA256: profileDigest,
		RecipientSigningKeyID:       "archive_key_02",
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
		DeliveryContract:            persistence.AuditExportDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v2",
		RecipientTrustProfileSHA256: profileDigest,
		RecipientSigningKeyID:       "archive_key_01",
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
	defer func() {
		if _, cleanupErr := database.ExecContext(ctx, `
			TRUNCATE audit_export_recipient_trust_events, audit_export_deliveries CASCADE
		`); cleanupErr != nil {
			t.Errorf("clean protected recipient trust fixture: %v", cleanupErr)
		}
	}()
	if err := persistence.MigrateDownTo(ctx, database, 23); err == nil {
		t.Fatal("recipient trust evidence was discarded by schema downgrade")
	}
	if err := persistence.RequireCurrentSchema(ctx, database); err != nil {
		t.Fatalf("failed downgrade changed current schema: %v", err)
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
	acknowledgementDigest := sha256.Sum256([]byte("archive receipt"))
	acknowledgement := persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       acknowledgementDigest[:],
		DeliveryContract:            persistence.AuditExportDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v2",
		RecipientTrustProfileSHA256: profileDigest,
		RecipientSigningKeyID:       "archive_key_01",
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
	if operation == "activate" {
		keyIDs = []string{"archive_key_01"}
	}
	return persistence.AuditExportRecipientTrustChange{
		Contract:           persistence.AuditExportRecipientTrustAuthorizationContract,
		Operation:          operation,
		IsolationDomainID:  delivery.IsolationDomainID,
		RecipientID:        delivery.RecipientID,
		Generation:         generation,
		TrustContract:      "dataground.audit-export-recipient-trust/ed25519/v1",
		TrustProfileSHA256: profileDigest,
		KeyIDs:             keyIDs,
		ActorID:            "operator@example.invalid",
		ReasonDigest:       reasonDigest[:],
		CorrelationID:      correlationID,
	}
}
