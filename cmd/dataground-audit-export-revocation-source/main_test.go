package main

import (
	"context"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/auditseal"
	"github.com/asabla/dataground/internal/persistence"
)

func TestParseArgumentsRequiresClosedSourceChange(t *testing.T) {
	base := []string{
		"-operation", "revoke",
		"-isolation-domain", "iso_00000000000000000001",
		"-purpose", "recipient-proof",
		"-source", "archive-revocations.primary",
		"-generation", "2",
		"-source-registry-sha256", "sha256:" + strings.Repeat("1", 64),
		"-actor", "operator@example.invalid",
		"-reason", "withdraw reviewed revocation source",
		"-correlation-id", "cor_00000000000000000001",
	}
	if _, err := parseArguments(base); err != nil {
		t.Fatal(err)
	}
	for name, arguments := range map[string][]string{
		"missing":   base[:len(base)-2],
		"operation": append(append([]string(nil), base...), "-operation", "replace"),
		"purpose":   append(append([]string(nil), base...), "-purpose", "other"),
		"mixed evidence": append(append([]string(nil), base...),
			"-source-registry-file", "/run/dataground/audit/revocation-sources.json"),
		"extra": append(append([]string(nil), base...), "unexpected"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseArguments(arguments); err == nil {
				t.Fatal("invalid arguments were accepted")
			}
		})
	}
}

func TestExecuteRequestRecordsExactSourceEvidence(t *testing.T) {
	request := commandRequest{
		operation: "activate", isolationDomainID: "iso_00000000000000000001",
		purpose:  auditseal.RevocationNoticePurposeRecipientProof,
		sourceID: "archive-revocations.primary", generation: 1,
		actorID: "operator@example.invalid", reason: "activate reviewed source",
		correlationID: "cor_00000000000000000001",
	}
	evidence := auditseal.RevocationSourceEvidence{
		Purpose: request.purpose, SourceID: request.sourceID,
		SourceRegistrySHA256: "sha256:" + strings.Repeat("1", 64),
	}
	change := newSourceChange(request, evidence)
	repository := &sourceRepositoryStub{}
	if err := executeRequest(context.Background(), repository, change); err != nil {
		t.Fatal(err)
	}
	if repository.change.SourceRegistrySHA256 != evidence.SourceRegistrySHA256 ||
		repository.change.Generation != 1 || !repository.change.Valid() {
		t.Fatalf("source change = %#v", repository.change)
	}
}

type sourceRepositoryStub struct {
	change persistence.AuditExportRevocationSourceChange
}

func (repository *sourceRepositoryStub) ChangeAuditExportRevocationSource(
	_ context.Context,
	change persistence.AuditExportRevocationSourceChange,
) error {
	repository.change = change
	return nil
}
