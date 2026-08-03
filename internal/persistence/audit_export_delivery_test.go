package persistence

import (
	"crypto/sha256"
	"strings"
	"testing"
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
	for name, mutate := range map[string]func(*AuditExportDelivery){
		"wrong contract":        func(value *AuditExportDelivery) { value.Contract = "dataground.audit-export-delivery/v2" },
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
}
