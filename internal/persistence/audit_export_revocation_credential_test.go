package persistence

import (
	"bytes"
	"testing"
	"time"
)

func TestAuditExportRevocationCredentialChangeValidation(t *testing.T) {
	valid := validAuditExportRevocationCredentialChange()
	if !valid.Valid() {
		t.Fatal("valid revocation credential change was rejected")
	}
	for name, mutate := range map[string]func(*AuditExportRevocationCredentialChange){
		"contract": func(value *AuditExportRevocationCredentialChange) { value.Contract = "other" },
		"operation": func(value *AuditExportRevocationCredentialChange) { value.Operation = "replace" },
		"domain": func(value *AuditExportRevocationCredentialChange) { value.IsolationDomainID = "other" },
		"purpose": func(value *AuditExportRevocationCredentialChange) { value.Purpose = "other" },
		"source": func(value *AuditExportRevocationCredentialChange) { value.SourceID = "Other" },
		"registry": func(value *AuditExportRevocationCredentialChange) { value.SourceRegistrySHA256 = "other" },
		"endpoint": func(value *AuditExportRevocationCredentialChange) { value.Endpoint = "other" },
		"generation": func(value *AuditExportRevocationCredentialChange) { value.Generation = 0 },
		"credential": func(value *AuditExportRevocationCredentialChange) { value.CredentialSHA256 = "other" },
		"expired": func(value *AuditExportRevocationCredentialChange) { value.ExpiresAt = value.ActivatedAt },
		"lifetime": func(value *AuditExportRevocationCredentialChange) {
			value.ExpiresAt = value.ActivatedAt.Add(25 * time.Hour)
		},
		"reason": func(value *AuditExportRevocationCredentialChange) { value.ReasonDigest = nil },
		"correlation": func(value *AuditExportRevocationCredentialChange) {
			value.CorrelationID = "other"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.ReasonDigest = append([]byte(nil), valid.ReasonDigest...)
			mutate(&candidate)
			if candidate.Valid() {
				t.Fatal("invalid revocation credential change was accepted")
			}
		})
	}
}

func TestAuditExportRevocationCredentialRevocationHasNoValidityWindow(t *testing.T) {
	change := validAuditExportRevocationCredentialChange()
	change.Operation = "revoke"
	change.ActivatedAt = time.Time{}
	change.ExpiresAt = time.Time{}
	if !change.Valid() {
		t.Fatal("valid revocation credential withdrawal was rejected")
	}
	change.ExpiresAt = time.Now().UTC()
	if change.Valid() {
		t.Fatal("revocation credential withdrawal retained a validity window")
	}
}

func TestSameAuditExportRevocationCredentialChangeIncludesAttribution(t *testing.T) {
	left := validAuditExportRevocationCredentialChange()
	right := left
	right.ReasonDigest = append([]byte(nil), left.ReasonDigest...)
	if !sameAuditExportRevocationCredentialChange(left, right) {
		t.Fatal("exact revocation credential replay was rejected")
	}
	right.ReasonDigest = bytes.Repeat([]byte{0x22}, 32)
	if sameAuditExportRevocationCredentialChange(left, right) {
		t.Fatal("changed revocation credential attribution was accepted")
	}
}

func validAuditExportRevocationCredentialChange() AuditExportRevocationCredentialChange {
	activatedAt := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	return AuditExportRevocationCredentialChange{
		Contract: AuditExportRevocationCredentialAuthorizationContract,
		Operation: "activate", IsolationDomainID: "iso_00000000000000000001",
		Purpose: AuditExportRevocationAuthorityPurposeRecipientProof,
		SourceID: "archive-revocations.primary",
		SourceRegistrySHA256: "sha256:" + string(bytes.Repeat([]byte{'a'}, 64)),
		Endpoint: "notice", Generation: 1,
		CredentialSHA256: "sha256:" + string(bytes.Repeat([]byte{'b'}, 64)),
		ActivatedAt: activatedAt, ExpiresAt: activatedAt.Add(time.Hour),
		ActorID: "operator@example.invalid", ReasonDigest: bytes.Repeat([]byte{0x11}, 32),
		CorrelationID: "cor_00000000000000000001",
	}
}
