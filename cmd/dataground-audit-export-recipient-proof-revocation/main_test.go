package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/auditseal"
	"github.com/asabla/dataground/internal/persistence"
)

func TestParseArgumentsRequiresClosedRevocationIntake(t *testing.T) {
	base := []string{
		"-isolation-domain", "iso_00000000000000000001",
		"-revocation-file", "/run/dataground/audit/archive-proof-revocation.json",
		"-revocation-trust-file", "/run/dataground/audit/archive-revocation-trust.json",
		"-actor", "operator@example.invalid",
		"-reason", "record external proofing key revocation",
		"-correlation-id", "cor_00000000000000000001",
	}
	request, err := parseArguments(base)
	if err != nil {
		t.Fatal(err)
	}
	if request.isolationDomainID != "iso_00000000000000000001" ||
		request.revocationFile == request.revocationTrustFile {
		t.Fatalf("request = %#v", request)
	}
	for name, arguments := range map[string][]string{
		"missing":    base[:len(base)-2],
		"same files": replaceArgument(base, base[5], base[3]),
		"control":    replaceArgument(base, base[9], "line one\nline two"),
		"positional": append(append([]string(nil), base...), "extra"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseArguments(arguments); err == nil {
				t.Fatal("invalid arguments were accepted")
			}
		})
	}
}

func TestExecuteRequestForwardsExactVerifiedRevocation(t *testing.T) {
	request := commandRequest{
		isolationDomainID: "iso_00000000000000000001", actorID: "operator@example.invalid",
		reason:        "record external proofing key revocation",
		correlationID: "cor_00000000000000000001",
	}
	issuedAt := time.Date(2026, 8, 3, 21, 0, 0, 0, time.UTC)
	verified := auditseal.VerifiedRecipientProofRevocation{
		Contract:          auditseal.RecipientProofRevocationContract,
		SHA256:            "sha256:" + strings.Repeat("1", 64),
		IsolationDomainID: request.isolationDomainID, Scope: "key",
		ProofingAuthorityID:          "archive-proofing.primary",
		ProofingTrustProfileSHA256:   "sha256:" + strings.Repeat("2", 64),
		ProofingSigningKeyID:         "proofing_key_01",
		ReasonSHA256:                 "sha256:" + strings.Repeat("3", 64),
		RevocationAuthorityID:        "archive-revocation.primary",
		RevocationTrustProfileSHA256: "sha256:" + strings.Repeat("4", 64),
		RevocationSigningKeyID:       "revocation_key_01",
		IssuedAt:                     issuedAt, EffectiveAt: issuedAt.Add(-time.Hour),
	}
	record := newRevocationRecord(request, verified)
	repository := &recipientProofRevocationRepositoryStub{}
	if err := executeRequest(context.Background(), repository, record); err != nil {
		t.Fatal(err)
	}
	if repository.calls != 1 || !repository.record.Valid() ||
		repository.record.RevocationSHA256 != verified.SHA256 {
		t.Fatalf("forwarded record = %#v; calls = %d", repository.record, repository.calls)
	}
	if err := executeRequest(context.Background(), nil, record); err == nil {
		t.Fatal("nil repository was accepted")
	}
}

type recipientProofRevocationRepositoryStub struct {
	calls  int
	record persistence.AuditExportRecipientProofRevocationRecord
}

func (repository *recipientProofRevocationRepositoryStub) RecordAuditExportRecipientProofRevocation(
	_ context.Context,
	record persistence.AuditExportRecipientProofRevocationRecord,
) error {
	if !record.Valid() {
		return errors.New("invalid revocation record")
	}
	repository.calls++
	repository.record = record
	return nil
}

func replaceArgument(arguments []string, oldValue string, newValue string) []string {
	result := append([]string(nil), arguments...)
	for index, value := range result {
		if value == oldValue {
			result[index] = newValue
			break
		}
	}
	return result
}
