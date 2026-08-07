package persistence

import (
	"bytes"
	"testing"
)

func TestAuditExportRevocationSourceChangeValidation(t *testing.T) {
	valid := validAuditExportRevocationSourceChange()
	if !valid.Valid() {
		t.Fatal("valid revocation source change was rejected")
	}
	for name, mutate := range map[string]func(*AuditExportRevocationSourceChange){
		"contract":   func(value *AuditExportRevocationSourceChange) { value.Contract = "other" },
		"operation":  func(value *AuditExportRevocationSourceChange) { value.Operation = "replace" },
		"domain":     func(value *AuditExportRevocationSourceChange) { value.IsolationDomainID = "other" },
		"purpose":    func(value *AuditExportRevocationSourceChange) { value.Purpose = "other" },
		"source":     func(value *AuditExportRevocationSourceChange) { value.SourceID = "Other" },
		"generation": func(value *AuditExportRevocationSourceChange) { value.Generation = 0 },
		"registry":   func(value *AuditExportRevocationSourceChange) { value.SourceRegistrySHA256 = "other" },
		"reason":     func(value *AuditExportRevocationSourceChange) { value.ReasonDigest = nil },
		"correlation": func(value *AuditExportRevocationSourceChange) {
			value.CorrelationID = "other"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.ReasonDigest = append([]byte(nil), valid.ReasonDigest...)
			mutate(&candidate)
			if candidate.Valid() {
				t.Fatal("invalid revocation source change was accepted")
			}
		})
	}
}

func TestSameAuditExportRevocationSourceChangeIncludesAttribution(t *testing.T) {
	left := validAuditExportRevocationSourceChange()
	right := left
	right.ReasonDigest = append([]byte(nil), left.ReasonDigest...)
	if !sameAuditExportRevocationSourceChange(left, right) {
		t.Fatal("exact revocation source replay was rejected")
	}
	right.ReasonDigest = bytes.Repeat([]byte{0x22}, 32)
	if sameAuditExportRevocationSourceChange(left, right) {
		t.Fatal("changed revocation source attribution was accepted")
	}
}

func validAuditExportRevocationSourceChange() AuditExportRevocationSourceChange {
	return AuditExportRevocationSourceChange{
		Contract: AuditExportRevocationSourceAuthorizationContract, Operation: "activate",
		IsolationDomainID: "iso_00000000000000000001",
		Purpose:           AuditExportRevocationAuthorityPurposeRecipientProof,
		SourceID:          "archive-revocations.primary", Generation: 1,
		SourceRegistrySHA256: "sha256:" + string(bytes.Repeat([]byte{'a'}, 64)),
		ActorID:              "operator@example.invalid", ReasonDigest: bytes.Repeat([]byte{0x11}, 32),
		CorrelationID: "cor_00000000000000000001",
	}
}
