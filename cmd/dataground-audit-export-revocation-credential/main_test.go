package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func TestParseArgumentsRequiresClosedCredentialScope(t *testing.T) {
	base := []string{
		"-operation", "activate",
		"-isolation-domain", "iso_00000000000000000001",
		"-purpose", "recipient-proof",
		"-source", "archive-revocations.primary",
		"-source-registry-sha256", "sha256:" + strings.Repeat("1", 64),
		"-endpoint", "notice",
		"-generation", "1",
		"-credential-file", "/run/dataground/audit/notice-credential.json",
		"-actor", "operator@example.invalid",
		"-reason", "authorize reviewed acquisition credential",
		"-correlation-id", "cor_00000000000000000001",
	}
	if _, err := parseArguments(base); err != nil {
		t.Fatal(err)
	}
	for name, arguments := range map[string][]string{
		"missing":  base[:len(base)-2],
		"purpose":  append(append([]string(nil), base...), "-purpose", "other"),
		"endpoint": append(append([]string(nil), base...), "-endpoint", "other"),
		"digest": append(append([]string(nil), base...),
			"-credential-sha256", "sha256:"+strings.Repeat("2", 64)),
		"extra": append(append([]string(nil), base...), "unexpected"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseArguments(arguments); err == nil {
				t.Fatal("invalid credential arguments were accepted")
			}
		})
	}
}

func TestParseArgumentsRequiresExactRevocationDigest(t *testing.T) {
	arguments := []string{
		"-operation", "revoke",
		"-isolation-domain", "iso_00000000000000000001",
		"-purpose", "recipient-proof",
		"-source", "archive-revocations.primary",
		"-source-registry-sha256", "sha256:" + strings.Repeat("1", 64),
		"-endpoint", "notice",
		"-generation", "2",
		"-credential-sha256", "sha256:" + strings.Repeat("2", 64),
		"-actor", "operator@example.invalid",
		"-reason", "record remote acquisition credential revocation",
		"-correlation-id", "cor_00000000000000000002",
	}
	request, err := parseArguments(arguments)
	if err != nil {
		t.Fatal(err)
	}
	change, err := newCredentialChange(request, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if change.Operation != "revoke" || change.Generation != 2 ||
		change.CredentialSHA256 != request.credentialSHA256 ||
		!change.ActivatedAt.IsZero() || !change.ExpiresAt.IsZero() {
		t.Fatalf("revocation change = %#v", change)
	}
}

func TestExecuteRequestRecordsCredentialChange(t *testing.T) {
	repository := &recordingCredentialRepository{}
	change := persistence.AuditExportRevocationCredentialChange{
		Contract:  persistence.AuditExportRevocationCredentialAuthorizationContract,
		Operation: "revoke", IsolationDomainID: "iso_00000000000000000001",
		Purpose: "recipient-proof", SourceID: "archive-revocations.primary",
		SourceRegistrySHA256: "sha256:" + strings.Repeat("1", 64),
		Endpoint:             "notice", Generation: 2,
		CredentialSHA256: "sha256:" + strings.Repeat("2", 64),
		ActorID:          "operator@example.invalid", ReasonDigest: make([]byte, 32),
		CorrelationID: "cor_00000000000000000002",
	}
	if err := executeRequest(context.Background(), repository, change); err != nil {
		t.Fatal(err)
	}
	if repository.change.CredentialSHA256 != change.CredentialSHA256 {
		t.Fatalf("recorded change = %#v", repository.change)
	}
}

type recordingCredentialRepository struct {
	change persistence.AuditExportRevocationCredentialChange
}

func (repository *recordingCredentialRepository) ChangeAuditExportRevocationCredential(
	_ context.Context,
	change persistence.AuditExportRevocationCredentialChange,
) error {
	repository.change = change
	return nil
}
