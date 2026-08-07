package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/auditseal"
	"github.com/asabla/dataground/internal/persistence"
)

func TestParseArgumentsRequiresClosedAuthorityChange(t *testing.T) {
	base := []string{
		"-operation", "activate", "-isolation-domain", "iso_00000000000000000001",
		"-authority", "archive-proofing.primary", "-generation", "1",
		"-trust-file", "/run/dataground/audit/archive-proofing-trust.json",
		"-actor", "operator@example.invalid", "-reason", "authorize reviewed proofing authority",
		"-correlation-id", "cor_00000000000000000001",
	}
	if _, err := parseArguments(base); err != nil {
		t.Fatal(err)
	}
	revoke := []string{
		"-operation", "revoke", "-isolation-domain", "iso_00000000000000000001",
		"-authority", "archive-proofing.primary", "-generation", "2",
		"-trust-sha256", "sha256:" + strings.Repeat("1", 64),
		"-actor", "operator@example.invalid", "-reason", "withdraw compromised proofing authority",
		"-correlation-id", "cor_00000000000000000002",
	}
	if _, err := parseArguments(revoke); err != nil {
		t.Fatal(err)
	}
	for name, arguments := range map[string][]string{
		"missing":         base[:len(base)-2],
		"activate digest": append(append([]string(nil), base...), "-trust-sha256", "sha256:"+strings.Repeat("1", 64)),
		"revoke file":     append(append([]string(nil), revoke...), "-trust-file", "/tmp/trust.json"),
		"control":         replaceArgument(base, "authorize reviewed proofing authority", "line one\nline two"),
		"positional":      append(append([]string(nil), base...), "extra"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseArguments(arguments); err == nil {
				t.Fatal("invalid authority arguments were accepted")
			}
		})
	}
}

func TestExecuteRequestForwardsExactAuthorityChange(t *testing.T) {
	request := commandRequest{
		operation: "activate", isolationDomainID: "iso_00000000000000000001",
		authorityID: "archive-proofing.primary", generation: 1,
		actorID: "operator@example.invalid", reason: "authorize reviewed authority",
		correlationID: "cor_00000000000000000001",
	}
	evidence := auditseal.ProofingAuthorityTrustEvidence{
		Contract: auditseal.RecipientProofingTrustContract,
		SHA256:   "sha256:" + strings.Repeat("1", 64), AuthorityID: request.authorityID,
		KeyIDs: []string{"proofing_key_01"},
	}
	change := newAuthorityChange(request, evidence)
	repository := &proofingAuthorityRepositoryStub{}
	if err := executeRequest(context.Background(), repository, change); err != nil {
		t.Fatal(err)
	}
	if repository.calls != 1 || !repository.change.Valid() ||
		repository.change.TrustProfileSHA256 != evidence.SHA256 {
		t.Fatalf("forwarded change = %#v; calls = %d", repository.change, repository.calls)
	}
	if err := executeRequest(context.Background(), nil, change); err == nil {
		t.Fatal("nil repository was accepted")
	}
}

type proofingAuthorityRepositoryStub struct {
	calls  int
	change persistence.AuditExportProofingAuthorityChange
}

func (repository *proofingAuthorityRepositoryStub) ChangeAuditExportProofingAuthority(
	_ context.Context,
	change persistence.AuditExportProofingAuthorityChange,
) error {
	if !change.Valid() {
		return errors.New("invalid authority change")
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
