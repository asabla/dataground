package auditseal

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptAndVerifyAuditExportPackage(t *testing.T) {
	fixture := newEncryptionFixture(t)
	if err := EncryptFile(fixture.request); err != nil {
		t.Fatalf("encrypt audit export: %v", err)
	}
	verified, err := VerifyEncryptedPackageFile(
		fixture.request.OutputFile,
		fixture.request.EnvelopeFile,
		fixture.request.ExportTrustProfileFile,
		fixture.request.RecipientTrustProfileFile,
	)
	if err != nil {
		t.Fatalf("verify encrypted package: %v", err)
	}
	encoded, err := os.ReadFile(fixture.request.OutputFile)
	if err != nil {
		t.Fatal(err)
	}
	if verified.PackageSHA256 != sha256.Sum256(encoded) ||
		verified.RecipientID != "archive.primary" ||
		verified.EncryptionKeyID != fixture.request.EncryptionKeyID ||
		verified.IsolationDomainID != "iso_00000000000000000001" {
		t.Fatalf("unexpected encrypted package evidence: %#v", verified)
	}
	plaintext := decryptPackage(t, encoded, fixture.recipientPrivateKey)
	want, err := os.ReadFile(fixture.request.EnvelopeFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plaintext, want) {
		t.Fatal("decrypted package does not contain the exact signed envelope")
	}
	clear(plaintext)
}

func TestEncryptRejectsLegacyTrustAndSubstitution(t *testing.T) {
	fixture := newEncryptionFixture(t)
	legacyPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	writeCanonicalPrivate(t, fixture.request.RecipientTrustProfileFile, RecipientTrustProfile{
		Contract: RecipientTrustContract, RecipientID: "archive.primary",
		Keys: []TrustedKey{{KeyID: "archive_key_01", PublicKey: base64.RawURLEncoding.EncodeToString(legacyPublic)}},
	})
	if err := EncryptFile(fixture.request); err == nil {
		t.Fatal("legacy signing-only recipient trust was accepted for encryption")
	}

	fixture = newEncryptionFixture(t)
	fixture.request.EncryptionKeyID = "missing_key"
	if err := EncryptFile(fixture.request); err == nil {
		t.Fatal("untrusted recipient encryption key was accepted")
	}

	fixture = newEncryptionFixture(t)
	fixture.request.OutputFile = fixture.request.EnvelopeFile
	if err := EncryptFile(fixture.request); err == nil {
		t.Fatal("encryption path collision was accepted")
	}
}

func TestEncryptDoesNotReplaceExistingPackage(t *testing.T) {
	fixture := newEncryptionFixture(t)
	if err := EncryptFile(fixture.request); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(fixture.request.OutputFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := EncryptFile(fixture.request); err == nil {
		t.Fatal("existing encrypted package was replaced")
	}
	after, err := os.ReadFile(fixture.request.OutputFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed encryption replay changed the installed package")
	}
	if _, err := VerifyEncryptedPackageFile(
		fixture.request.OutputFile,
		fixture.request.EnvelopeFile,
		fixture.request.ExportTrustProfileFile,
		fixture.request.RecipientTrustProfileFile,
	); err != nil {
		t.Fatalf("verify package after failed replay: %v", err)
	}
}

func TestRecipientRejectsTamperedEncryptedPackage(t *testing.T) {
	fixture := newEncryptionFixture(t)
	if err := EncryptFile(fixture.request); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(fixture.request.OutputFile)
	if err != nil {
		t.Fatal(err)
	}
	var document EncryptedPackage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(document.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[0] ^= 1
	document.Ciphertext = base64.RawURLEncoding.EncodeToString(ciphertext)
	clear(ciphertext)
	tampered, err := canonicalJSON(document)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(tampered)
	if plaintext := decryptPackage(t, tampered, fixture.recipientPrivateKey); plaintext != nil {
		clear(plaintext)
		t.Fatal("tampered ciphertext decrypted successfully")
	}
}

type encryptionFixture struct {
	request             EncryptRequest
	recipientPrivateKey *ecdh.PrivateKey
}

func newEncryptionFixture(t *testing.T) encryptionFixture {
	t.Helper()
	seal := newSealFixture(t, AuthorizationExportKind)
	if err := PrepareSigningMessage(PrepareRequest{
		ExportFile: seal.exportFile, TrustProfileFile: seal.trustFile, SigningMessageFile: seal.messageFile,
	}); err != nil {
		t.Fatal(err)
	}
	seal.sign(t)
	if err := Install(InstallRequest{
		ExportFile: seal.exportFile, SignatureFile: seal.signatureFile,
		TrustProfileFile: seal.trustFile, OutputFile: seal.envelopeFile,
	}); err != nil {
		t.Fatal(err)
	}
	recipientPrivateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signingPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recipientTrustFile := filepath.Join(seal.directory, "recipient-trust.json")
	writeCanonicalPrivate(t, recipientTrustFile, RecipientTrustProfile{
		Contract: RecipientEncryptionTrustContract, RecipientID: "archive.primary",
		SigningKeys: []TrustedKey{{
			KeyID: "archive_signing_key_01", PublicKey: base64.RawURLEncoding.EncodeToString(signingPublic),
		}},
		EncryptionKeys: []TrustedKey{{
			KeyID:     "archive_encryption_key_01",
			PublicKey: base64.RawURLEncoding.EncodeToString(recipientPrivateKey.PublicKey().Bytes()),
		}},
	})
	return encryptionFixture{
		request: EncryptRequest{
			EnvelopeFile: seal.envelopeFile, ExportTrustProfileFile: seal.trustFile,
			RecipientTrustProfileFile: recipientTrustFile, EncryptionKeyID: "archive_encryption_key_01",
			OutputFile: filepath.Join(seal.directory, "encrypted-package.json"),
		},
		recipientPrivateKey: recipientPrivateKey,
	}
}

func decryptPackage(t *testing.T, encoded []byte, recipientPrivateKey *ecdh.PrivateKey) []byte {
	t.Helper()
	var document EncryptedPackage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	ephemeralPublicKeyBytes, err := base64.RawURLEncoding.DecodeString(document.EphemeralPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(ephemeralPublicKeyBytes)
	ephemeralPublicKey, err := ecdh.X25519().NewPublicKey(ephemeralPublicKeyBytes)
	if err != nil {
		t.Fatal(err)
	}
	sharedSecret, err := recipientPrivateKey.ECDH(ephemeralPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(sharedSecret)
	header := encryptedPackageHeader{
		Contract: document.Contract, EnvelopeSHA256: document.EnvelopeSHA256,
		ExportKind: document.ExportKind, ExportID: document.ExportID,
		IsolationDomainID: document.IsolationDomainID, RecipientID: document.RecipientID,
		RecipientTrustProfileSHA256: document.RecipientTrustProfileSHA256,
		EncryptionKeyID:             document.EncryptionKeyID, EphemeralPublicKey: document.EphemeralPublicKey,
		Nonce: document.Nonce,
	}
	aad, err := canonicalJSON(header)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(aad)
	key, err := deriveEncryptionKey(sharedSecret, sha256.Sum256(aad))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(document.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(nonce)
	ciphertext, err := base64.RawURLEncoding.DecodeString(document.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(ciphertext)
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil
	}
	return plaintext
}
