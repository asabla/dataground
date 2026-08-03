package persistence

import (
	"strings"
	"testing"
	"time"
)

func TestAuditExportRecipientProofRevocationRecordValidation(t *testing.T) {
	record := validAuditExportRecipientProofRevocationRecord()
	if !record.Valid() {
		t.Fatal("valid audit export recipient proof revocation was rejected")
	}
	for name, mutate := range map[string]func(*AuditExportRecipientProofRevocationRecord){
		"contract":    func(value *AuditExportRecipientProofRevocationRecord) { value.Contract = "other" },
		"domain":      func(value *AuditExportRecipientProofRevocationRecord) { value.IsolationDomainID = "other" },
		"scope":       func(value *AuditExportRecipientProofRevocationRecord) { value.Scope = "recipient" },
		"profile key": func(value *AuditExportRecipientProofRevocationRecord) { value.ProofingSigningKeyID = "proofing_key_01" },
		"same authority": func(value *AuditExportRecipientProofRevocationRecord) {
			value.RevocationAuthorityID = value.ProofingAuthorityID
		},
		"future format": func(value *AuditExportRecipientProofRevocationRecord) {
			value.IssuedAt = value.IssuedAt.In(time.FixedZone("other", 1))
		},
		"reason": func(value *AuditExportRecipientProofRevocationRecord) { value.ReasonDigest = nil },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := record
			mutate(&invalid)
			if invalid.Valid() {
				t.Fatal("invalid audit export recipient proof revocation was accepted")
			}
		})
	}
	record.Scope = "key"
	record.ProofingSigningKeyID = "proofing_key_01"
	if !record.Valid() {
		t.Fatal("valid key-scoped audit export recipient proof revocation was rejected")
	}
}

func validAuditExportRecipientProofRevocationRecord() AuditExportRecipientProofRevocationRecord {
	issuedAt := time.Date(2026, 8, 3, 21, 0, 0, 0, time.UTC)
	return AuditExportRecipientProofRevocationRecord{
		Contract:           AuditExportRecipientProofRevocationRecordContract,
		RevocationContract: auditExportRecipientProofRevocationContract,
		RevocationSHA256:   "sha256:" + strings.Repeat("1", 64),
		IsolationDomainID:  "iso_00000000000000000001", Scope: "profile",
		ProofingAuthorityID:          "archive-proofing.primary",
		ProofingTrustProfileSHA256:   "sha256:" + strings.Repeat("2", 64),
		ExternalReasonSHA256:         "sha256:" + strings.Repeat("3", 64),
		RevocationAuthorityID:        "archive-revocation.primary",
		RevocationTrustProfileSHA256: "sha256:" + strings.Repeat("4", 64),
		RevocationSigningKeyID:       "revocation_key_01",
		IssuedAt:                     issuedAt, EffectiveAt: issuedAt.Add(-time.Hour), ActorID: "operator",
		ReasonDigest:  []byte(strings.Repeat("5", 32)),
		CorrelationID: "cor_00000000000000000001",
	}
}
