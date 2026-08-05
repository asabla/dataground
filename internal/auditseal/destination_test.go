package auditseal

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
)

func TestVerifyDeliveryDestinationFileBindsExactDelivery(t *testing.T) {
	destination := DeliveryDestination{
		Contract:          DeliveryDestinationContract,
		DeliveryID:        "adl_00000000000000000001",
		IsolationDomainID: "iso_00000000000000000001",
		RecipientID:       "archive.primary",
		TransportContract: persistence.AuditExportDeliveryTransportContract,
		Endpoint:          "http://127.0.0.1:8333", Bucket: "dataground-development",
		AddressingStyle: "path",
		ObjectKey: "audit-export-deliveries/v1/iso_00000000000000000001/" +
			"adl_00000000000000000001/" + strings.Repeat("5", 64) + ".json",
	}
	encoded, err := canonicalJSON(destination)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "destination.json")
	writePrivate(t, path, encoded)
	digest := sha256.Sum256(encoded)
	packageDigest := bytes.Repeat([]byte{0x55}, sha256.Size)
	delivery := persistence.AuditExportDelivery{
		Contract:   persistence.AuditExportTransportedDeliveryContract,
		DeliveryID: destination.DeliveryID, IsolationDomainID: destination.IsolationDomainID,
		ExportKind: "operator", ExportID: "oax_00000000000000000001",
		EnvelopeDigest: digest[:], ExportSHA256: "sha256:" + strings.Repeat("1", 64),
		TrustProfileSHA256: "sha256:" + strings.Repeat("2", 64), SigningKeyID: "audit_key_01",
		RecipientID: destination.RecipientID, DestinationDigest: digest[:],
		EncryptedPackageDigest:      packageDigest,
		RecipientTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		RecipientEncryptionKeyID:    "archive_encryption_key_01",
	}
	verified, err := VerifyDeliveryDestinationFile(path, delivery)
	if err != nil {
		t.Fatal(err)
	}
	if verified.SHA256 != digest || verified.ObjectKey != destination.ObjectKey ||
		verified.TransportContract != persistence.AuditExportDeliveryTransportContract {
		t.Fatalf("verified destination = %#v", verified)
	}
	delivery.RecipientID = "archive.secondary"
	if _, err := VerifyDeliveryDestinationFile(path, delivery); err == nil {
		t.Fatal("cross-recipient destination was accepted")
	}
	delivery.RecipientID = destination.RecipientID
	delivery.EncryptedPackageDigest = bytes.Repeat([]byte{0x66}, sha256.Size)
	if _, err := VerifyDeliveryDestinationFile(path, delivery); err == nil {
		t.Fatal("object key for a different encrypted package was accepted")
	}
}

func TestVerifyDeliveryDestinationFileBindsMTLSIdentity(t *testing.T) {
	destination := DeliveryDestination{
		Contract:          DeliveryDestinationMTLSContract,
		DeliveryID:        "adl_00000000000000000001",
		IsolationDomainID: "iso_00000000000000000001",
		RecipientID:       "archive.primary",
		TransportContract: persistence.AuditExportDeliveryMTLSTransportContract,
		Endpoint:          "https://archive.internal.example", Bucket: "dataground-audit",
		AddressingStyle: "virtual-hosted",
		ObjectKey: "audit-export-deliveries/v1/iso_00000000000000000001/" +
			"adl_00000000000000000001/" + strings.Repeat("5", 64) + ".json",
		ClientCertificateSHA256: "sha256:" + strings.Repeat("6", 64),
		ServerTrustSHA256:       "sha256:" + strings.Repeat("7", 64),
	}
	encoded, err := canonicalJSON(destination)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "destination.json")
	writePrivate(t, path, encoded)
	digest := sha256.Sum256(encoded)
	delivery := persistence.AuditExportDelivery{
		Contract:   persistence.AuditExportTransportedDeliveryContract,
		DeliveryID: destination.DeliveryID, IsolationDomainID: destination.IsolationDomainID,
		ExportKind: "operator", ExportID: "oax_00000000000000000001",
		EnvelopeDigest: digest[:], ExportSHA256: "sha256:" + strings.Repeat("1", 64),
		TrustProfileSHA256: "sha256:" + strings.Repeat("2", 64), SigningKeyID: "audit_key_01",
		RecipientID: destination.RecipientID, DestinationDigest: digest[:],
		EncryptedPackageDigest:      bytes.Repeat([]byte{0x55}, sha256.Size),
		RecipientTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		RecipientEncryptionKeyID:    "archive_encryption_key_01",
	}
	verified, err := VerifyDeliveryDestinationFile(path, delivery)
	if err != nil {
		t.Fatal(err)
	}
	if verified.TransportContract != persistence.AuditExportDeliveryMTLSTransportContract ||
		verified.ClientCertificateSHA256 != destination.ClientCertificateSHA256 ||
		verified.ServerTrustSHA256 != destination.ServerTrustSHA256 {
		t.Fatalf("verified destination = %#v", verified)
	}
	destination.ServerTrustSHA256 = ""
	invalid, err := canonicalJSON(destination)
	if err != nil {
		t.Fatal(err)
	}
	writePrivate(t, path, invalid)
	invalidDigest := sha256.Sum256(invalid)
	delivery.DestinationDigest = invalidDigest[:]
	if _, err := VerifyDeliveryDestinationFile(path, delivery); err == nil {
		t.Fatal("mTLS destination without bound server trust was accepted")
	}
}
