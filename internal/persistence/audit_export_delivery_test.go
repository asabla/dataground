package persistence

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"
)

func TestAuditExportDeliveryValidationClosesIdentityAndAttribution(t *testing.T) {
	digest := sha256.Sum256([]byte("evidence"))
	valid := AuditExportDelivery{
		Contract: AuditExportDeliveryContract, DeliveryID: "adl_00000000000000000001",
		IsolationDomainID: "iso_00000000000000000001", ExportKind: "operator",
		ExportID: "oax_00000000000000000001", EnvelopeDigest: digest[:],
		ExportSHA256:       "sha256:" + strings.Repeat("1", 64),
		TrustProfileSHA256: "sha256:" + strings.Repeat("2", 64), SigningKeyID: "audit_key_01",
		RecipientID: "archive.primary", DestinationDigest: digest[:],
	}
	if !valid.Valid() {
		t.Fatal("valid delivery was rejected")
	}
	stored := cloneAuditExportDelivery(valid)
	stored.Contract = auditExportDeliveryLegacyContract
	if !validStoredAuditExportDelivery(stored) {
		t.Fatal("legacy stored delivery was rejected")
	}
	stored.Contract = AuditExportDeliveryReceiptVerifiedContract
	if !validStoredAuditExportDelivery(stored) {
		t.Fatal("receipt-verified stored delivery was rejected")
	}
	stored.Contract = "dataground.audit-export-delivery/v7"
	if validStoredAuditExportDelivery(stored) {
		t.Fatal("unknown stored delivery contract was accepted")
	}
	for name, mutate := range map[string]func(*AuditExportDelivery){
		"wrong contract":        func(value *AuditExportDelivery) { value.Contract = "dataground.audit-export-delivery/v7" },
		"cross-kind export":     func(value *AuditExportDelivery) { value.ExportID = "aex_00000000000000000001" },
		"short envelope digest": func(value *AuditExportDelivery) { value.EnvelopeDigest = value.EnvelopeDigest[:31] },
		"invalid trust digest":  func(value *AuditExportDelivery) { value.TrustProfileSHA256 = "sha256:bad" },
		"invalid recipient":     func(value *AuditExportDelivery) { value.RecipientID = "https://archive.invalid" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneAuditExportDelivery(valid)
			mutate(&candidate)
			if candidate.Valid() {
				t.Fatal("invalid delivery was accepted")
			}
		})
	}
	attribution := AuditExportDeliveryAttribution{
		ActorID: "operator@example.invalid", ReasonDigest: digest[:],
		CorrelationID: "cor_00000000000000000001",
	}
	if !attribution.Valid() {
		t.Fatal("valid attribution was rejected")
	}
	changed := cloneAuditExportDeliveryAttribution(attribution)
	changed.ActorID = "operator\nother"
	if changed.Valid() {
		t.Fatal("control-bearing actor was accepted")
	}
	acknowledgement := AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       digest[:],
		DeliveryContract:            AuditExportDeliveryContract,
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v2",
		RecipientTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		RecipientSigningKeyID:       "archive_key_01",
		AcceptedAt:                  time.Date(2026, 8, 3, 15, 30, 0, 123000, time.UTC),
		Attribution:                 attribution,
	}
	if !acknowledgement.Valid() {
		t.Fatal("valid verified acknowledgement was rejected")
	}
	encrypted := cloneAuditExportDelivery(valid)
	encrypted.Contract = AuditExportEncryptedDeliveryContract
	encrypted.EncryptedPackageDigest = append([]byte(nil), digest[:]...)
	encrypted.RecipientTrustProfileSHA256 = "sha256:" + strings.Repeat("3", 64)
	encrypted.RecipientEncryptionKeyID = "archive_encryption_key_01"
	if !encrypted.Valid() {
		t.Fatal("valid encrypted delivery was rejected")
	}
	encryptedAcknowledgement := acknowledgement
	encryptedAcknowledgement.DeliveryContract = AuditExportEncryptedDeliveryContract
	encryptedAcknowledgement.ReceiptContract = auditExportEncryptedDeliveryReceiptContract
	encryptedAcknowledgement.RecipientTrustGeneration = 1
	if !encryptedAcknowledgement.Valid() {
		t.Fatal("valid encrypted acknowledgement was rejected")
	}
	transported := cloneAuditExportDelivery(encrypted)
	transported.Contract = AuditExportTransportedDeliveryContract
	if !transported.Valid() || !validStoredAuditExportDelivery(transported) {
		t.Fatal("valid transport-required delivery was rejected")
	}
	transportedAcknowledgement := encryptedAcknowledgement
	transportedAcknowledgement.DeliveryContract = AuditExportTransportedDeliveryContract
	transportedAcknowledgement.ReceiptContract = auditExportTransportedDeliveryReceiptContract
	if !transportedAcknowledgement.Valid() {
		t.Fatal("valid transported acknowledgement was rejected")
	}
	workload := cloneAuditExportDelivery(encrypted)
	workload.Contract = AuditExportWorkloadDeliveryContract
	if !workload.Valid() || !validStoredAuditExportDelivery(workload) {
		t.Fatal("valid workload-authorized delivery was rejected")
	}
	workloadAcknowledgement := encryptedAcknowledgement
	workloadAcknowledgement.DeliveryContract = AuditExportWorkloadDeliveryContract
	workloadAcknowledgement.ReceiptContract = auditExportWorkloadDeliveryReceiptContract
	if !workloadAcknowledgement.Valid() {
		t.Fatal("valid workload-authorized acknowledgement was rejected")
	}
	legacyAcknowledgement := acknowledgement
	legacyAcknowledgement.DeliveryContract = AuditExportDeliveryReceiptVerifiedContract
	legacyAcknowledgement.ReceiptContract = auditExportDeliveryLegacyReceiptContract
	if !legacyAcknowledgement.Valid() {
		t.Fatal("legacy receipt-verified acknowledgement was rejected")
	}
	legacyAcknowledgement.ReceiptContract = auditExportDeliveryReceiptContract
	if legacyAcknowledgement.Valid() {
		t.Fatal("mismatched delivery and receipt versions were accepted")
	}
	acknowledgement.AcceptedAt = acknowledgement.AcceptedAt.Add(time.Nanosecond)
	if acknowledgement.Valid() {
		t.Fatal("sub-microsecond acknowledgement time was accepted")
	}
}
