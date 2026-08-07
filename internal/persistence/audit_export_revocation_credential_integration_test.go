package persistence_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestAuditExportRevocationCredentialsAreSequentialAuditedAndRevocable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	isolationDomainID := identity.New("iso")
	sourceDigest := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	sourceReason := sha256.Sum256([]byte("activate reviewed revocation source"))
	if err := repository.ChangeAuditExportRevocationSource(
		ctx,
		persistence.AuditExportRevocationSourceChange{
			Contract:  persistence.AuditExportRevocationSourceAuthorizationContract,
			Operation: "activate", IsolationDomainID: isolationDomainID,
			Purpose:  persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
			SourceID: "archive-revocations.primary", Generation: 1,
			SourceRegistrySHA256: sourceDigest, ActorID: identity.New("usr"),
			ReasonDigest: sourceReason[:], CorrelationID: identity.New("cor"),
		},
	); err != nil {
		t.Fatalf("activate revocation source: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	notice := persistence.AuditExportRevocationCredentialEvidence{
		Endpoint:         "notice",
		CredentialSHA256: "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		ActivatedAt:      now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	trust := persistence.AuditExportRevocationCredentialEvidence{
		Endpoint:         "trust",
		CredentialSHA256: "sha256:3333333333333333333333333333333333333333333333333333333333333333",
		ActivatedAt:      now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	activate := func(evidence persistence.AuditExportRevocationCredentialEvidence) persistence.AuditExportRevocationCredentialChange {
		reason := sha256.Sum256([]byte("activate " + evidence.Endpoint + " credential"))
		return persistence.AuditExportRevocationCredentialChange{
			Contract:  persistence.AuditExportRevocationCredentialAuthorizationContract,
			Operation: "activate", IsolationDomainID: isolationDomainID,
			Purpose:  persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
			SourceID: "archive-revocations.primary", SourceRegistrySHA256: sourceDigest,
			Endpoint: evidence.Endpoint, Generation: 1,
			CredentialSHA256: evidence.CredentialSHA256,
			ActivatedAt:      evidence.ActivatedAt, ExpiresAt: evidence.ExpiresAt,
			ActorID: identity.New("usr"), ReasonDigest: reason[:],
			CorrelationID: identity.New("cor"),
		}
	}
	noticeActivation := activate(notice)
	if err := repository.ChangeAuditExportRevocationCredential(ctx, noticeActivation); err != nil {
		t.Fatalf("activate notice credential: %v", err)
	}
	if err := repository.ChangeAuditExportRevocationCredential(ctx, noticeActivation); err != nil {
		t.Fatalf("replay notice credential activation: %v", err)
	}
	trustActivation := activate(trust)
	if err := repository.ChangeAuditExportRevocationCredential(ctx, trustActivation); err != nil {
		t.Fatalf("activate trust credential: %v", err)
	}
	generations, err := repository.AuthorizeAuditExportRevocationCredentials(
		ctx, isolationDomainID,
		persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
		"archive-revocations.primary", sourceDigest, notice, trust,
	)
	if err != nil {
		t.Fatalf("authorize credential pair: %v", err)
	}
	if generations.Notice != 1 || generations.Trust != 1 {
		t.Fatalf("credential generations = %#v", generations)
	}

	conflict := noticeActivation
	conflict.ActorID = identity.New("usr")
	if err := repository.ChangeAuditExportRevocationCredential(
		ctx, conflict,
	); !errors.Is(err, persistence.ErrAuditExportRevocationCredentialConflict) {
		t.Fatalf("changed credential replay error = %v", err)
	}
	revokeReason := sha256.Sum256([]byte("record remote notice credential revocation"))
	revocation := persistence.AuditExportRevocationCredentialChange{
		Contract:  persistence.AuditExportRevocationCredentialAuthorizationContract,
		Operation: "revoke", IsolationDomainID: isolationDomainID,
		Purpose:  persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
		SourceID: "archive-revocations.primary", SourceRegistrySHA256: sourceDigest,
		Endpoint: "notice", Generation: 2, CredentialSHA256: notice.CredentialSHA256,
		ActorID: identity.New("usr"), ReasonDigest: revokeReason[:],
		CorrelationID: identity.New("cor"),
	}
	if err := repository.ChangeAuditExportRevocationCredential(ctx, revocation); err != nil {
		t.Fatalf("revoke notice credential: %v", err)
	}
	if _, err := repository.AuthorizeAuditExportRevocationCredentials(
		ctx, isolationDomainID,
		persistence.AuditExportRevocationAuthorityPurposeRecipientProof,
		"archive-revocations.primary", sourceDigest, notice, trust,
	); !errors.Is(err, persistence.ErrAuditExportRevocationCredentialUnauthorized) {
		t.Fatalf("revoked credential authorization error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM audit_export_revocation_credential_events
		WHERE correlation_id = $1
	`, revocation.CorrelationID); err == nil {
		t.Fatal("database allowed credential event deletion")
	}
}
