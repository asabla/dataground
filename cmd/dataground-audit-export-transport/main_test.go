package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/auditseal"
	"github.com/asabla/dataground/internal/audittransport"
	"github.com/asabla/dataground/internal/persistence"
)

func TestParseArgumentsRequiresDistinctCompleteEvidence(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	base := []string{
		"-delivery-contract", persistence.AuditExportTransportedDeliveryContract,
		"-delivery-id", "adl_00000000000000000001",
		"-isolation-domain", "iso_00000000000000000001",
		"-envelope-file", "/run/dataground/audit/envelope.json",
		"-encrypted-file", "/run/dataground/audit/encrypted.json",
		"-trust-file", "/run/dataground/audit/trust.json",
		"-recipient-trust-file", "/run/dataground/audit/recipient-trust.json",
		"-recipient", "archive.primary",
		"-destination-file", "/run/dataground/audit/destination.json",
		"-destination-sha256", digest,
		"-actor", "operator@example.invalid",
		"-reason", "transport encrypted export",
		"-correlation-id", "cor_00000000000000000001",
		"-allow-loopback-http",
	}
	if _, err := parseArguments(base); err != nil {
		t.Fatal(err)
	}
	for name, arguments := range map[string][]string{
		"missing destination": base[:len(base)-9],
		"invalid digest":      append(append([]string{}, base...), "-destination-sha256", "sha256:bad"),
		"colliding files":     append(append([]string{}, base...), "-destination-file", "/run/dataground/audit/encrypted.json"),
		"control reason":      append(append([]string{}, base...), "-reason", "line one\nline two"),
		"partial mTLS":        append(append([]string{}, base...), "-client-certificate-file", "/run/dataground/audit/client.pem"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseArguments(arguments); err == nil {
				t.Fatal("invalid arguments were accepted")
			}
		})
	}
}

func TestParseArgumentsRequiresMTLSForWorkloadDelivery(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	base := []string{
		"-delivery-id", "adl_00000000000000000001",
		"-isolation-domain", "iso_00000000000000000001",
		"-envelope-file", "/run/dataground/audit/envelope.json",
		"-encrypted-file", "/run/dataground/audit/encrypted.json",
		"-trust-file", "/run/dataground/audit/trust.json",
		"-recipient-trust-file", "/run/dataground/audit/recipient-trust.json",
		"-recipient", "archive.primary",
		"-destination-file", "/run/dataground/audit/destination.json",
		"-destination-sha256", digest,
		"-actor", "operator@example.invalid",
		"-reason", "transport encrypted export",
		"-correlation-id", "cor_00000000000000000001",
		"-workload-identity-grant-file", "/run/dataground/audit/workload-grant.json",
		"-workload-identity-trust-file", "/run/dataground/audit/workload-trust.json",
	}
	if _, err := parseArguments(base); err == nil {
		t.Fatal("workload delivery without mTLS evidence was accepted")
	}
	complete := append(append([]string{}, base...),
		"-client-certificate-file", "/run/dataground/audit/client.pem",
		"-client-private-key-file", "/run/dataground/audit/client-key.pem",
		"-server-trust-bundle-file", "/run/dataground/audit/server-ca.pem",
	)
	if _, err := parseArguments(complete); err != nil {
		t.Fatalf("complete workload delivery arguments: %v", err)
	}
	if _, err := parseArguments(append(append([]string{}, complete...), "-allow-loopback-http")); err == nil {
		t.Fatal("workload delivery accepted loopback HTTP downgrade")
	}
}

func TestExecuteTransportCompletesOnlyAfterExactReadBack(t *testing.T) {
	content := []byte("encrypted package")
	digest := sha256.Sum256(content)
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "encrypted.json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	delivery := persistence.AuditExportDelivery{
		Contract:          persistence.AuditExportTransportedDeliveryContract,
		DeliveryID:        "adl_00000000000000000001",
		IsolationDomainID: "iso_00000000000000000001",
		ExportKind:        "operator", ExportID: "oax_00000000000000000001",
		EnvelopeDigest: digest[:], ExportSHA256: "sha256:" + strings.Repeat("1", 64),
		TrustProfileSHA256: "sha256:" + strings.Repeat("2", 64), SigningKeyID: "audit_key_01",
		RecipientID: "archive.primary", DestinationDigest: digest[:],
		EncryptedPackageDigest:      digest[:],
		RecipientTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		RecipientEncryptionKeyID:    "archive_encryption_key_01",
	}
	attribution := persistence.AuditExportDeliveryAttribution{
		ActorID: "operator@example.invalid", ReasonDigest: digest[:],
		CorrelationID: "cor_00000000000000000001",
	}
	repository := &transportRepositoryStub{}
	store := &transportObjectStoreStub{}
	if err := executeTransport(
		context.Background(), repository, store, delivery,
		persistence.AuditExportDeliveryTransportContract,
		persistence.AuditExportWorkloadIdentityAuthorization{}, attribution, path,
		"audit-export-deliveries/v1/iso_00000000000000000001/"+
			"adl_00000000000000000001/"+strings.Repeat("5", 64)+".json",
	); err != nil {
		t.Fatal(err)
	}
	if repository.reserved != 1 || repository.completed != 1 || store.puts != 1 || store.opens != 1 {
		t.Fatalf("repository = %#v, store = %#v", repository, store)
	}
	store.openErr = audittransport.ErrObjectUnavailable
	repository.completed = 0
	if err := executeTransport(
		context.Background(), repository, store, delivery,
		persistence.AuditExportDeliveryTransportContract,
		persistence.AuditExportWorkloadIdentityAuthorization{}, attribution, path, "object",
	); !errors.Is(err, audittransport.ErrObjectUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if repository.completed != 0 {
		t.Fatal("unverified transport was completed")
	}
}

func TestHTTPStyleLoopbackRequiresLiteralHTTPAddress(t *testing.T) {
	for endpoint, expected := range map[string]bool{
		"http://127.0.0.1:8333": true,
		"http://[::1]:8333":     true,
		"https://127.0.0.1":     false,
		"http://localhost:8333": false,
		"http://10.0.0.1:8333":  false,
	} {
		if observed := isHTTPStyleLoopback(endpoint); observed != expected {
			t.Fatalf("isHTTPStyleLoopback(%q) = %t, want %t", endpoint, observed, expected)
		}
	}
}

func TestTransportProfilesRejectCredentialDowngrade(t *testing.T) {
	loopback := auditseal.VerifiedDeliveryDestination{
		TransportContract: persistence.AuditExportDeliveryTransportContract,
		Endpoint:          "http://127.0.0.1:8333",
		AddressingStyle:   "path",
	}
	transport, allowHTTP, err := newHTTPTransport(
		commandRequest{allowLoopbackHTTP: true}, loopback,
	)
	if err != nil {
		t.Fatal(err)
	}
	transport.CloseIdleConnections()
	if !allowHTTP {
		t.Fatal("loopback development profile did not preserve explicit HTTP permission")
	}
	if _, _, err := newHTTPTransport(commandRequest{
		allowLoopbackHTTP:     true,
		clientCertificateFile: "/run/dataground/audit/client.pem",
	}, loopback); err == nil {
		t.Fatal("loopback profile accepted mTLS identity material")
	}
	mtls := auditseal.VerifiedDeliveryDestination{
		TransportContract: persistence.AuditExportDeliveryMTLSTransportContract,
		Endpoint:          "https://archive.internal.example",
		AddressingStyle:   "virtual-hosted",
	}
	if _, _, err := newHTTPTransport(commandRequest{allowLoopbackHTTP: true}, mtls); err == nil {
		t.Fatal("mTLS profile accepted loopback downgrade permission")
	}
}

type transportRepositoryStub struct {
	reserved  int
	completed int
}

func (repository *transportRepositoryStub) ReserveAuditExportDeliveryTransportWithWorkloadIdentity(
	context.Context,
	persistence.AuditExportDelivery,
	string,
	persistence.AuditExportWorkloadIdentityAuthorization,
	persistence.AuditExportDeliveryAttribution,
) error {
	repository.reserved++
	return nil
}

func (repository *transportRepositoryStub) CompleteAuditExportDeliveryTransportWithWorkloadIdentity(
	context.Context,
	persistence.AuditExportDelivery,
	string,
	persistence.AuditExportWorkloadIdentityAuthorization,
	persistence.AuditExportDeliveryAttribution,
) error {
	repository.completed++
	return nil
}

type transportObjectStoreStub struct {
	content []byte
	openErr error
	puts    int
	opens   int
}

func (store *transportObjectStoreStub) PutAuditExportObjectIfAbsent(
	_ context.Context,
	_ string,
	content io.Reader,
	_ int64,
	_ [sha256.Size]byte,
) error {
	store.puts++
	store.content, _ = io.ReadAll(content)
	return nil
}

func (store *transportObjectStoreStub) OpenAuditExportObject(
	context.Context,
	string,
) (io.ReadCloser, error) {
	store.opens++
	if store.openErr != nil {
		return nil, store.openErr
	}
	return io.NopCloser(bytes.NewReader(store.content)), nil
}
