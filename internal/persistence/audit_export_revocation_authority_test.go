package persistence

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditExportRevocationAuthorityChangeValidation(t *testing.T) {
	change := validAuditExportRevocationAuthorityChange()
	if !change.Valid() {
		t.Fatal("valid audit export revocation authority change was rejected")
	}
	for name, mutate := range map[string]func(*AuditExportRevocationAuthorityChange){
		"contract": func(value *AuditExportRevocationAuthorityChange) { value.Contract = "other" },
		"purpose contract": func(value *AuditExportRevocationAuthorityChange) {
			value.Purpose = AuditExportRevocationAuthorityPurposeWorkloadIdentity
		},
		"domain":     func(value *AuditExportRevocationAuthorityChange) { value.IsolationDomainID = "other" },
		"authority":  func(value *AuditExportRevocationAuthorityChange) { value.AuthorityID = "Other" },
		"generation": func(value *AuditExportRevocationAuthorityChange) { value.Generation = 0 },
		"keys unsorted": func(value *AuditExportRevocationAuthorityChange) {
			value.KeyIDs = []string{"revocation_key_02", "revocation_key_01"}
		},
		"keys duplicate": func(value *AuditExportRevocationAuthorityChange) {
			value.KeyIDs = []string{"revocation_key_01", "revocation_key_01"}
		},
		"reason": func(value *AuditExportRevocationAuthorityChange) { value.ReasonDigest = nil },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := change
			mutate(&invalid)
			if invalid.Valid() {
				t.Fatal("invalid audit export revocation authority change was accepted")
			}
		})
	}
	revocation := change
	revocation.Operation = "revoke"
	revocation.Generation = 2
	revocation.KeyIDs = nil
	revocation.CorrelationID = "cor_00000000000000000002"
	if !revocation.Valid() {
		t.Fatal("valid audit export revocation authority withdrawal was rejected")
	}
}

func TestOperatorAuditMetadataAcceptsRevocationAuthorityEvidence(t *testing.T) {
	for key, value := range map[string]any{
		"revocationAuthorityPurpose":            "recipient-proof",
		"revocationAuthorityId":                 "archive-revocation.primary",
		"revocationAuthorityGeneration":         json.Number("1"),
		"revocationAuthorityTrustProfileSha256": "sha256:" + strings.Repeat("1", 64),
	} {
		if !validOperatorAuditMetadataField(key, value) {
			t.Fatalf("valid revocation authority metadata %q was rejected", key)
		}
	}
}

func validAuditExportRevocationAuthorityChange() AuditExportRevocationAuthorityChange {
	return AuditExportRevocationAuthorityChange{
		Contract:  AuditExportRevocationAuthorityAuthorizationContract,
		Operation: "activate", IsolationDomainID: "iso_00000000000000000001",
		Purpose:     AuditExportRevocationAuthorityPurposeRecipientProof,
		AuthorityID: "archive-revocation.primary", Generation: 1,
		TrustContract:      auditExportRecipientRevocationTrustContract,
		TrustProfileSHA256: "sha256:" + strings.Repeat("1", 64),
		KeyIDs:             []string{"revocation_key_01"}, ActorID: "operator@example.invalid",
		ReasonDigest:  []byte(strings.Repeat("2", 32)),
		CorrelationID: "cor_00000000000000000001",
	}
}
