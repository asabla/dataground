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

func TestParseArgumentsSeparatesActivationAndRevocationEvidence(t *testing.T) {
	base := []string{
		"-isolation-domain", "iso_00000000000000000001",
		"-workload", "audit-export.dispatcher", "-generation", "1",
		"-client-certificate-sha256", "sha256:" + strings.Repeat("6", 64),
		"-actor", "operator@example.invalid", "-reason", "authorize audit export transport",
		"-correlation-id", "cor_00000000000000000001",
	}
	activate := append(append([]string{}, base...),
		"-operation", "activate", "-grant-file", "/grant.json", "-issuer-trust-file", "/trust.json")
	if _, err := parseArguments(activate); err != nil {
		t.Fatalf("parse activation: %v", err)
	}
	revoke := append(append([]string{}, base...),
		"-operation", "revoke", "-grant-sha256", "sha256:"+strings.Repeat("8", 64))
	if _, err := parseArguments(revoke); err != nil {
		t.Fatalf("parse revocation: %v", err)
	}
	if _, err := parseArguments(append(activate, "-grant-sha256", "sha256:"+strings.Repeat("8", 64))); err == nil {
		t.Fatal("activation accepted revocation evidence")
	}
}

func TestNewWorkloadIdentityChangeBindsVerifiedGrant(t *testing.T) {
	now := time.Date(2026, 8, 6, 10, 0, 0, 0, time.UTC)
	request := commandRequest{
		operation: "activate", isolationDomainID: "iso_00000000000000000001",
		workloadID: "audit-export.dispatcher", generation: 1,
		clientCertificateSHA256: "sha256:" + strings.Repeat("6", 64),
		actorID:                 "operator@example.invalid", reason: "authorize transport identity",
		correlationID: "cor_00000000000000000001",
	}
	grant := auditseal.VerifiedWorkloadIdentityGrant{
		Contract: auditseal.WorkloadIdentityGrantContract, SHA256: "sha256:" + strings.Repeat("8", 64),
		IsolationDomainID: request.isolationDomainID, WorkloadID: request.workloadID,
		Audience:                auditseal.AuditExportTransportAudience,
		ClientCertificateSHA256: request.clientCertificateSHA256,
		AuthorityID:             "workload-issuer.primary", IssuerTrustProfileSHA256: "sha256:" + strings.Repeat("9", 64),
		IssuerSigningKeyID: "issuer_key_01", IssuedAt: now.Add(-2 * time.Minute),
		NotBefore: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	change := newWorkloadIdentityChange(request, grant)
	if !change.Valid() || change.GrantSHA256 != grant.SHA256 || change.ExpiresAt != grant.ExpiresAt {
		t.Fatalf("workload identity change = %#v", change)
	}
}

func TestExecuteRequestDelegatesOnce(t *testing.T) {
	want := errors.New("sentinel")
	repository := &fakeRepository{err: want}
	err := executeRequest(context.Background(), repository, persistence.AuditExportWorkloadIdentityChange{})
	if !errors.Is(err, want) || repository.calls != 1 {
		t.Fatalf("execute request error = %v, calls = %d", err, repository.calls)
	}
}

type fakeRepository struct {
	calls int
	err   error
}

func (repository *fakeRepository) ChangeAuditExportWorkloadIdentity(
	context.Context,
	persistence.AuditExportWorkloadIdentityChange,
) error {
	repository.calls++
	return repository.err
}
