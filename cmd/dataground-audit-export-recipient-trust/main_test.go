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

func TestParseArgumentsRequiresClosedTrustChange(t *testing.T) {
	digest := "sha256:" + strings.Repeat("3", 64)
	base := []string{
		"-operation", "activate",
		"-isolation-domain", "iso_00000000000000000001",
		"-recipient", "archive.primary",
		"-generation", "1",
		"-trust-file", "/run/dataground/audit/archive-trust.json",
		"-identity-proof-file", "/run/dataground/audit/archive-identity-proof.json",
		"-proofing-trust-file", "/run/dataground/audit/archive-proofing-trust.json",
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
	verifiedAt := time.Date(2026, 8, 3, 20, 0, 0, 0, time.UTC)
	profile := auditseal.RecipientTrustEvidence{
		Contract:    "dataground.audit-export-recipient-trust/ed25519/v1",
		RecipientID: request.recipientID, SHA256: "sha256:" + strings.Repeat("3", 64),
		KeyIDs: []string{"archive_key_01"},
	}
	identityProof := auditseal.VerifiedRecipientIdentityProof{
		Contract:                    auditseal.RecipientIdentityProofContract,
		SHA256:                      "sha256:" + strings.Repeat("4", 64),
		RecipientTrustProfileSHA256: profile.SHA256,
		EvidenceSHA256:              "sha256:" + strings.Repeat("5", 64),
		AuthorityID:                 "archive-proofing.primary",
		ProofingTrustProfileSHA256:  "sha256:" + strings.Repeat("6", 64),
		SigningKeyID:                "proofing_key_01",
		VerifiedAt:                  verifiedAt, ExpiresAt: verifiedAt.Add(24 * time.Hour),
	}
	change := newTrustChange(request, profile, identityProof)
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

func TestNewTrustChangeSelectsEncryptionAuthorization(t *testing.T) {
	request := commandRequest{
		operation: "activate", isolationDomainID: "iso_00000000000000000001",
		recipientID: "archive.primary", generation: 1,
		actorID: "operator@example.invalid", reason: "activate encrypted archive trust",
		correlationID: "cor_00000000000000000001",
	}
	verifiedAt := time.Date(2026, 8, 4, 20, 0, 0, 0, time.UTC)
	profile := auditseal.RecipientTrustEvidence{
		Contract: auditseal.RecipientEncryptionTrustContract, RecipientID: request.recipientID,
		SHA256: "sha256:" + strings.Repeat("3", 64), KeyIDs: []string{"archive_signing_key_01"},
		EncryptionKeyIDs: []string{"archive_encryption_key_01"},
	}
	proof := auditseal.VerifiedRecipientIdentityProof{
		Contract: auditseal.RecipientIdentityProofContract, SHA256: "sha256:" + strings.Repeat("4", 64),
		EvidenceSHA256: "sha256:" + strings.Repeat("5", 64), AuthorityID: "archive-proofing.primary",
		ProofingTrustProfileSHA256: "sha256:" + strings.Repeat("6", 64), SigningKeyID: "proofing_key_01",
		VerifiedAt: verifiedAt, ExpiresAt: verifiedAt.Add(24 * time.Hour),
	}
	change := newTrustChange(request, profile, proof)
	if change.Contract != persistence.AuditExportRecipientEncryptionAuthorizationContract ||
		len(change.EncryptionKeyIDs) != 1 || !change.Valid() {
		t.Fatalf("encryption trust change = %#v", change)
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
