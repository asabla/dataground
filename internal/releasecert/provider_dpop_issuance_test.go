package releasecert

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProviderDPoPIssuanceEvidenceLifecycle(t *testing.T) {
	t.Parallel()
	fixture := newProviderDPoPIssuanceFixture(t)

	if err := PrepareProviderDPoPIssuanceSigningMessage(ProviderDPoPIssuancePrepareRequest{
		StatementFile: fixture.statementFile, TrustProfileFile: fixture.trustFile,
		SigningMessageFile: fixture.messageFile, Now: fixture.now,
	}); err != nil {
		t.Fatalf("prepare signing message: %v", err)
	}
	message, err := os.ReadFile(fixture.messageFile)
	if err != nil {
		t.Fatal(err)
	}
	signature := ProviderDPoPIssuanceSignature{
		Contract:  ProviderDPoPIssuanceSignatureContract,
		KeyID:     "provider_reviewer_one",
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.privateKey, message)),
	}
	writeProviderDPoPIssuanceJSON(t, fixture.signatureFile, signature)
	if err := InstallProviderDPoPIssuance(ProviderDPoPIssuanceInstallRequest{
		StatementFile: fixture.statementFile, SignatureFile: fixture.signatureFile,
		TrustProfileFile: fixture.trustFile, OutputFile: fixture.envelopeFile, Now: fixture.now,
	}); err != nil {
		t.Fatalf("install provider issuance evidence: %v", err)
	}
	envelope, err := VerifyProviderDPoPIssuanceFile(fixture.envelopeFile, fixture.trustFile, fixture.now)
	if err != nil {
		t.Fatalf("verify provider issuance evidence: %v", err)
	}
	if envelope.Statement.ProviderID != "primary" ||
		envelope.Statement.ConformanceReportSHA256 != fixture.statement.ConformanceReportSHA256 ||
		envelope.Signature.KeyID != "provider_reviewer_one" {
		t.Fatalf("verified envelope = %#v", envelope)
	}
	if err := InstallProviderDPoPIssuance(ProviderDPoPIssuanceInstallRequest{
		StatementFile: fixture.statementFile, SignatureFile: fixture.signatureFile,
		TrustProfileFile: fixture.trustFile, OutputFile: fixture.envelopeFile, Now: fixture.now,
	}); err != nil {
		t.Fatalf("replay provider issuance installation: %v", err)
	}
	fixture.report.AuthorizationServerNonce = "not-required"
	writeProviderDPoPIssuanceJSON(t, fixture.reportFile, fixture.report)
	if _, err := VerifyProviderDPoPIssuanceFile(
		fixture.envelopeFile, fixture.trustFile, fixture.now,
	); err == nil {
		t.Fatal("changed conformance report remained valid")
	}
}

func TestProviderDPoPIssuanceRejectsSignatureSubstitution(t *testing.T) {
	t.Parallel()
	fixture := newProviderDPoPIssuanceFixture(t)
	if err := PrepareProviderDPoPIssuanceSigningMessage(ProviderDPoPIssuancePrepareRequest{
		StatementFile: fixture.statementFile, TrustProfileFile: fixture.trustFile,
		SigningMessageFile: fixture.messageFile, Now: fixture.now,
	}); err != nil {
		t.Fatal(err)
	}
	message, err := os.ReadFile(fixture.messageFile)
	if err != nil {
		t.Fatal(err)
	}
	_, substitutedKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	writeProviderDPoPIssuanceJSON(t, fixture.signatureFile, ProviderDPoPIssuanceSignature{
		Contract:  ProviderDPoPIssuanceSignatureContract,
		KeyID:     "provider_reviewer_one",
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(substitutedKey, message)),
	})
	if err := InstallProviderDPoPIssuance(ProviderDPoPIssuanceInstallRequest{
		StatementFile: fixture.statementFile, SignatureFile: fixture.signatureFile,
		TrustProfileFile: fixture.trustFile, OutputFile: fixture.envelopeFile, Now: fixture.now,
	}); err == nil {
		t.Fatal("substituted provider reviewer key was accepted")
	}
}

func TestProviderDPoPIssuanceRejectsIncompleteConformance(t *testing.T) {
	t.Parallel()
	fixture := newProviderDPoPIssuanceFixture(t)
	fixture.report.Checks.TokenEndpointProofReplayRejected = false
	writeProviderDPoPIssuanceJSON(t, fixture.reportFile, fixture.report)
	setProviderDPoPIssuanceReportDigest(t, &fixture.statement, fixture.reportFile)
	writeProviderDPoPIssuanceJSON(t, fixture.statementFile, fixture.statement)

	err := PrepareProviderDPoPIssuanceSigningMessage(ProviderDPoPIssuancePrepareRequest{
		StatementFile: fixture.statementFile, TrustProfileFile: fixture.trustFile,
		SigningMessageFile: fixture.messageFile, Now: fixture.now,
	})
	if err == nil {
		t.Fatal("incomplete provider conformance was accepted")
	}
}

func TestProviderDPoPIssuanceRejectsBindingDrift(t *testing.T) {
	t.Parallel()
	fixture := newProviderDPoPIssuanceFixture(t)
	fixture.report.TokenEndpoint = "https://identity.example.invalid/other/token"
	writeProviderDPoPIssuanceJSON(t, fixture.reportFile, fixture.report)
	setProviderDPoPIssuanceReportDigest(t, &fixture.statement, fixture.reportFile)
	writeProviderDPoPIssuanceJSON(t, fixture.statementFile, fixture.statement)

	err := PrepareProviderDPoPIssuanceSigningMessage(ProviderDPoPIssuancePrepareRequest{
		StatementFile: fixture.statementFile, TrustProfileFile: fixture.trustFile,
		SigningMessageFile: fixture.messageFile, Now: fixture.now,
	})
	if err == nil {
		t.Fatal("provider token endpoint drift was accepted")
	}
}

func TestProviderDPoPIssuanceRejectsStaleObservationAndExpiry(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*providerDPoPIssuanceFixture){
		"stale observation": func(fixture *providerDPoPIssuanceFixture) {
			fixture.report.ObservedAt = fixture.now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
			writeProviderDPoPIssuanceJSON(t, fixture.reportFile, fixture.report)
			setProviderDPoPIssuanceReportDigest(t, &fixture.statement, fixture.reportFile)
		},
		"expired": func(fixture *providerDPoPIssuanceFixture) {
			fixture.statement.IssuedAt = fixture.now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
			fixture.statement.ExpiresAt = fixture.now.Add(-time.Hour).Format(time.RFC3339Nano)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newProviderDPoPIssuanceFixture(t)
			mutate(&fixture)
			writeProviderDPoPIssuanceJSON(t, fixture.statementFile, fixture.statement)
			if err := PrepareProviderDPoPIssuanceSigningMessage(ProviderDPoPIssuancePrepareRequest{
				StatementFile: fixture.statementFile, TrustProfileFile: fixture.trustFile,
				SigningMessageFile: fixture.messageFile, Now: fixture.now,
			}); err == nil {
				t.Fatal("invalid provider issuance validity was accepted")
			}
		})
	}
}

func TestProviderDPoPIssuanceRejectsNonCanonicalAndUnknownEvidence(t *testing.T) {
	t.Parallel()
	for name, content := range map[string][]byte{
		"noncanonical": []byte("{\n  \"contract\": \"dataground.provider-dpop-issuance-conformance/v1\"\n}\n"),
		"unknown":      []byte("{\"contract\":\"dataground.provider-dpop-issuance-conformance/v1\",\"unknown\":true}\n"),
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newProviderDPoPIssuanceFixture(t)
			if err := os.WriteFile(fixture.reportFile, content, 0o600); err != nil {
				t.Fatal(err)
			}
			setProviderDPoPIssuanceReportDigest(t, &fixture.statement, fixture.reportFile)
			writeProviderDPoPIssuanceJSON(t, fixture.statementFile, fixture.statement)
			if err := PrepareProviderDPoPIssuanceSigningMessage(ProviderDPoPIssuancePrepareRequest{
				StatementFile: fixture.statementFile, TrustProfileFile: fixture.trustFile,
				SigningMessageFile: fixture.messageFile, Now: fixture.now,
			}); err == nil {
				t.Fatal("invalid provider conformance JSON was accepted")
			}
		})
	}
}

func TestProviderDPoPIssuanceURLsMatchOIDCProfileShapes(t *testing.T) {
	t.Parallel()
	for _, issuer := range []string{
		"https://identity.example.invalid/",
		"https://identity.example.invalid:443/realms/dataground",
	} {
		if !validProviderDPoPIssuanceURL(issuer, true) {
			t.Fatalf("valid OIDC issuer was rejected: %q", issuer)
		}
	}
	if !validProviderDPoPIssuanceURL("https://identity.example.invalid/token", false) {
		t.Fatal("valid token endpoint was rejected")
	}
	for _, endpoint := range []string{
		"http://identity.example.invalid/token",
		"https://identity.example.invalid",
		"https://identity.example.invalid/token?audience=dataground-api",
	} {
		if validProviderDPoPIssuanceURL(endpoint, false) {
			t.Fatalf("invalid token endpoint was accepted: %q", endpoint)
		}
	}
}

type providerDPoPIssuanceFixture struct {
	now           time.Time
	privateKey    ed25519.PrivateKey
	report        ProviderDPoPIssuanceReport
	statement     ProviderDPoPIssuanceStatement
	reportFile    string
	statementFile string
	signatureFile string
	trustFile     string
	messageFile   string
	envelopeFile  string
}

func newProviderDPoPIssuanceFixture(t *testing.T) providerDPoPIssuanceFixture {
	t.Helper()
	directory := t.TempDir()
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := ProviderDPoPIssuanceTrustProfile{
		Contract: ProviderDPoPIssuanceTrustContract,
		Keys: []TrustedKey{{
			KeyID:     "provider_reviewer_one",
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		}},
	}
	trustFile := filepath.Join(directory, "trust.json")
	writeProviderDPoPIssuanceJSON(t, trustFile, trust)
	trustBytes, err := os.ReadFile(trustFile)
	if err != nil {
		t.Fatal(err)
	}
	trustDigest := sha256.Sum256(trustBytes)
	report := ProviderDPoPIssuanceReport{
		Contract:                 ProviderDPoPIssuanceReportContract,
		RunID:                    "provider_run_one",
		ProviderID:               "primary",
		ProviderRegistrySHA256:   hex.EncodeToString(make([]byte, sha256.Size)),
		Issuer:                   "https://identity.example.invalid/realms/dataground",
		TokenEndpoint:            "https://identity.example.invalid/realms/dataground/token",
		Audience:                 "dataground-api",
		GrantTypes:               []string{"authorization_code", "client_credentials"},
		DPoPAlgorithms:           []string{"ES256", "EdDSA"},
		AuthorizationServerNonce: "challenge-retry",
		ObservedAt:               now.Add(-5 * time.Minute).Format(time.RFC3339Nano),
		Checks: ProviderDPoPIssuanceChecks{
			TokenEndpointProofAccepted:         true,
			TokenTypeDPoP:                      true,
			ConfirmationJKTMatched:             true,
			MissingTokenEndpointProofRejected:  true,
			MismatchedTokenEndpointKeyRejected: true,
			TokenEndpointProofReplayRejected:   true,
			WrongTokenEndpointMethodRejected:   true,
			WrongTokenEndpointURIRejected:      true,
			StaleTokenEndpointProofRejected:    true,
			ResourceProofAccepted:              true,
			MismatchedResourceKeyRejected:      true,
			ResourceProofReplayRejected:        true,
			WrongResourceMethodRejected:        true,
			WrongResourceURIRejected:           true,
			WrongAccessTokenHashRejected:       true,
		},
	}
	reportFile := filepath.Join(directory, "report.json")
	writeProviderDPoPIssuanceJSON(t, reportFile, report)
	statement := ProviderDPoPIssuanceStatement{
		Contract:                 ProviderDPoPIssuanceStatementContract,
		CertificationID:          "provider_certification_one",
		ProviderID:               report.ProviderID,
		ProviderRegistrySHA256:   report.ProviderRegistrySHA256,
		Issuer:                   report.Issuer,
		TokenEndpoint:            report.TokenEndpoint,
		Audience:                 report.Audience,
		GrantTypes:               append([]string(nil), report.GrantTypes...),
		DPoPAlgorithms:           append([]string(nil), report.DPoPAlgorithms...),
		AuthorizationServerNonce: report.AuthorizationServerNonce,
		ConformanceReportFile:    reportFile,
		TrustProfileSHA256:       hex.EncodeToString(trustDigest[:]),
		IssuedAt:                 now.Format(time.RFC3339Nano),
		ExpiresAt:                now.Add(time.Hour).Format(time.RFC3339Nano),
		ReviewerID:               "reviewer_one",
		Reason:                   "certify the exact provider DPoP issuance profile",
	}
	setProviderDPoPIssuanceReportDigest(t, &statement, reportFile)
	statementFile := filepath.Join(directory, "statement.json")
	writeProviderDPoPIssuanceJSON(t, statementFile, statement)
	return providerDPoPIssuanceFixture{
		now: now, privateKey: privateKey, report: report, statement: statement,
		reportFile: reportFile, statementFile: statementFile,
		signatureFile: filepath.Join(directory, "signature.json"),
		trustFile:     trustFile, messageFile: filepath.Join(directory, "message.bin"),
		envelopeFile: filepath.Join(directory, "envelope.json"),
	}
}

func setProviderDPoPIssuanceReportDigest(
	t *testing.T,
	statement *ProviderDPoPIssuanceStatement,
	reportFile string,
) {
	t.Helper()
	encoded, err := os.ReadFile(reportFile)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	statement.ConformanceReportSHA256 = hex.EncodeToString(digest[:])
}

func writeProviderDPoPIssuanceJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := canonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
