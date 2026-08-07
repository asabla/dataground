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

func TestParseArgumentsRequiresClosedAcquisitionScope(t *testing.T) {
	base := []string{
		"-isolation-domain", "iso_00000000000000000001",
		"-purpose", "recipient-proof", "-source", "archive-revocations.primary",
		"-source-registry-file", "/run/dataground/audit/revocation-sources.json",
		"-source-registry-sha256", "sha256:" + strings.Repeat("1", 64),
		"-actor", "operator@example.invalid", "-reason", "acquire reviewed revocation notice",
		"-correlation-id", "cor_00000000000000000001",
	}
	if _, err := parseArguments(base); err != nil {
		t.Fatal(err)
	}
	for name, arguments := range map[string][]string{
		"missing": base[:len(base)-2],
		"purpose": append(append([]string(nil), base...), "-purpose", "other"),
		"extra":   append(append([]string(nil), base...), "unexpected"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseArguments(arguments); err == nil {
				t.Fatal("invalid arguments were accepted")
			}
		})
	}
}

func TestExecuteRequestRecordsPurposeSpecificAcquisition(t *testing.T) {
	request := commandRequest{
		isolationDomainID:    "iso_00000000000000000001",
		purpose:              auditseal.RevocationNoticePurposeRecipientProof,
		sourceID:             "archive-revocations.primary",
		sourceRegistrySHA256: "sha256:" + strings.Repeat("1", 64),
		actorID:              "operator@example.invalid", reason: "acquire reviewed notice",
		correlationID: "cor_00000000000000000001",
	}
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	verified := auditseal.VerifiedRecipientProofRevocation{
		Contract:          auditseal.RecipientProofRevocationContract,
		SHA256:            "sha256:" + strings.Repeat("2", 64),
		IsolationDomainID: request.isolationDomainID, Scope: "profile",
		ProofingAuthorityID:          "archive-proofing.primary",
		ProofingTrustProfileSHA256:   "sha256:" + strings.Repeat("3", 64),
		ReasonSHA256:                 "sha256:" + strings.Repeat("4", 64),
		RevocationAuthorityID:        "archive-revocation.primary",
		RevocationTrustProfileSHA256: "sha256:" + strings.Repeat("5", 64),
		RevocationSigningKeyID:       "revocation_key_01", IssuedAt: now, EffectiveAt: now,
	}
	repository := &recordingRepository{}
	err := executeRequest(context.Background(), repository, request, auditseal.AcquiredRevocationNotice{
		Purpose: request.purpose, SourceID: request.sourceID,
		SourceRegistrySHA256: request.sourceRegistrySHA256, RecipientProof: &verified,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repository.recipient.Acquisition == nil ||
		repository.recipient.Acquisition.SourceID != request.sourceID ||
		repository.recipient.RevocationSHA256 != verified.SHA256 {
		t.Fatalf("recorded recipient revocation = %#v", repository.recipient)
	}
}

func TestExecuteRequestRecordsWorkloadIdentityAcquisition(t *testing.T) {
	request := commandRequest{
		isolationDomainID:    "iso_00000000000000000001",
		purpose:              auditseal.RevocationNoticePurposeWorkloadIdentity,
		sourceID:             "workload-revocations.primary",
		sourceRegistrySHA256: "sha256:" + strings.Repeat("1", 64),
		actorID:              "operator@example.invalid", reason: "acquire reviewed workload notice",
		correlationID: "cor_00000000000000000002",
	}
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	verified := auditseal.VerifiedWorkloadIdentityRevocation{
		Contract:          auditseal.WorkloadIdentityRevocationContract,
		SHA256:            "sha256:" + strings.Repeat("2", 64),
		IsolationDomainID: request.isolationDomainID, Scope: "profile",
		WorkloadIdentityAuthorityID:        "workload-issuer.primary",
		WorkloadIdentityTrustProfileSHA256: "sha256:" + strings.Repeat("3", 64),
		ReasonSHA256:                       "sha256:" + strings.Repeat("4", 64),
		RevocationAuthorityID:              "archive-revocation.primary",
		RevocationTrustProfileSHA256:       "sha256:" + strings.Repeat("5", 64),
		RevocationSigningKeyID:             "revocation_key_01", IssuedAt: now, EffectiveAt: now,
	}
	repository := &recordingRepository{}
	err := executeRequest(context.Background(), repository, request, auditseal.AcquiredRevocationNotice{
		Purpose: request.purpose, SourceID: request.sourceID,
		SourceRegistrySHA256: request.sourceRegistrySHA256, WorkloadIdentity: &verified,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repository.workload.Acquisition == nil ||
		repository.workload.Acquisition.SourceID != request.sourceID ||
		repository.workload.RevocationSHA256 != verified.SHA256 {
		t.Fatalf("recorded workload revocation = %#v", repository.workload)
	}
}

type recordingRepository struct {
	recipient persistence.AuditExportRecipientProofRevocationRecord
	workload  persistence.AuditExportWorkloadIdentityRevocationRecord
	replayed  bool
}

func (repository *recordingRepository) ReplayAuditExportRevocationAcquisition(
	_ context.Context,
	_ persistence.AuditExportRevocationAcquisitionReplay,
) (bool, error) {
	return repository.replayed, nil
}

func (repository *recordingRepository) RecordAuditExportRecipientProofRevocation(
	_ context.Context,
	record persistence.AuditExportRecipientProofRevocationRecord,
) error {
	repository.recipient = record
	return nil
}

func (repository *recordingRepository) RecordAuditExportWorkloadIdentityRevocation(
	_ context.Context,
	record persistence.AuditExportWorkloadIdentityRevocationRecord,
) error {
	repository.workload = record
	return nil
}

func TestExecuteRequestRejectsMismatchedAcquisition(t *testing.T) {
	request := commandRequest{purpose: auditseal.RevocationNoticePurposeRecipientProof}
	err := executeRequest(context.Background(), &recordingRepository{}, request, auditseal.AcquiredRevocationNotice{})
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("mismatched acquisition error = %v", err)
	}
}

func TestReplayRequestUsesExactAttribution(t *testing.T) {
	repository := &recordingRepository{replayed: true}
	request := commandRequest{
		isolationDomainID:    "iso_00000000000000000001",
		purpose:              auditseal.RevocationNoticePurposeRecipientProof,
		sourceID:             "archive-revocations.primary",
		sourceRegistrySHA256: "sha256:" + strings.Repeat("1", 64),
		actorID:              "operator@example.invalid", reason: "acquire reviewed notice",
		correlationID: "cor_00000000000000000001",
	}
	replayed, err := replayRequest(context.Background(), repository, request)
	if err != nil || !replayed {
		t.Fatalf("replay = %v, %v", replayed, err)
	}
}
