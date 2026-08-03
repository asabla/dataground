package persistence

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestAuditExportRecipientTrustChangeValidation(t *testing.T) {
	reasonDigest := sha256.Sum256([]byte("activate reviewed archive trust"))
	valid := AuditExportRecipientTrustChange{
		Contract:           AuditExportRecipientTrustAuthorizationContract,
		Operation:          "activate",
		IsolationDomainID:  "iso_00000000000000000001",
		RecipientID:        "archive.primary",
		Generation:         1,
		TrustContract:      auditExportRecipientTrustProfileContract,
		TrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		KeyIDs:             []string{"archive_key_01"},
		ActorID:            "operator@example.invalid",
		ReasonDigest:       reasonDigest[:],
		CorrelationID:      "cor_00000000000000000001",
	}
	if !valid.Valid() {
		t.Fatal("valid recipient trust change was rejected")
	}
	for name, mutate := range map[string]func(*AuditExportRecipientTrustChange){
		"contract":       func(value *AuditExportRecipientTrustChange) { value.Contract = "v2" },
		"operation":      func(value *AuditExportRecipientTrustChange) { value.Operation = "delete" },
		"domain":         func(value *AuditExportRecipientTrustChange) { value.IsolationDomainID = "other" },
		"recipient":      func(value *AuditExportRecipientTrustChange) { value.RecipientID = "Archive" },
		"generation":     func(value *AuditExportRecipientTrustChange) { value.Generation = 0 },
		"trust contract": func(value *AuditExportRecipientTrustChange) { value.TrustContract = "v2" },
		"profile digest": func(value *AuditExportRecipientTrustChange) { value.TrustProfileSHA256 = "sha256:bad" },
		"keys":           func(value *AuditExportRecipientTrustChange) { value.KeyIDs = nil },
		"actor":          func(value *AuditExportRecipientTrustChange) { value.ActorID = "" },
		"reason":         func(value *AuditExportRecipientTrustChange) { value.ReasonDigest = nil },
		"correlation":    func(value *AuditExportRecipientTrustChange) { value.CorrelationID = "cor_bad" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.ReasonDigest = append([]byte(nil), valid.ReasonDigest...)
			candidate.KeyIDs = append([]string(nil), valid.KeyIDs...)
			mutate(&candidate)
			if candidate.Valid() {
				t.Fatal("invalid recipient trust change was accepted")
			}
		})
	}
	revocation := valid
	revocation.Operation = "revoke"
	revocation.Generation = 2
	revocation.KeyIDs = nil
	if !revocation.Valid() {
		t.Fatal("valid recipient trust revocation was rejected")
	}
}
