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

func TestVerifyRecipientProofRevocationBindsScopeAndIndependentAuthority(t *testing.T) {
	fixture := newRecipientProofRevocationFixture(t, "key")
	verified, err := VerifyRecipientProofRevocationFile(
		fixture.revocationFile, fixture.trustFile, fixture.isolationDomainID, fixture.now,
	)
	if err != nil {
		t.Fatalf("verify recipient proof revocation: %v", err)
	}
	if verified.Contract != RecipientProofRevocationContract || verified.Scope != "key" ||
		verified.ProofingSigningKeyID != "proofing_key_01" ||
		verified.RevocationAuthorityID != "archive-revocation.primary" ||
		verified.EffectiveAt != fixture.revocation.Content.EffectiveAt {
		t.Fatalf("verified recipient proof revocation = %#v", verified)
	}
	if _, err := VerifyRecipientProofRevocationFile(
		fixture.revocationFile, fixture.trustFile, "iso_00000000000000000002", fixture.now,
	); err == nil {
		t.Fatal("recipient proof revocation was accepted for another isolation domain")
	}
	fixture.revocation.Content.ProofingAuthorityID = fixture.revocation.Content.RevocationAuthorityID
	content, err := canonicalJSON(fixture.revocation.Content)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256(content[:len(content)-1])
	fixture.revocation.ContentSHA256 = digestString(contentDigest)
	message, err := recipientProofRevocationSigningMessage(fixture.revocation)
	if err != nil {
		t.Fatal(err)
	}
	fixture.revocation.Signature.Signature = base64.RawURLEncoding.EncodeToString(
		ed25519.Sign(fixture.privateKey, message),
	)
	writeCanonicalPrivate(t, fixture.revocationFile, fixture.revocation)
	if _, err := VerifyRecipientProofRevocationFile(
		fixture.revocationFile, fixture.trustFile, fixture.isolationDomainID, fixture.now,
	); err == nil {
		t.Fatal("proofing authority was accepted as its own revocation authority")
	}
}

func TestVerifyRecipientProofRevocationRejectsForgedOrFutureNotice(t *testing.T) {
	fixture := newRecipientProofRevocationFixture(t, "profile")
	fixture.revocation.Content.ReasonSHA256 = "sha256:" + strings.Repeat("8", 64)
	content, err := canonicalJSON(fixture.revocation.Content)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256(content[:len(content)-1])
	fixture.revocation.ContentSHA256 = digestString(contentDigest)
	writeCanonicalPrivate(t, fixture.revocationFile, fixture.revocation)
	if _, err := VerifyRecipientProofRevocationFile(
		fixture.revocationFile, fixture.trustFile, fixture.isolationDomainID, fixture.now,
	); err == nil {
		t.Fatal("forged recipient proof revocation was accepted")
	}

	fixture = newRecipientProofRevocationFixture(t, "profile")
	fixture.revocation.Content.IssuedAt = fixture.now.Add(maximumProofClockSkew + time.Microsecond)
	content, err = canonicalJSON(fixture.revocation.Content)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest = sha256.Sum256(content[:len(content)-1])
	fixture.revocation.ContentSHA256 = digestString(contentDigest)
	message, err := recipientProofRevocationSigningMessage(fixture.revocation)
	if err != nil {
		t.Fatal(err)
	}
	fixture.revocation.Signature.Signature = base64.RawURLEncoding.EncodeToString(
		ed25519.Sign(fixture.privateKey, message),
	)
	writeCanonicalPrivate(t, fixture.revocationFile, fixture.revocation)
	if _, err := VerifyRecipientProofRevocationFile(
		fixture.revocationFile, fixture.trustFile, fixture.isolationDomainID, fixture.now,
	); err == nil {
		t.Fatal("future-issued recipient proof revocation was accepted")
	}
}

func TestRecipientProofRevocationSigningMessageHasOneTerminalNewline(t *testing.T) {
	fixture := newRecipientProofRevocationFixture(t, "profile")
	message, err := recipientProofRevocationSigningMessage(fixture.revocation)
	if err != nil {
		t.Fatal(err)
	}
	if len(message) < 2 || message[len(message)-1] != '\n' || message[len(message)-2] == '\n' {
		t.Fatalf("signing message has an invalid terminator: %q", message[len(message)-2:])
	}
}

type recipientProofRevocationFixture struct {
	isolationDomainID string
	now               time.Time
	revocation        RecipientProofRevocation
	revocationFile    string
	trustFile         string
	privateKey        ed25519.PrivateKey
}

func newRecipientProofRevocationFixture(t *testing.T, scope string) recipientProofRevocationFixture {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := RecipientRevocationTrustProfile{
		Contract: RecipientRevocationTrustContract, AuthorityID: "archive-revocation.primary",
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
	content := RecipientProofRevocationContent{
		Contract:          RecipientProofRevocationContentContract,
		IsolationDomainID: "iso_00000000000000000001", Scope: scope,
		ProofingAuthorityID:        "archive-proofing.primary",
		ProofingTrustProfileSHA256: "sha256:" + strings.Repeat("6", 64),
		ReasonSHA256:               "sha256:" + strings.Repeat("7", 64),
		RevocationAuthorityID:      trust.AuthorityID,
		IssuedAt:                   now.Add(-time.Hour), EffectiveAt: now.Add(-2 * time.Hour),
	}
	if scope == "key" {
		content.ProofingSigningKeyID = "proofing_key_01"
	}
	canonicalContent, err := canonicalJSON(content)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256(canonicalContent[:len(canonicalContent)-1])
	revocation := RecipientProofRevocation{
		Contract: RecipientProofRevocationContract, Content: content,
		ContentSHA256:                digestString(contentDigest),
		RevocationTrustProfileSHA256: digestString(trustDigest),
		Signature: RecipientProofRevocationSignature{
			Contract: RecipientProofRevocationSignatureContract, KeyID: trust.Keys[0].KeyID,
		},
	}
	message, err := recipientProofRevocationSigningMessage(revocation)
	if err != nil {
		t.Fatal(err)
	}
	revocation.Signature.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	revocationFile := filepath.Join(directory, "recipient-proof-revocation.json")
	trustFile := filepath.Join(directory, "recipient-revocation-trust.json")
	writeCanonicalPrivate(t, revocationFile, revocation)
	writeCanonicalPrivate(t, trustFile, trust)
	return recipientProofRevocationFixture{
		isolationDomainID: content.IsolationDomainID, now: now, revocation: revocation,
		revocationFile: revocationFile, trustFile: trustFile, privateKey: privateKey,
	}
}
