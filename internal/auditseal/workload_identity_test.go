package auditseal

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerifyWorkloadIdentityGrantBindsScopeAudienceAndCertificate(t *testing.T) {
	fixture := newWorkloadIdentityGrantFixture(t)
	verified, err := VerifyWorkloadIdentityGrantFile(
		fixture.grantFile, fixture.trustFile, fixture.grant.Content.IsolationDomainID,
		fixture.grant.Content.WorkloadID, fixture.grant.Content.ClientCertificateSHA256, fixture.now,
	)
	if err != nil {
		t.Fatalf("verify workload identity grant: %v", err)
	}
	if verified.Contract != WorkloadIdentityGrantContract ||
		verified.Audience != AuditExportTransportAudience ||
		verified.AuthorityID != fixture.grant.Content.AuthorityID ||
		verified.IssuerSigningKeyID != fixture.grant.Signature.KeyID ||
		verified.ExpiresAt != fixture.grant.Content.ExpiresAt {
		t.Fatalf("verified workload identity grant = %#v", verified)
	}
	if _, err := VerifyWorkloadIdentityGrantFile(
		fixture.grantFile, fixture.trustFile, fixture.grant.Content.IsolationDomainID,
		fixture.grant.Content.WorkloadID, "sha256:"+strings.Repeat("9", 64), fixture.now,
	); err == nil {
		t.Fatal("workload identity grant was accepted for another certificate")
	}
	if _, err := VerifyWorkloadIdentityGrantFile(
		fixture.grantFile, fixture.trustFile, "iso_00000000000000000002",
		fixture.grant.Content.WorkloadID, fixture.grant.Content.ClientCertificateSHA256, fixture.now,
	); err == nil {
		t.Fatal("workload identity grant was accepted for another isolation domain")
	}
	if _, err := VerifyWorkloadIdentityGrantFile(
		fixture.grantFile, fixture.trustFile, fixture.grant.Content.IsolationDomainID,
		"audit-export.other-dispatcher", fixture.grant.Content.ClientCertificateSHA256, fixture.now,
	); err == nil {
		t.Fatal("workload identity grant was accepted for another workload")
	}
}

func TestVerifyWorkloadIdentityGrantRejectsExpiredOrForgedGrant(t *testing.T) {
	fixture := newWorkloadIdentityGrantFixture(t)
	if _, err := VerifyWorkloadIdentityGrantFile(
		fixture.grantFile, fixture.trustFile, fixture.grant.Content.IsolationDomainID,
		fixture.grant.Content.WorkloadID, fixture.grant.Content.ClientCertificateSHA256,
		fixture.grant.Content.ExpiresAt,
	); err == nil {
		t.Fatal("expired workload identity grant was accepted")
	}
	fixture.grant.Content.Audience = "dataground.other-service"
	content, err := canonicalJSON(fixture.grant.Content)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content[:len(content)-1])
	fixture.grant.ContentSHA256 = digestString(digest)
	writeCanonicalPrivate(t, fixture.grantFile, fixture.grant)
	if _, err := VerifyWorkloadIdentityGrantFile(
		fixture.grantFile, fixture.trustFile, fixture.grant.Content.IsolationDomainID,
		fixture.grant.Content.WorkloadID, fixture.grant.Content.ClientCertificateSHA256, fixture.now,
	); err == nil {
		t.Fatal("forged workload identity grant was accepted")
	}
}

type workloadIdentityGrantFixture struct {
	now       time.Time
	grant     WorkloadIdentityGrant
	grantFile string
	trustFile string
}

func newWorkloadIdentityGrantFixture(t *testing.T) workloadIdentityGrantFixture {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := WorkloadIdentityTrustProfile{
		Contract: WorkloadIdentityTrustContract, AuthorityID: "workload-issuer.primary",
		Keys: []TrustedKey{{
			KeyID: "issuer_key_01", PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		}},
	}
	canonicalTrust, err := canonicalJSON(trust)
	if err != nil {
		t.Fatal(err)
	}
	trustDigest := sha256.Sum256(canonicalTrust)
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	content := WorkloadIdentityGrantContent{
		Contract:          WorkloadIdentityGrantContentContract,
		IsolationDomainID: "iso_00000000000000000001", WorkloadID: "audit-export.dispatcher",
		Audience:                AuditExportTransportAudience,
		ClientCertificateSHA256: "sha256:" + strings.Repeat("6", 64),
		AuthorityID:             trust.AuthorityID, IssuedAt: now.Add(-2 * time.Minute),
		NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	canonicalContent, err := canonicalJSON(content)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256(canonicalContent[:len(canonicalContent)-1])
	grant := WorkloadIdentityGrant{
		Contract: WorkloadIdentityGrantContract, Content: content,
		ContentSHA256: digestString(contentDigest), IssuerTrustProfileSHA256: digestString(trustDigest),
		Signature: WorkloadIdentityGrantSignature{
			Contract: WorkloadIdentityGrantSignatureContract, KeyID: trust.Keys[0].KeyID,
		},
	}
	message, err := workloadIdentityGrantSigningMessage(grant)
	if err != nil {
		t.Fatal(err)
	}
	grant.Signature.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	grantFile := filepath.Join(directory, "workload-identity-grant.json")
	trustFile := filepath.Join(directory, "workload-identity-trust.json")
	writeCanonicalPrivate(t, grantFile, grant)
	writeCanonicalPrivate(t, trustFile, trust)
	return workloadIdentityGrantFixture{now: now, grant: grant, grantFile: grantFile, trustFile: trustFile}
}
