package auditseal

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRevocationNoticeAcquirerPinsSourceCredentialsAndVerifiesNotice(t *testing.T) {
	fixture := newRecipientProofRevocationFixture(t, "profile")
	notice, err := os.ReadFile(fixture.revocationFile)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := os.ReadFile(fixture.trustFile)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/notice":
			if request.Header.Get("Authorization") != "Bearer notice-token" {
				http.Error(response, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = response.Write(notice)
		case "/trust":
			if request.Header.Get("Authorization") != "Bearer trust-token" {
				http.Error(response, "unauthorized", http.StatusUnauthorized)
				return
			}
			_, _ = response.Write(trust)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(directory, "sources.json")
	noticeCredentialPath := filepath.Join(directory, "notice-credential.json")
	trustCredentialPath := filepath.Join(directory, "trust-credential.json")
	registry := revocationSourceRegistry{
		Contract: revocationSourceRegistryContract,
		Sources: []revocationSourceProfile{{
			ID: "archive-revocations.primary", Purpose: RevocationNoticePurposeRecipientProof,
			NoticeURL: server.URL + "/notice", TrustURL: server.URL + "/trust",
			NoticeAuthentication: revocationSourceAuthentication{
				Kind: "bearer-credential-file", CredentialFile: noticeCredentialPath,
			},
			TrustAuthentication: revocationSourceAuthentication{
				Kind: "bearer-credential-file", CredentialFile: trustCredentialPath,
			},
		}},
	}
	writeCanonicalPrivate(t, registryPath, registry)
	registryBytes, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(registryBytes)
	registrySHA256 := digestString(digest)
	evidence, err := InspectRevocationSourceRegistryFile(
		registryPath, RevocationNoticePurposeRecipientProof, "archive-revocations.primary",
	)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.SourceRegistrySHA256 != registrySHA256 ||
		evidence.Purpose != RevocationNoticePurposeRecipientProof {
		t.Fatalf("revocation source evidence = %#v", evidence)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	writeCanonicalPrivate(t, noticeCredentialPath, revocationSourceCredentialDocument{
		Contract: revocationSourceCredentialContract, IsolationDomainID: fixture.isolationDomainID,
		Purpose: RevocationNoticePurposeRecipientProof,
		SourceID: "archive-revocations.primary", SourceRegistrySHA256: registrySHA256,
		Endpoint: "notice", ActivatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		BearerToken: json.RawMessage(`"notice-token"`),
	})
	writeCanonicalPrivate(t, trustCredentialPath, revocationSourceCredentialDocument{
		Contract: revocationSourceCredentialContract, IsolationDomainID: fixture.isolationDomainID,
		Purpose: RevocationNoticePurposeRecipientProof,
		SourceID: "archive-revocations.primary", SourceRegistrySHA256: registrySHA256,
		Endpoint: "trust", ActivatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		BearerToken: json.RawMessage(`"trust-token"`),
	})
	acquirer, err := NewRevocationNoticeAcquirer(RevocationNoticeAcquisitionConfig{
		IsolationDomainID: fixture.isolationDomainID, Purpose: RevocationNoticePurposeRecipientProof,
		SourceID: "archive-revocations.primary", SourceRegistryFile: registryPath,
		SourceRegistrySHA256: registrySHA256,
		Transport:            server.Client().Transport.(*http.Transport),
	})
	if err != nil {
		t.Fatal(err)
	}
	noticeEvidence, trustEvidence, err := acquirer.CredentialEvidence()
	if err != nil {
		t.Fatal(err)
	}
	if noticeEvidence.Endpoint != "notice" || trustEvidence.Endpoint != "trust" ||
		noticeEvidence.CredentialSHA256 == trustEvidence.CredentialSHA256 ||
		!noticeEvidence.ActivatedAt.Equal(now.Add(-time.Minute)) ||
		!trustEvidence.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("credential evidence = %#v, %#v", noticeEvidence, trustEvidence)
	}
	acquired, err := acquirer.Acquire(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if acquired.RecipientProof == nil || acquired.WorkloadIdentity != nil ||
		acquired.RecipientProof.RevocationAuthorityID != "archive-revocation.primary" ||
		acquired.SourceRegistrySHA256 != registrySHA256 ||
		acquired.NoticeCredential != noticeEvidence ||
		acquired.TrustCredential != trustEvidence {
		t.Fatalf("acquired revocation = %#v", acquired)
	}
	if _, err := acquirer.Acquire(t.Context(), now); err == nil {
		t.Fatal("single-use revocation acquirer was reused")
	}
}

func TestRevocationNoticeAcquirerRejectsRegistryAndCredentialSubstitution(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(directory, "sources.json")
	noticeCredentialPath := filepath.Join(directory, "notice-credential.json")
	trustCredentialPath := filepath.Join(directory, "trust-credential.json")
	registry := revocationSourceRegistry{
		Contract: revocationSourceRegistryContract,
		Sources: []revocationSourceProfile{{
			ID: "archive-revocations.primary", Purpose: RevocationNoticePurposeRecipientProof,
			NoticeURL: "https://revocations.example.test/notice",
			TrustURL:  "https://revocations.example.test/trust",
			NoticeAuthentication: revocationSourceAuthentication{
				Kind: "bearer-credential-file", CredentialFile: noticeCredentialPath,
			},
			TrustAuthentication: revocationSourceAuthentication{
				Kind: "bearer-credential-file", CredentialFile: trustCredentialPath,
			},
		}},
	}
	writeCanonicalPrivate(t, registryPath, registry)
	encoded, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	registrySHA256 := digestString(digest)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for endpoint, credentialPath := range map[string]string{
		"notice": noticeCredentialPath,
		"trust": trustCredentialPath,
	} {
		writeCanonicalPrivate(t, credentialPath, revocationSourceCredentialDocument{
			Contract: revocationSourceCredentialContract,
			IsolationDomainID: "iso_00000000000000000001",
			Purpose: RevocationNoticePurposeRecipientProof,
			SourceID: "archive-revocations.primary",
			SourceRegistrySHA256: registrySHA256, Endpoint: endpoint,
			ActivatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
			BearerToken: json.RawMessage(`"shared-token"`),
		})
	}
	if _, err := NewRevocationNoticeAcquirer(RevocationNoticeAcquisitionConfig{
		IsolationDomainID: "iso_00000000000000000001",
		Purpose:           RevocationNoticePurposeRecipientProof, SourceID: "archive-revocations.primary",
		SourceRegistryFile: registryPath, SourceRegistrySHA256: registrySHA256,
	}); err == nil {
		t.Fatal("shared endpoint bearer token was accepted")
	}
}
