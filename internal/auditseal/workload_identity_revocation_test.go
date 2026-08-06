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

func TestVerifyWorkloadIdentityRevocationBindsScopeAndIndependentAuthority(t *testing.T) {
	fixture := newWorkloadIdentityRevocationFixture(t, "key")
	verified, err := VerifyWorkloadIdentityRevocationFile(
		fixture.revocationFile, fixture.trustFile, fixture.isolationDomainID, fixture.now,
	)
	if err != nil {
		t.Fatalf("verify workload identity revocation: %v", err)
	}
	if verified.Contract != WorkloadIdentityRevocationContract || verified.Scope != "key" ||
		verified.WorkloadIdentitySigningKeyID != "workload_issuer_key_01" ||
		verified.RevocationAuthorityID != "archive-revocation.primary" ||
		verified.EffectiveAt != fixture.revocation.Content.EffectiveAt {
		t.Fatalf("verified workload identity revocation = %#v", verified)
	}
	if _, err := VerifyWorkloadIdentityRevocationFile(
		fixture.revocationFile, fixture.trustFile, "iso_00000000000000000002", fixture.now,
	); err == nil {
		t.Fatal("workload identity revocation was accepted for another isolation domain")
	}
	fixture.revocation.Content.WorkloadIdentityAuthorityID = fixture.revocation.Content.RevocationAuthorityID
	content, err := canonicalJSON(fixture.revocation.Content)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256(content[:len(content)-1])
	fixture.revocation.ContentSHA256 = digestString(contentDigest)
	message, err := workloadIdentityRevocationSigningMessage(fixture.revocation)
	if err != nil {
		t.Fatal(err)
	}
	fixture.revocation.Signature.Signature = base64.RawURLEncoding.EncodeToString(
		ed25519.Sign(fixture.privateKey, message),
	)
	writeCanonicalPrivate(t, fixture.revocationFile, fixture.revocation)
	if _, err := VerifyWorkloadIdentityRevocationFile(
		fixture.revocationFile, fixture.trustFile, fixture.isolationDomainID, fixture.now,
	); err == nil {
		t.Fatal("workload identity authority was accepted as its own revocation authority")
	}
}

func TestVerifyWorkloadIdentityRevocationRejectsForgedOrFutureNotice(t *testing.T) {
	fixture := newWorkloadIdentityRevocationFixture(t, "profile")
	fixture.revocation.Content.ReasonSHA256 = "sha256:" + strings.Repeat("8", 64)
	content, err := canonicalJSON(fixture.revocation.Content)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256(content[:len(content)-1])
	fixture.revocation.ContentSHA256 = digestString(contentDigest)
	writeCanonicalPrivate(t, fixture.revocationFile, fixture.revocation)
	if _, err := VerifyWorkloadIdentityRevocationFile(
		fixture.revocationFile, fixture.trustFile, fixture.isolationDomainID, fixture.now,
	); err == nil {
		t.Fatal("forged workload identity revocation was accepted")
	}

	fixture = newWorkloadIdentityRevocationFixture(t, "profile")
	fixture.revocation.Content.IssuedAt = fixture.now.Add(maximumProofClockSkew + time.Microsecond)
	content, err = canonicalJSON(fixture.revocation.Content)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest = sha256.Sum256(content[:len(content)-1])
	fixture.revocation.ContentSHA256 = digestString(contentDigest)
	message, err := workloadIdentityRevocationSigningMessage(fixture.revocation)
	if err != nil {
		t.Fatal(err)
	}
	fixture.revocation.Signature.Signature = base64.RawURLEncoding.EncodeToString(
		ed25519.Sign(fixture.privateKey, message),
	)
	writeCanonicalPrivate(t, fixture.revocationFile, fixture.revocation)
	if _, err := VerifyWorkloadIdentityRevocationFile(
		fixture.revocationFile, fixture.trustFile, fixture.isolationDomainID, fixture.now,
	); err == nil {
		t.Fatal("future-issued workload identity revocation was accepted")
	}
}

func TestWorkloadIdentityRevocationSigningMessageHasOneTerminalNewline(t *testing.T) {
	fixture := newWorkloadIdentityRevocationFixture(t, "profile")
	message, err := workloadIdentityRevocationSigningMessage(fixture.revocation)
	if err != nil {
		t.Fatal(err)
	}
	if len(message) < 2 || message[len(message)-1] != '\n' || message[len(message)-2] == '\n' {
		t.Fatalf("signing message has an invalid terminator: %q", message[len(message)-2:])
	}
}

type workloadIdentityRevocationFixture struct {
	isolationDomainID string
	now               time.Time
	revocation        WorkloadIdentityRevocation
	revocationFile    string
	trustFile         string
	privateKey        ed25519.PrivateKey
}

func newWorkloadIdentityRevocationFixture(t *testing.T, scope string) workloadIdentityRevocationFixture {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := WorkloadIdentityRevocationTrustProfile{
		Contract: WorkloadIdentityRevocationTrustContract, AuthorityID: "archive-revocation.primary",
		Keys: []TrustedKey{{
			KeyID: "revocation_key_01", PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		}},
	}
	canonicalTrust, err := canonicalJSON(trust)
	if err != nil {
		t.Fatal(err)
	}
	trustDigest := sha256.Sum256(canonicalTrust)
	now := time.Date(2026, 8, 3, 21, 0, 0, 0, time.UTC)
	content := WorkloadIdentityRevocationContent{
		Contract:          WorkloadIdentityRevocationContentContract,
		IsolationDomainID: "iso_00000000000000000001", Scope: scope,
		WorkloadIdentityAuthorityID:        "audit-workload-issuer.primary",
		WorkloadIdentityTrustProfileSHA256: "sha256:" + strings.Repeat("6", 64),
		ReasonSHA256:                       "sha256:" + strings.Repeat("7", 64),
		RevocationAuthorityID:              trust.AuthorityID,
		IssuedAt:                           now.Add(-time.Hour), EffectiveAt: now.Add(-2 * time.Hour),
	}
	if scope == "key" {
		content.WorkloadIdentitySigningKeyID = "workload_issuer_key_01"
	}
	canonicalContent, err := canonicalJSON(content)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256(canonicalContent[:len(canonicalContent)-1])
	revocation := WorkloadIdentityRevocation{
		Contract: WorkloadIdentityRevocationContract, Content: content,
		ContentSHA256:                digestString(contentDigest),
		RevocationTrustProfileSHA256: digestString(trustDigest),
		Signature: WorkloadIdentityRevocationSignature{
			Contract: WorkloadIdentityRevocationSignatureContract, KeyID: trust.Keys[0].KeyID,
		},
	}
	message, err := workloadIdentityRevocationSigningMessage(revocation)
	if err != nil {
		t.Fatal(err)
	}
	revocation.Signature.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	revocationFile := filepath.Join(directory, "workload-identity-revocation.json")
	trustFile := filepath.Join(directory, "workload-identity-revocation-trust.json")
	writeCanonicalPrivate(t, revocationFile, revocation)
	writeCanonicalPrivate(t, trustFile, trust)
	return workloadIdentityRevocationFixture{
		isolationDomainID: content.IsolationDomainID, now: now, revocation: revocation,
		revocationFile: revocationFile, trustFile: trustFile, privateKey: privateKey,
	}
}
