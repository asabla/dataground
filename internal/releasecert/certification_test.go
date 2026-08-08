package releasecert

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testRevision = "0123456789abcdef0123456789abcdef01234567"
const testGoVersion = "go1.26.5"

type certificationFixture struct {
	directory     string
	statementFile string
	signatureFile string
	trustFile     string
	outputFile    string
	statement     Statement
	signature     Signature
	trust         TrustProfile
	privateKey    ed25519.PrivateKey
	now           time.Time
}

func TestInstallAndVerifyCertification(t *testing.T) {
	t.Parallel()
	fixture := newCertificationFixture(t)

	request := fixture.installRequest()
	if err := Install(request); err != nil {
		t.Fatalf("install certification: %v", err)
	}
	verification, err := VerifyOIDCLoopbackFile(
		fixture.outputFile,
		fixture.trustFile,
		testRevision,
		testGoVersion,
		fixture.now,
	)
	if err != nil {
		t.Fatalf("verify certification: %v", err)
	}
	if verification.Envelope.Statement.ReleaseID != fixture.statement.ReleaseID ||
		verification.Envelope.Signature.KeyID != fixture.signature.KeyID ||
		verification.ProviderDPoPIssuance.Statement.ProviderID != "primary" {
		t.Fatalf("verified certification = %#v", verification)
	}
	if err := Install(request); err != nil {
		t.Fatalf("replay certification: %v", err)
	}
}

func TestPrepareSigningMessage(t *testing.T) {
	t.Parallel()
	fixture := newCertificationFixture(t)
	messageFile := filepath.Join(fixture.directory, "signing-message")
	request := PrepareRequest{
		StatementFile:      fixture.statementFile,
		TrustProfileFile:   fixture.trustFile,
		SigningMessageFile: messageFile,
		SourceRevision:     testRevision,
		GoVersion:          testGoVersion,
		Now:                fixture.now,
	}
	if err := PrepareSigningMessage(request); err != nil {
		t.Fatalf("prepare signing message: %v", err)
	}
	statementBytes, err := os.ReadFile(fixture.statementFile)
	if err != nil {
		t.Fatal(err)
	}
	message, err := os.ReadFile(messageFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(message) != string(signatureMessage(statementBytes)) {
		t.Fatal("prepared signing message does not match the verifier input")
	}
	if err := PrepareSigningMessage(request); err != nil {
		t.Fatalf("replay signing-message preparation: %v", err)
	}
}

func TestDPoPNonceCapacityBindingRequiresExactConfiguration(t *testing.T) {
	t.Parallel()
	disabled := false
	if err := verifyDPoPNonceCapacityBinding(dpopNonceCapacityBinding{Enabled: &disabled}, nil); err != nil {
		t.Fatalf("disabled nonce binding: %v", err)
	}
	if err := verifyDPoPNonceCapacityBinding(dpopNonceCapacityBinding{Enabled: &disabled}, []byte("null")); err == nil {
		t.Fatal("null nonce configuration was accepted")
	}
	enabledValue := true
	enabled := dpopNonceCapacityBinding{
		Enabled: &enabledValue, LifetimeNanoseconds: time.Minute.Nanoseconds(), MaximumActivePerKey: 4,
	}
	if err := verifyDPoPNonceCapacityBinding(dpopNonceCapacityBinding{}, nil); err == nil {
		t.Fatal("missing nonce capacity enabled state was accepted")
	}
	configuration := []byte(`{"lifetime":"1m","maximumActivePerKey":4}`)
	if err := verifyDPoPNonceCapacityBinding(enabled, configuration); err != nil {
		t.Fatalf("enabled nonce binding: %v", err)
	}
	for _, invalid := range [][]byte{
		[]byte(`{"lifetime":"2m","maximumActivePerKey":4}`),
		[]byte(`{"lifetime":"1m","maximumActivePerKey":3}`),
		[]byte(`{"lifetime":"1m","maximumActivePerKey":4,"unknown":true}`),
	} {
		if err := verifyDPoPNonceCapacityBinding(enabled, invalid); err == nil {
			t.Fatalf("invalid nonce binding %s was accepted", invalid)
		}
	}
}

func TestCertificationRejectsSignatureAndArtifactSubstitution(t *testing.T) {
	t.Parallel()

	t.Run("signature", func(t *testing.T) {
		fixture := newCertificationFixture(t)
		decoded, err := base64.RawURLEncoding.DecodeString(fixture.signature.Signature)
		if err != nil {
			t.Fatal(err)
		}
		decoded[0] ^= 1
		fixture.signature.Signature = base64.RawURLEncoding.EncodeToString(decoded)
		writeCanonicalPrivate(t, fixture.signatureFile, fixture.signature)
		if err := Install(fixture.installRequest()); err == nil || !strings.Contains(err.Error(), "does not verify") {
			t.Fatalf("signature substitution error = %v", err)
		}
	})

	t.Run("previous signing domain", func(t *testing.T) {
		fixture := newCertificationFixture(t)
		statementBytes, err := os.ReadFile(fixture.statementFile)
		if err != nil {
			t.Fatal(err)
		}
		message := append([]byte("DataGround release certification oidc-loopback v3\n"), statementBytes...)
		fixture.signature.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.privateKey, message))
		writeCanonicalPrivate(t, fixture.signatureFile, fixture.signature)
		if err := Install(fixture.installRequest()); err == nil || !strings.Contains(err.Error(), "does not verify") {
			t.Fatalf("previous signing domain error = %v", err)
		}
	})

	t.Run("artifact", func(t *testing.T) {
		fixture := newCertificationFixture(t)
		writePrivate(t, fixture.statement.Artifacts[0].File, []byte("changed\n"))
		if err := Install(fixture.installRequest()); err == nil || !strings.Contains(err.Error(), "artifact digest") {
			t.Fatalf("artifact substitution error = %v", err)
		}
	})

	t.Run("provider-registry-binding", func(t *testing.T) {
		fixture := newCertificationFixture(t)
		configurationPath := fixture.statement.Artifacts[2].File
		configuration, err := os.ReadFile(configurationPath)
		if err != nil {
			t.Fatal(err)
		}
		configuration = bytes.Replace(
			configuration,
			[]byte(strings.Repeat("0", sha256.Size*2)),
			[]byte(strings.Repeat("2", sha256.Size*2)),
			1,
		)
		writePrivate(t, configurationPath, configuration)
		fixture.statement.Artifacts[2].SHA256 = certificationArtifactDigest(t, configurationPath)
		fixture.resign(t)
		if err := Install(fixture.installRequest()); err == nil ||
			!strings.Contains(err.Error(), "provider DPoP issuance binding") {
			t.Fatalf("provider registry binding error = %v", err)
		}
	})

	t.Run("provider-trust-profile", func(t *testing.T) {
		fixture := newCertificationFixture(t)
		publicKey, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		providerTrustPath := fixture.statement.Artifacts[4].File
		writeCanonicalPrivate(t, providerTrustPath, ProviderDPoPIssuanceTrustProfile{
			Contract: ProviderDPoPIssuanceTrustContract,
			Keys: []TrustedKey{{
				KeyID:     "provider_reviewer_one",
				PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
			}},
		})
		fixture.statement.Artifacts[4].SHA256 = certificationArtifactDigest(t, providerTrustPath)
		fixture.resign(t)
		if err := Install(fixture.installRequest()); err == nil ||
			!strings.Contains(err.Error(), "provider DPoP issuance evidence") {
			t.Fatalf("provider trust binding error = %v", err)
		}
	})

	t.Run("trust-profile", func(t *testing.T) {
		fixture := newCertificationFixture(t)
		publicKey, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		fixture.trust.Keys[0].PublicKey = base64.RawURLEncoding.EncodeToString(publicKey)
		writeCanonicalPrivate(t, fixture.trustFile, fixture.trust)
		if err := Install(fixture.installRequest()); err == nil || !strings.Contains(err.Error(), "trust profile digest") {
			t.Fatalf("trust substitution error = %v", err)
		}
	})
}

func TestCertificationRejectsNonCanonicalEnvelope(t *testing.T) {
	t.Parallel()
	fixture := newCertificationFixture(t)
	if err := Install(fixture.installRequest()); err != nil {
		t.Fatalf("install certification: %v", err)
	}
	encoded, err := os.ReadFile(fixture.outputFile)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append([]byte(" "), encoded...)
	writePrivate(t, fixture.outputFile, encoded)
	if _, err := VerifyFile(
		fixture.outputFile,
		fixture.trustFile,
		testRevision,
		testGoVersion,
		fixture.now,
	); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("non-canonical envelope error = %v", err)
	}
}

func TestCertificationRejectsInvalidStatementSemantics(t *testing.T) {
	t.Parallel()

	mutations := []struct {
		name   string
		mutate func(*certificationFixture)
	}{
		{
			name: "source revision",
			mutate: func(fixture *certificationFixture) {
				fixture.statement.SourceRevision = strings.Repeat("f", 40)
			},
		},
		{
			name: "Go runtime",
			mutate: func(fixture *certificationFixture) {
				fixture.statement.GoVersion = "go1.26.4"
			},
		},
		{
			name: "expired",
			mutate: func(fixture *certificationFixture) {
				fixture.statement.ExpiresAt = fixture.now.Add(-time.Second).Format(time.RFC3339Nano)
			},
		},
		{
			name: "future issuance",
			mutate: func(fixture *certificationFixture) {
				fixture.statement.IssuedAt = fixture.now.Add(maximumClockSkew + time.Second).Format(time.RFC3339Nano)
				fixture.statement.ExpiresAt = fixture.now.Add(time.Hour).Format(time.RFC3339Nano)
			},
		},
		{
			name: "excessive validity",
			mutate: func(fixture *certificationFixture) {
				fixture.statement.ExpiresAt = fixture.now.Add(maximumValidity + time.Second).Format(time.RFC3339Nano)
			},
		},
		{
			name: "artifact order",
			mutate: func(fixture *certificationFixture) {
				fixture.statement.Artifacts[0], fixture.statement.Artifacts[1] =
					fixture.statement.Artifacts[1], fixture.statement.Artifacts[0]
			},
		},
		{
			name: "reason whitespace",
			mutate: func(fixture *certificationFixture) {
				fixture.statement.Reason = " reviewed release evidence "
			},
		},
		{
			name: "reason control character",
			mutate: func(fixture *certificationFixture) {
				fixture.statement.Reason = "reviewed\nrelease evidence"
			},
		},
		{
			name: "reused artifact path",
			mutate: func(fixture *certificationFixture) {
				fixture.statement.Artifacts[1].File = fixture.statement.Artifacts[0].File
				fixture.statement.Artifacts[1].SHA256 = fixture.statement.Artifacts[0].SHA256
			},
		},
	}

	for _, mutation := range mutations {
		mutation := mutation
		t.Run(mutation.name, func(t *testing.T) {
			t.Parallel()
			fixture := newCertificationFixture(t)
			mutation.mutate(fixture)
			fixture.resign(t)
			if err := Install(fixture.installRequest()); err == nil {
				t.Fatal("invalid certification was accepted")
			}
		})
	}
}

func TestCertificationRejectsExpiredProviderDPoPIssuance(t *testing.T) {
	t.Parallel()
	fixture := newCertificationFixture(t)
	if err := Install(fixture.installRequest()); err != nil {
		t.Fatalf("install certification: %v", err)
	}
	if _, err := VerifyFile(
		fixture.outputFile,
		fixture.trustFile,
		testRevision,
		testGoVersion,
		fixture.now.Add(2*time.Hour),
	); err == nil || !strings.Contains(err.Error(), "provider DPoP issuance evidence") {
		t.Fatalf("expired provider issuance error = %v", err)
	}
}

func TestCertificationRequiresCanonicalUniquePrivateFiles(t *testing.T) {
	t.Parallel()

	t.Run("non-canonical", func(t *testing.T) {
		fixture := newCertificationFixture(t)
		encoded, err := canonicalJSON(fixture.statement)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append([]byte(" "), encoded...)
		writePrivate(t, fixture.statementFile, encoded)
		if err := Install(fixture.installRequest()); err == nil || !strings.Contains(err.Error(), "not canonical") {
			t.Fatalf("non-canonical statement error = %v", err)
		}
	})

	t.Run("duplicate-member", func(t *testing.T) {
		fixture := newCertificationFixture(t)
		encoded, err := os.ReadFile(fixture.signatureFile)
		if err != nil {
			t.Fatal(err)
		}
		duplicate := strings.Replace(string(encoded), `"contract":`, `"contract":"duplicate","contract":`, 1)
		writePrivate(t, fixture.signatureFile, []byte(duplicate))
		if err := Install(fixture.installRequest()); err == nil || !strings.Contains(err.Error(), "signature is invalid") {
			t.Fatalf("duplicate signature member error = %v", err)
		}
	})

	t.Run("unsafe-permissions", func(t *testing.T) {
		fixture := newCertificationFixture(t)
		if err := os.Chmod(fixture.trustFile, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := Install(fixture.installRequest()); err == nil || !strings.Contains(err.Error(), "trust profile") {
			t.Fatalf("unsafe trust file error = %v", err)
		}
	})
}

func TestCertificationDoesNotReplaceExistingEnvelope(t *testing.T) {
	t.Parallel()
	fixture := newCertificationFixture(t)
	writePrivate(t, fixture.outputFile, []byte("occupied\n"))
	if err := Install(fixture.installRequest()); err == nil || !strings.Contains(err.Error(), "conflicts with existing file") {
		t.Fatalf("existing output error = %v", err)
	}
	content, err := os.ReadFile(fixture.outputFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "occupied\n" {
		t.Fatalf("existing output changed to %q", content)
	}
}

func newCertificationFixture(t *testing.T) *certificationFixture {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust := TrustProfile{
		Contract: TrustContract,
		Keys: []TrustedKey{{
			KeyID:     "release_key_01",
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		}},
	}
	trustFile := filepath.Join(directory, "trust.json")
	trustBytes := writeCanonicalPrivate(t, trustFile, trust)
	trustDigest := sha256.Sum256(trustBytes)

	provider := newProviderDPoPIssuanceFixture(t)
	now := provider.now
	if err := PrepareProviderDPoPIssuanceSigningMessage(ProviderDPoPIssuancePrepareRequest{
		StatementFile: provider.statementFile, TrustProfileFile: provider.trustFile,
		SigningMessageFile: provider.messageFile, Now: now,
	}); err != nil {
		t.Fatalf("prepare provider DPoP issuance evidence: %v", err)
	}
	providerMessage, err := os.ReadFile(provider.messageFile)
	if err != nil {
		t.Fatal(err)
	}
	writeProviderDPoPIssuanceJSON(t, provider.signatureFile, ProviderDPoPIssuanceSignature{
		Contract: ProviderDPoPIssuanceSignatureContract, KeyID: "provider_reviewer_one",
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(provider.privateKey, providerMessage)),
	})
	if err := InstallProviderDPoPIssuance(ProviderDPoPIssuanceInstallRequest{
		StatementFile: provider.statementFile, SignatureFile: provider.signatureFile,
		TrustProfileFile: provider.trustFile, OutputFile: provider.envelopeFile, Now: now,
	}); err != nil {
		t.Fatalf("install provider DPoP issuance evidence: %v", err)
	}
	capacityFile := filepath.Join(directory, "admission-capacity-evidence.json")
	capacityBytes := writeCanonicalPrivate(t, capacityFile, struct {
		Contract          string `json:"contract"`
		SourceRevision    string `json:"sourceRevision"`
		GoVersion         string `json:"goVersion"`
		DeploymentProfile string `json:"deploymentProfile"`
		Accepted          bool   `json:"accepted"`
		DPoPNonce         struct {
			Enabled             bool   `json:"enabled"`
			LifetimeNanoseconds int64  `json:"lifetimeNanoseconds"`
			MaximumActivePerKey uint32 `json:"maximumActivePerKey"`
		} `json:"dpopNonce"`
	}{
		Contract:          "dataground.authentication-rate-limit-capacity-evidence/v3",
		SourceRevision:    testRevision,
		GoVersion:         testGoVersion,
		DeploymentProfile: "team",
		Accepted:          true,
	})
	capacityDigest := sha256.Sum256(capacityBytes)
	capacityDigestHex := hex.EncodeToString(capacityDigest[:])
	policyFile := filepath.Join(directory, "api-authorization-policy.cedar")
	policyBytes := []byte("permit(principal, action, resource);\n")
	writePrivate(t, policyFile, policyBytes)
	policyDigest := sha256.Sum256(policyBytes)
	configurationFile := filepath.Join(directory, "oidc-security-configuration.json")
	configurationBytes := writeCanonicalPrivate(t, configurationFile, struct {
		Contract string `json:"contract"`
		Issuer   string `json:"issuer"`
		Provider struct {
			ID             string `json:"id"`
			RegistrySHA256 string `json:"registrySha256"`
		} `json:"provider"`
		Admission struct {
			DeploymentProfile    string `json:"deploymentProfile"`
			CapacityEvidenceFile string `json:"capacityEvidenceFile"`
			CapacityEvidenceHash string `json:"capacityEvidenceSha256"`
		} `json:"admission"`
		Authorization struct {
			PolicyFile string `json:"policyFile"`
		} `json:"authorization"`
	}{
		Contract: "dataground.api-security/oidc-dpop/v5",
		Issuer:   provider.statement.Issuer,
		Provider: struct {
			ID             string `json:"id"`
			RegistrySHA256 string `json:"registrySha256"`
		}{ID: provider.statement.ProviderID, RegistrySHA256: provider.statement.ProviderRegistrySHA256},
		Admission: struct {
			DeploymentProfile    string `json:"deploymentProfile"`
			CapacityEvidenceFile string `json:"capacityEvidenceFile"`
			CapacityEvidenceHash string `json:"capacityEvidenceSha256"`
		}{
			DeploymentProfile:    "team",
			CapacityEvidenceFile: capacityFile,
			CapacityEvidenceHash: capacityDigestHex,
		},
		Authorization: struct {
			PolicyFile string `json:"policyFile"`
		}{PolicyFile: policyFile},
	})
	configurationDigest := sha256.Sum256(configurationBytes)
	artifacts := []Artifact{
		{Kind: "admission-capacity-evidence", File: capacityFile, SHA256: capacityDigestHex},
		{Kind: "api-authorization-policy", File: policyFile, SHA256: hex.EncodeToString(policyDigest[:])},
		{Kind: "oidc-security-configuration", File: configurationFile, SHA256: hex.EncodeToString(configurationDigest[:])},
		{Kind: "provider-dpop-issuance-certification", File: provider.envelopeFile, SHA256: certificationArtifactDigest(t, provider.envelopeFile)},
		{Kind: "provider-dpop-issuance-trust-profile", File: provider.trustFile, SHA256: certificationArtifactDigest(t, provider.trustFile)},
	}
	fixture := &certificationFixture{
		directory:     directory,
		statementFile: filepath.Join(directory, "statement.json"),
		signatureFile: filepath.Join(directory, "signature.json"),
		trustFile:     trustFile,
		outputFile:    filepath.Join(directory, "certification.json"),
		statement: Statement{
			Contract:           StatementContract,
			ReleaseID:          "release_2026_08_02",
			SourceRevision:     testRevision,
			GoVersion:          testGoVersion,
			DeploymentProfile:  "team",
			TrustProfileSHA256: hex.EncodeToString(trustDigest[:]),
			IssuedAt:           now.Add(-time.Minute).Format(time.RFC3339Nano),
			ExpiresAt:          now.Add(24 * time.Hour).Format(time.RFC3339Nano),
			ReviewerID:         "reviewer_01",
			Reason:             "reviewed loopback OIDC release evidence",
			Artifacts:          artifacts,
		},
		trust:      trust,
		privateKey: privateKey,
		now:        now,
	}
	fixture.resign(t)
	return fixture
}

func (fixture *certificationFixture) resign(t *testing.T) {
	t.Helper()
	statementBytes := writeCanonicalPrivate(t, fixture.statementFile, fixture.statement)
	fixture.signature = Signature{
		Contract:  SignatureContract,
		KeyID:     fixture.trust.Keys[0].KeyID,
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(fixture.privateKey, signatureMessage(statementBytes))),
	}
	writeCanonicalPrivate(t, fixture.signatureFile, fixture.signature)
}

func (fixture *certificationFixture) installRequest() InstallRequest {
	return InstallRequest{
		StatementFile:    fixture.statementFile,
		SignatureFile:    fixture.signatureFile,
		TrustProfileFile: fixture.trustFile,
		OutputFile:       fixture.outputFile,
		SourceRevision:   testRevision,
		GoVersion:        testGoVersion,
		Now:              fixture.now,
	}
}

func certificationArtifactDigest(t *testing.T, path string) string {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func writeCanonicalPrivate(t *testing.T, path string, value any) []byte {
	t.Helper()
	encoded, err := canonicalJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	writePrivate(t, path, encoded)
	return encoded
}

func writePrivate(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
