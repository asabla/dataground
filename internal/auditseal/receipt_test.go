package auditseal

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func TestVerifyDeliveryReceiptBindsExactDeliveryAndRecipientTrust(t *testing.T) {
	fixture := newDeliveryReceiptFixture(t, time.Date(2026, 8, 3, 15, 30, 0, 123000, time.UTC))
	verified, err := VerifyDeliveryReceiptFile(fixture.receiptFile, fixture.trustFile, fixture.delivery)
	if err != nil {
		t.Fatalf("verify delivery receipt: %v", err)
	}
	if verified.Contract != DeliveryReceiptContract || verified.SigningKeyID != fixture.keyID ||
		verified.AcceptedAt != fixture.receipt.Content.AcceptedAt ||
		verified.RecipientTrustProfileSHA256 != fixture.receipt.RecipientTrustProfileSHA256 {
		t.Fatalf("verified receipt = %#v", verified)
	}

	changed := fixture.delivery
	changed.DestinationDigest = append([]byte(nil), changed.DestinationDigest...)
	changed.DestinationDigest[0] ^= 0xff
	if _, err := VerifyDeliveryReceiptFile(fixture.receiptFile, fixture.trustFile, changed); err == nil {
		t.Fatal("receipt was accepted for a changed destination")
	}
	originalSignature := fixture.receipt.Signature.Signature
	fixture.receipt.Signature.Signature = originalSignature[:len(originalSignature)-1] + "A"
	if fixture.receipt.Signature.Signature == originalSignature {
		fixture.receipt.Signature.Signature = originalSignature[:len(originalSignature)-1] + "B"
	}
	writeCanonicalPrivate(t, fixture.receiptFile, fixture.receipt)
	if _, err := VerifyDeliveryReceiptFile(fixture.receiptFile, fixture.trustFile, fixture.delivery); err == nil {
		t.Fatal("receipt with a changed signature was accepted")
	}
	fixture.receipt.Signature.Signature = originalSignature
	writeCanonicalPrivate(t, fixture.receiptFile, fixture.receipt)

	otherPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	writeCanonicalPrivate(t, fixture.trustFile, RecipientTrustProfile{
		Contract: RecipientTrustContract, RecipientID: fixture.delivery.RecipientID,
		Keys: []TrustedKey{{KeyID: fixture.keyID, PublicKey: base64.RawURLEncoding.EncodeToString(otherPublicKey)}},
	})
	if _, err := VerifyDeliveryReceiptFile(fixture.receiptFile, fixture.trustFile, fixture.delivery); err == nil {
		t.Fatal("receipt was accepted with a substituted recipient trust profile")
	}
}

func TestVerifyDeliveryReceiptRejectsNonCanonicalAcceptanceTime(t *testing.T) {
	fixture := newDeliveryReceiptFixture(t, time.Date(2026, 8, 3, 15, 30, 0, 123456789, time.UTC))
	if _, err := VerifyDeliveryReceiptFile(fixture.receiptFile, fixture.trustFile, fixture.delivery); err == nil {
		t.Fatal("sub-microsecond recipient acceptance time was accepted")
	}
}

func TestDeliveryReceiptSigningMessageHasOneTerminalNewline(t *testing.T) {
	fixture := newDeliveryReceiptFixture(t, time.Date(2026, 8, 3, 15, 30, 0, 123000, time.UTC))
	message, err := deliveryReceiptSigningMessage(fixture.receipt)
	if err != nil {
		t.Fatal(err)
	}
	if len(message) < 2 || message[len(message)-1] != '\n' || message[len(message)-2] == '\n' {
		t.Fatalf("signing message has an invalid terminator: %q", message[len(message)-2:])
	}
}

type deliveryReceiptFixture struct {
	delivery    persistence.AuditExportDelivery
	receipt     DeliveryReceipt
	receiptFile string
	trustFile   string
	keyID       string
}

func newDeliveryReceiptFixture(t *testing.T, acceptedAt time.Time) deliveryReceiptFixture {
	t.Helper()
	directory := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	envelopeDigest := sha256.Sum256([]byte("sealed envelope"))
	destinationDigest := sha256.Sum256([]byte("archive.primary\nobject-prefix"))
	delivery := persistence.AuditExportDelivery{
		Contract: persistence.AuditExportDeliveryContract, DeliveryID: "adl_00000000000000000001",
		IsolationDomainID: "iso_00000000000000000001", ExportKind: "operator",
		ExportID: "oax_00000000000000000001", EnvelopeDigest: envelopeDigest[:],
		ExportSHA256: "sha256:" + strings.Repeat("1", 64), TrustProfileSHA256: "sha256:" + strings.Repeat("2", 64),
		SigningKeyID: "audit_key_01", RecipientID: "archive.primary", DestinationDigest: destinationDigest[:],
	}
	keyID := "archive_key_01"
	trust := RecipientTrustProfile{
		Contract: RecipientTrustContract, RecipientID: delivery.RecipientID,
		Keys: []TrustedKey{{KeyID: keyID, PublicKey: base64.RawURLEncoding.EncodeToString(publicKey)}},
	}
	canonicalTrust, err := canonicalJSON(trust)
	if err != nil {
		t.Fatal(err)
	}
	trustDigest := sha256.Sum256(canonicalTrust)
	content := DeliveryReceiptContent{
		DeliveryContract: delivery.Contract, DeliveryID: delivery.DeliveryID,
		IsolationDomainID: delivery.IsolationDomainID, ExportKind: delivery.ExportKind, ExportID: delivery.ExportID,
		EnvelopeSHA256: digestStringFromBytes(delivery.EnvelopeDigest), ExportSHA256: delivery.ExportSHA256,
		ExportTrustProfileSHA256: delivery.TrustProfileSHA256, ExportSigningKeyID: delivery.SigningKeyID,
		RecipientID: delivery.RecipientID, DestinationSHA256: digestStringFromBytes(delivery.DestinationDigest),
		AcceptedAt: acceptedAt,
	}
	canonicalContent, err := canonicalJSON(content)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest := sha256.Sum256(canonicalContent[:len(canonicalContent)-1])
	receipt := DeliveryReceipt{
		Contract: DeliveryReceiptContract, Content: content, ContentSHA256: digestString(contentDigest),
		RecipientTrustProfileSHA256: digestString(trustDigest),
		Signature:                   DeliveryReceiptSignature{Contract: DeliveryReceiptSignatureContract, KeyID: keyID},
	}
	message, err := deliveryReceiptSigningMessage(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Signature.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, message))
	receiptFile := filepath.Join(directory, "receipt.json")
	trustFile := filepath.Join(directory, "recipient-trust.json")
	writeCanonicalPrivate(t, receiptFile, receipt)
	writeCanonicalPrivate(t, trustFile, trust)
	return deliveryReceiptFixture{
		delivery: delivery, receipt: receipt, receiptFile: receiptFile, trustFile: trustFile, keyID: keyID,
	}
}
