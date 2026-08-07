package persistence

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditExportProofingAuthorityChangeValidation(t *testing.T) {
	change := validAuditExportProofingAuthorityChange()
	if !change.Valid() {
		t.Fatal("valid audit export proofing authority change was rejected")
	}
	for name, mutate := range map[string]func(*AuditExportProofingAuthorityChange){
		"contract":       func(value *AuditExportProofingAuthorityChange) { value.Contract = "other" },
		"domain":         func(value *AuditExportProofingAuthorityChange) { value.IsolationDomainID = "other" },
		"authority":      func(value *AuditExportProofingAuthorityChange) { value.AuthorityID = "Other" },
		"generation":     func(value *AuditExportProofingAuthorityChange) { value.Generation = 0 },
		"trust contract": func(value *AuditExportProofingAuthorityChange) { value.TrustContract = "other" },
		"keys unsorted": func(value *AuditExportProofingAuthorityChange) {
			value.KeyIDs = []string{"proofing_key_02", "proofing_key_01"}
		},
		"keys duplicate": func(value *AuditExportProofingAuthorityChange) {
			value.KeyIDs = []string{"proofing_key_01", "proofing_key_01"}
		},
		"reason": func(value *AuditExportProofingAuthorityChange) { value.ReasonDigest = nil },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := change
			mutate(&invalid)
			if invalid.Valid() {
				t.Fatal("invalid audit export proofing authority change was accepted")
			}
		})
	}
	revocation := change
	revocation.Operation = "revoke"
	revocation.Generation = 2
	revocation.KeyIDs = nil
	revocation.CorrelationID = "cor_00000000000000000002"
	if !revocation.Valid() {
		t.Fatal("valid audit export proofing authority withdrawal was rejected")
	}
}

func TestOperatorAuditMetadataAcceptsProofingAuthorityEvidence(t *testing.T) {
	for key, value := range map[string]any{
		"proofingAuthorityId":                 "archive-proofing.primary",
		"proofingAuthorityGeneration":         json.Number("1"),
		"proofingAuthorityTrustProfileSha256": "sha256:" + strings.Repeat("1", 64),
	} {
		if !validOperatorAuditMetadataField(key, value) {
			t.Fatalf("valid proofing authority metadata %q was rejected", key)
		}
	}
}

func validAuditExportProofingAuthorityChange() AuditExportProofingAuthorityChange {
	return AuditExportProofingAuthorityChange{
		Contract:  AuditExportProofingAuthorityAuthorizationContract,
		Operation: "activate", IsolationDomainID: "iso_00000000000000000001",
		AuthorityID: "archive-proofing.primary", Generation: 1,
		TrustContract:      auditExportRecipientProofingTrustContract,
		TrustProfileSHA256: "sha256:" + strings.Repeat("1", 64),
		KeyIDs:             []string{"proofing_key_01"}, ActorID: "operator@example.invalid",
		ReasonDigest:  []byte(strings.Repeat("2", 32)),
		CorrelationID: "cor_00000000000000000001",
	}
}
