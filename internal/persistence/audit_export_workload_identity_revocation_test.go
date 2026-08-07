package persistence

import (
	"strings"
	"testing"
	"time"
)

func TestAuditExportWorkloadIdentityRevocationRecordValidation(t *testing.T) {
	record := validAuditExportWorkloadIdentityRevocationRecord()
	if !record.Valid() {
		t.Fatal("valid audit export workload identity revocation was rejected")
	}
	for name, mutate := range map[string]func(*AuditExportWorkloadIdentityRevocationRecord){
		"contract": func(value *AuditExportWorkloadIdentityRevocationRecord) { value.Contract = "other" },
		"domain":   func(value *AuditExportWorkloadIdentityRevocationRecord) { value.IsolationDomainID = "other" },
		"scope":    func(value *AuditExportWorkloadIdentityRevocationRecord) { value.Scope = "recipient" },
		"profile key": func(value *AuditExportWorkloadIdentityRevocationRecord) {
			value.WorkloadIdentitySigningKeyID = "workload_issuer_key_01"
		},
		"same authority": func(value *AuditExportWorkloadIdentityRevocationRecord) {
			value.RevocationAuthorityID = value.WorkloadIdentityAuthorityID
		},
		"future format": func(value *AuditExportWorkloadIdentityRevocationRecord) {
			value.IssuedAt = value.IssuedAt.In(time.FixedZone("other", 1))
		},
		"reason": func(value *AuditExportWorkloadIdentityRevocationRecord) { value.ReasonDigest = nil },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := record
			mutate(&invalid)
			if invalid.Valid() {
				t.Fatal("invalid audit export workload identity revocation was accepted")
			}
		})
	}
	record.Scope = "key"
	record.WorkloadIdentitySigningKeyID = "workload_issuer_key_01"
	if !record.Valid() {
		t.Fatal("valid key-scoped audit export workload identity revocation was rejected")
	}
	record.Acquisition = &AuditExportRevocationAcquisition{
		Contract:             AuditExportRevocationAcquisitionContract,
		Purpose:              AuditExportRevocationAuthorityPurposeWorkloadIdentity,
		SourceID:             "workload-revocations.primary",
		SourceRegistrySHA256: "sha256:" + strings.Repeat("6", 64),
		SourceGeneration:          1,
		NoticeCredentialSHA256:    "sha256:" + strings.Repeat("7", 64),
		NoticeCredentialGeneration: 1,
		TrustCredentialSHA256:     "sha256:" + strings.Repeat("8", 64),
		TrustCredentialGeneration: 1,
	}
	if !record.Valid() {
		t.Fatal("valid workload revocation acquisition was rejected")
	}
	record.Acquisition.Purpose = AuditExportRevocationAuthorityPurposeRecipientProof
	if record.Valid() {
		t.Fatal("cross-purpose workload revocation acquisition was accepted")
	}
}

func validAuditExportWorkloadIdentityRevocationRecord() AuditExportWorkloadIdentityRevocationRecord {
	issuedAt := time.Date(2026, 8, 3, 21, 0, 0, 0, time.UTC)
	return AuditExportWorkloadIdentityRevocationRecord{
		Contract:           AuditExportWorkloadIdentityRevocationRecordContract,
		RevocationContract: auditExportWorkloadIdentityRevocationContract,
		RevocationSHA256:   "sha256:" + strings.Repeat("1", 64),
		IsolationDomainID:  "iso_00000000000000000001", Scope: "profile",
		WorkloadIdentityAuthorityID:        "audit-workload-issuer.primary",
		WorkloadIdentityTrustProfileSHA256: "sha256:" + strings.Repeat("2", 64),
		ExternalReasonSHA256:               "sha256:" + strings.Repeat("3", 64),
		RevocationAuthorityID:              "archive-revocation.primary",
		RevocationTrustProfileSHA256:       "sha256:" + strings.Repeat("4", 64),
		RevocationSigningKeyID:             "revocation_key_01",
		IssuedAt:                           issuedAt, EffectiveAt: issuedAt.Add(-time.Hour), ActorID: "operator",
		ReasonDigest:  []byte(strings.Repeat("5", 32)),
		CorrelationID: "cor_00000000000000000001",
	}
}
