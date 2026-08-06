package persistence

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAuditExportWorkloadIdentityChangeValidation(t *testing.T) {
	reasonDigest := sha256.Sum256([]byte("authorize audit export workload"))
	issuedAt := time.Date(2026, 8, 6, 9, 58, 0, 0, time.UTC)
	valid := AuditExportWorkloadIdentityChange{
		Contract: AuditExportWorkloadIdentityAuthorizationContract, Operation: "activate",
		IsolationDomainID: "iso_00000000000000000001", WorkloadID: "audit-export.dispatcher",
		Generation: 1, GrantContract: auditExportWorkloadIdentityGrantContract,
		GrantSHA256: "sha256:" + strings.Repeat("8", 64), Audience: auditExportWorkloadIdentityAudience,
		ClientCertificateSHA256:  "sha256:" + strings.Repeat("6", 64),
		AuthorityID:              "workload-issuer.primary",
		IssuerTrustProfileSHA256: "sha256:" + strings.Repeat("9", 64),
		IssuerSigningKeyID:       "issuer_key_01", IssuedAt: issuedAt,
		NotBefore: issuedAt.Add(time.Minute), ExpiresAt: issuedAt.Add(time.Hour),
		ActorID: "operator@example.invalid", ReasonDigest: reasonDigest[:],
		CorrelationID: "cor_00000000000000000001",
	}
	if !valid.Valid() {
		t.Fatal("valid workload identity activation was rejected")
	}
	for name, mutate := range map[string]func(*AuditExportWorkloadIdentityChange){
		"contract":    func(value *AuditExportWorkloadIdentityChange) { value.Contract = "v2" },
		"domain":      func(value *AuditExportWorkloadIdentityChange) { value.IsolationDomainID = "other" },
		"workload":    func(value *AuditExportWorkloadIdentityChange) { value.WorkloadID = "Bad" },
		"audience":    func(value *AuditExportWorkloadIdentityChange) { value.Audience = "other" },
		"certificate": func(value *AuditExportWorkloadIdentityChange) { value.ClientCertificateSHA256 = "sha256:bad" },
		"interval":    func(value *AuditExportWorkloadIdentityChange) { value.ExpiresAt = value.NotBefore },
		"reason":      func(value *AuditExportWorkloadIdentityChange) { value.ReasonDigest = nil },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			candidate.ReasonDigest = append([]byte(nil), valid.ReasonDigest...)
			mutate(&candidate)
			if candidate.Valid() {
				t.Fatal("invalid workload identity change was accepted")
			}
		})
	}
	revocation := valid
	revocation.Operation = "revoke"
	revocation.Generation = 2
	revocation.GrantContract = ""
	revocation.Audience = ""
	revocation.AuthorityID = ""
	revocation.IssuerTrustProfileSHA256 = ""
	revocation.IssuerSigningKeyID = ""
	revocation.IssuedAt = time.Time{}
	revocation.NotBefore = time.Time{}
	revocation.ExpiresAt = time.Time{}
	if !revocation.Valid() {
		t.Fatal("valid workload identity revocation was rejected")
	}
}

func TestOperatorAuditMetadataAcceptsWorkloadIdentityEvidence(t *testing.T) {
	for key, value := range map[string]any{
		"clientCertificateSha256":               "sha256:" + strings.Repeat("1", 64),
		"workloadId":                            "audit-export.dispatcher",
		"workloadIdentityAuthorityId":           "workload-issuer.primary",
		"workloadIdentityExpiresAt":             "2026-08-06T10:58:00Z",
		"workloadIdentityGeneration":            json.Number("1"),
		"workloadIdentityGrantSha256":           "sha256:" + strings.Repeat("2", 64),
		"workloadIdentityRevocationAuthorityId": "workload-revocation.primary",
		"workloadIdentityRevocationEffectiveAt": "2026-08-06T10:58:00Z",
		"workloadIdentityRevocationScope":       "key",
		"workloadIdentityRevocationSha256":      "sha256:" + strings.Repeat("3", 64),
		"workloadIdentitySigningKeyId":          "issuer_key_01",
		"workloadIdentityTrustProfileSha256":    "sha256:" + strings.Repeat("4", 64),
	} {
		if !validOperatorAuditMetadataField(key, value) {
			t.Fatalf("valid workload identity metadata %q was rejected", key)
		}
	}
}
