package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/auditseal"
	"github.com/asabla/dataground/internal/persistence"
)

func TestParseArgumentsRequiresClosedTrustChange(t *testing.T) {
	digest := "sha256:" + strings.Repeat("3", 64)
	base := []string{
		"-operation", "activate",
		"-isolation-domain", "iso_00000000000000000001",
		"-recipient", "archive.primary",
		"-generation", "1",
		"-trust-file", "/run/dataground/audit/archive-trust.json",
		"-actor", "operator@example.invalid",
		"-reason", "activate reviewed archive trust",
		"-correlation-id", "cor_00000000000000000001",
	}
	request, err := parseArguments(base)
	if err != nil {
		t.Fatal(err)
	}
	if request.operation != "activate" || request.generation != 1 || request.recipientID != "archive.primary" {
		t.Fatalf("request = %#v", request)
	}
	revoke := []string{
		"-operation", "revoke",
		"-isolation-domain", "iso_00000000000000000001",
		"-recipient", "archive.primary",
		"-generation", "2",
		"-trust-sha256", digest,
		"-actor", "operator@example.invalid",
		"-reason", "revoke archive trust",
		"-correlation-id", "cor_00000000000000000002",
	}
	if request, err := parseArguments(revoke); err != nil || request.trustSHA256 != digest {
		t.Fatalf("revoke request = %#v; error = %v", request, err)
	}
	for name, arguments := range map[string][]string{
		"missing":          base[:len(base)-2],
		"operation":        replaceArgument(base, "activate", "delete"),
		"generation":       replaceArgument(base, "1", "0"),
		"revoke with file": replaceArgument(base, "activate", "revoke"),
		"uppercase digest": replaceArgument(revoke, digest, "sha256:"+strings.Repeat("A", 64)),
		"control reason":   replaceArgument(base, "activate reviewed archive trust", "line one\nline two"),
		"positional":       append(append([]string(nil), base...), "extra"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseArguments(arguments); err == nil {
				t.Fatal("invalid arguments were accepted")
			}
		})
	}
}

func TestExecuteRequestForwardsExactValidatedChange(t *testing.T) {
	request := commandRequest{
		operation: "activate", isolationDomainID: "iso_00000000000000000001",
		recipientID: "archive.primary", generation: 1,
		actorID: "operator@example.invalid", reason: "activate reviewed archive trust",
		correlationID: "cor_00000000000000000001",
	}
	profile := auditseal.RecipientTrustEvidence{
		Contract:    "dataground.audit-export-recipient-trust/ed25519/v1",
		RecipientID: request.recipientID, SHA256: "sha256:" + strings.Repeat("3", 64),
		KeyIDs: []string{"archive_key_01"},
	}
	change := newTrustChange(request, profile)
	repository := &recipientTrustRepositoryStub{}
	if err := executeRequest(context.Background(), repository, change); err != nil {
		t.Fatal(err)
	}
	if repository.calls != 1 || !repository.change.Valid() ||
		repository.change.TrustProfileSHA256 != profile.SHA256 {
		t.Fatalf("forwarded change = %#v; calls = %d", repository.change, repository.calls)
	}
	if err := executeRequest(context.Background(), nil, change); err == nil {
		t.Fatal("nil repository was accepted")
	}
}

type recipientTrustRepositoryStub struct {
	calls  int
	change persistence.AuditExportRecipientTrustChange
}

func (repository *recipientTrustRepositoryStub) ChangeAuditExportRecipientTrust(
	_ context.Context,
	change persistence.AuditExportRecipientTrustChange,
) error {
	if !change.Valid() {
		return errors.New("invalid trust change")
	}
	repository.calls++
	repository.change = change
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
