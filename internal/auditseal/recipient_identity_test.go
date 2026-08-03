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

func TestVerifyRecipientIdentityProofBindsScopeTrustAndAuthority(t *testing.T) {
	fixture := newRecipientIdentityProofFixture(t)
	verified, err := VerifyRecipientIdentityProofFile(
		fixture.proofFile,
		fixture.proofingTrustFile,
		fixture.recipientTrust,
		fixture.isolationDomainID,
		fixture.now,
	)
	if err != nil {
		t.Fatalf("verify recipient identity proof: %v", err)
	}
	if verified.Contract != RecipientIdentityProofContract ||
		verified.RecipientTrustProfileSHA256 != fixture.recipientTrust.SHA256 ||
		verified.AuthorityID != fixture.proof.Content.AuthorityID ||
		verified.SigningKeyID != fixture.proof.Signature.KeyID ||
		verified.ExpiresAt != fixture.proof.Content.ExpiresAt {
		t.Fatalf("verified recipient identity proof = %#v", verified)
	}

	changedTrust := fixture.recipientTrust
	changedTrust.SHA256 = "sha256:" + strings.Repeat("9", 64)
	if _, err := VerifyRecipientIdentityProofFile(
		fixture.proofFile, fixture.proofingTrustFile, changedTrust,
		fixture.isolationDomainID, fixture.now,
	); err == nil {
		t.Fatal("recipient identity proof was accepted for a changed recipient trust profile")
	}
	if _, err := VerifyRecipientIdentityProofFile(
		fixture.proofFile, fixture.proofingTrustFile, fixture.recipientTrust,
		"iso_00000000000000000002", fixture.now,
	); err == nil {
		t.Fatal("recipient identity proof was accepted for another isolation domain")
	}
}

func TestVerifyRecipientIdentityProofRejectsExpiredOrForgedEvidence(t *testing.T) {
	fixture := newRecipientIdentityProofFixture(t)
	if _, err := VerifyRecipientIdentityProofFile(
		fixture.proofFile, fixture.proofingTrustFile, fixture.recipientTrust,
		fixture.isolationDomainID, fixture.proof.Content.ExpiresAt,
	); err == nil {
		t.Fatal("expired recipient identity proof was accepted")
	}

	fixture.proof.Content.EvidenceSHA256 = "sha256:" + strings.Repeat("8", 64)
	content, err := canonicalJSON(fixture.proof.Content)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256(content[:len(content)-1])
	fixture.proof.ContentSHA256 = digestString(contentDigest)
	writeCanonicalPrivate(t, fixture.proofFile, fixture.proof)
	if _, err := VerifyRecipientIdentityProofFile(
		fixture.proofFile, fixture.proofingTrustFile, fixture.recipientTrust,
		fixture.isolationDomainID, fixture.now,
	); err == nil {
		t.Fatal("recipient identity proof with a forged evidence binding was accepted")
	}
}

func TestRecipientIdentityProofSigningMessageHasOneTerminalNewline(t *testing.T) {
	fixture := newRecipientIdentityProofFixture(t)
	message, err := recipientIdentityProofSigningMessage(fixture.proof)
	if err != nil {
		t.Fatal(err)
	}
	if len(message) < 2 || message[len(message)-1] != '\n' || message[len(message)-2] == '\n' {
		t.Fatalf("signing message has an invalid terminator: %q", message[len(message)-2:])
	}
}

type recipientIdentityProofFixture struct {
	isolationDomainID string
	now               time.Time
	recipientTrust    RecipientTrustEvidence
	proof             RecipientIdentityProof
	proofFile         string
	proofingTrustFile string
}

func newRecipientIdentityProofFixture(t *testing.T) recipientIdentityProofFixture {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := RecipientProofingTrustProfile{
		Contract: RecipientProofingTrustContract, AuthorityID: "archive-proofing.primary",
		Keys: []TrustedKey{{
			KeyID: "proofing_key_01", PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		}},
	}
	canonicalTrust, err := canonicalJSON(trust)
	if err != nil {
		t.Fatal(err)
	}
	trustDigest := sha256.Sum256(canonicalTrust)
	now := time.Date(2026, 8, 3, 20, 0, 0, 0, time.UTC)
	recipientTrust := RecipientTrustEvidence{
		Contract: RecipientTrustContract, RecipientID: "archive.primary",
		SHA256: "sha256:" + strings.Repeat("3", 64), KeyIDs: []string{"archive_key_01"},
	}
	content := RecipientIdentityProofContent{
		Contract:          RecipientIdentityProofContentContract,
		IsolationDomainID: "iso_00000000000000000001", RecipientID: recipientTrust.RecipientID,
		RecipientTrustProfileSHA256: recipientTrust.SHA256,
		EvidenceSHA256:              "sha256:" + strings.Repeat("4", 64),
		AuthorityID:                 trust.AuthorityID,
		VerifiedAt:                  now.Add(-time.Hour),
		ExpiresAt:                   now.Add(24 * time.Hour),
	}
	canonicalContent, err := canonicalJSON(content)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256(canonicalContent[:len(canonicalContent)-1])
	proof := RecipientIdentityProof{
		Contract: RecipientIdentityProofContract, Content: content,
		ContentSHA256:              digestString(contentDigest),
		ProofingTrustProfileSHA256: digestString(trustDigest),
		Signature: RecipientIdentityProofSignature{
			Contract: RecipientIdentityProofSignatureContract, KeyID: trust.Keys[0].KeyID,
		},
	}
	message, err := recipientIdentityProofSigningMessage(proof)
	if err != nil {
		t.Fatal(err)
	}
	proof.Signature.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	proofFile := filepath.Join(directory, "recipient-identity-proof.json")
	proofingTrustFile := filepath.Join(directory, "recipient-proofing-trust.json")
	writeCanonicalPrivate(t, proofFile, proof)
	writeCanonicalPrivate(t, proofingTrustFile, trust)
	return recipientIdentityProofFixture{
		isolationDomainID: content.IsolationDomainID, now: now, recipientTrust: recipientTrust,
		proof: proof, proofFile: proofFile, proofingTrustFile: proofingTrustFile,
	}
}
