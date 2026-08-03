package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func TestParseArgumentsRejectsIncompleteAndMixedModes(t *testing.T) {
	digest := "sha256:" + strings.Repeat("1", 64)
	base := []string{
		"-delivery-id", "adl_00000000000000000001",
		"-isolation-domain", "iso_00000000000000000001",
		"-envelope-file", "/run/dataground/audit/envelope.json",
		"-trust-file", "/run/dataground/audit/trust.json",
		"-recipient", "archive.primary",
		"-destination-sha256", digest,
		"-actor", "operator@example.invalid",
		"-reason", "archive incident export",
		"-correlation-id", "cor_00000000000000000001",
	}
	for name, arguments := range map[string][]string{
		"missing mode": base,
		"unknown mode": append(append([]string{}, base...), "-operation", "send"),
		"prepare with acknowledgement": append(append([]string{}, base...),
			"-operation", "prepare", "-receipt-file", "/tmp/receipt", "-recipient-trust-file", "/tmp/recipient-trust"),
		"acknowledge without evidence": append(append([]string{}, base...), "-operation", "acknowledge"),
		"acknowledge colliding evidence files": append(append([]string{}, base...),
			"-operation", "acknowledge", "-receipt-file", "/tmp/receipt", "-recipient-trust-file", "/tmp/receipt"),
		"invalid destination digest": {"-operation", "prepare", "-delivery-id", "adl_00000000000000000001",
			"-isolation-domain", "iso_00000000000000000001", "-envelope-file", "/tmp/envelope",
			"-trust-file", "/tmp/trust", "-recipient", "archive", "-destination-sha256", "sha256:bad",
			"-actor", "operator", "-reason", "reason", "-correlation-id", "cor_00000000000000000001"},
		"non-canonical destination digest": {"-operation", "prepare", "-delivery-id", "adl_00000000000000000001",
			"-isolation-domain", "iso_00000000000000000001", "-envelope-file", "/tmp/envelope",
			"-trust-file", "/tmp/trust", "-recipient", "archive", "-destination-sha256", "sha256:" + strings.Repeat("A", 64),
			"-actor", "operator", "-reason", "reason", "-correlation-id", "cor_00000000000000000001"},
		"control-bearing reason": append(append([]string{}, base...), "-operation", "prepare", "-reason", "line one\nline two"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseArguments(arguments); err == nil {
				t.Fatal("invalid arguments were accepted")
			}
		})
	}
}

func TestExecuteRequestKeepsPreparationAndAcknowledgementDistinct(t *testing.T) {
	digest := sha256.Sum256([]byte("evidence"))
	delivery := validDelivery(digest)
	repository := &deliveryRepositoryStub{}
	prepare := commandRequest{
		operation: "prepare", actorID: "operator@example.invalid", reason: "deliver",
		correlationID: "cor_00000000000000000001",
	}
	if err := executeRequest(context.Background(), repository, delivery, prepare); err != nil {
		t.Fatal(err)
	}
	if repository.prepared != 1 || repository.acknowledged != 0 {
		t.Fatalf("prepare calls = %d, acknowledge calls = %d", repository.prepared, repository.acknowledged)
	}
	acknowledge := prepare
	acknowledge.operation = "acknowledge"
	acknowledge.correlationID = "cor_00000000000000000002"
	acknowledge.acknowledgement = persistence.AuditExportDeliveryAcknowledgement{
		AcknowledgementDigest:       digest[:],
		ReceiptContract:             "dataground.audit-export-delivery-receipt/ed25519/v1",
		RecipientTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		RecipientSigningKeyID:       "archive_key_01",
		AcceptedAt:                  time.Date(2026, 8, 3, 15, 30, 0, 123000, time.UTC),
	}
	if err := executeRequest(context.Background(), repository, delivery, acknowledge); err != nil {
		t.Fatal(err)
	}
	if repository.prepared != 1 || repository.acknowledged != 1 {
		t.Fatalf("prepare calls = %d, acknowledge calls = %d", repository.prepared, repository.acknowledged)
	}
}

type deliveryRepositoryStub struct {
	prepared     int
	acknowledged int
}

func (repository *deliveryRepositoryStub) PrepareAuditExportDelivery(
	_ context.Context,
	_ persistence.AuditExportDelivery,
	attribution persistence.AuditExportDeliveryAttribution,
) error {
	if !attribution.Valid() {
		return errors.New("invalid preparation attribution")
	}
	repository.prepared++
	return nil
}

func (repository *deliveryRepositoryStub) AcknowledgeAuditExportDelivery(
	_ context.Context,
	_ persistence.AuditExportDelivery,
	acknowledgement persistence.AuditExportDeliveryAcknowledgement,
) error {
	if !acknowledgement.Valid() {
		return errors.New("invalid acknowledgement")
	}
	repository.acknowledged++
	return nil
}

func validDelivery(digest [sha256.Size]byte) persistence.AuditExportDelivery {
	return persistence.AuditExportDelivery{
		Contract: persistence.AuditExportDeliveryContract, DeliveryID: "adl_00000000000000000001",
		IsolationDomainID: "iso_00000000000000000001", ExportKind: "operator",
		ExportID: "oax_00000000000000000001", EnvelopeDigest: digest[:],
		ExportSHA256:       "sha256:" + strings.Repeat("1", 64),
		TrustProfileSHA256: "sha256:" + strings.Repeat("2", 64), SigningKeyID: "audit_key_01",
		RecipientID: "archive.primary", DestinationDigest: digest[:],
	}
}
