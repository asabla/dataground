package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/releasecert"
)

func TestLoadOIDCSecurityConfigurationOwnsStrictDeploymentInputs(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	keysets := writeStartupFile(t, directory, "keysets.json", `{}`)
	policy := writeStartupFile(t, directory, "api.cedar", `permit(principal, action, resource);`)
	evidence, evidenceHash := writeStartupCapacityEvidence(t, directory)
	configurationPath := writeStartupFile(
		t,
		directory,
		"security.json",
		startupConfiguration(keysets, policy, evidence, evidenceHash),
	)
	now := time.Now().UTC()
	certification, trust := writeStartupReleaseCertification(
		t,
		directory,
		configurationPath,
		policy,
		evidence,
		"0123456789abcdef0123456789abcdef01234567",
		"go1.26.5",
		now,
	)

	configuration, policyBytes, err := loadOIDCSecurityConfigurationForBuild(
		configurationPath,
		certification,
		trust,
		"0123456789abcdef0123456789abcdef01234567",
		"go1.26.5",
		now,
	)
	if err != nil {
		t.Fatalf("load OIDC security configuration: %v", err)
	}
	if configuration.Contract != oidcSecurityConfigurationContract ||
		configuration.KeysetPublicationFile != keysets ||
		configuration.Authorization.PolicyFile != policy ||
		configuration.JWT.MaximumLifetime.value != time.Hour ||
		configuration.Admission.Generation != 1 ||
		configuration.Admission.CredentialBurst != 10 ||
		!configuration.DPoP.Nonce.set ||
		configuration.DPoP.Nonce.value.Lifetime.value != time.Minute ||
		configuration.DPoP.Nonce.value.MaximumActivePerKey != 4 {
		t.Fatalf("loaded configuration = %#v", configuration)
	}
	if string(policyBytes) != `permit(principal, action, resource);` {
		t.Fatalf("loaded policy = %q", policyBytes)
	}
	clear(policyBytes)

	withoutNonce := strings.Replace(
		startupConfiguration(keysets, policy, evidence, evidenceHash),
		`,"nonce":{"lifetime":"1m","maximumActivePerKey":4}`,
		``,
		1,
	)
	withoutNonceDirectory := t.TempDir()
	withoutNonceEvidence, withoutNonceEvidenceHash := writeStartupCapacityEvidenceWithNonce(
		t,
		withoutNonceDirectory,
		false,
	)
	withoutNonce = strings.Replace(withoutNonce, evidence, withoutNonceEvidence, 1)
	withoutNonce = strings.Replace(withoutNonce, evidenceHash, withoutNonceEvidenceHash, 1)
	withoutNoncePath := writeStartupFile(
		t,
		withoutNonceDirectory,
		"security-without-nonce.json",
		withoutNonce,
	)
	withoutNonceCertification, withoutNonceTrust := writeStartupReleaseCertification(
		t,
		withoutNonceDirectory,
		withoutNoncePath,
		policy,
		withoutNonceEvidence,
		"0123456789abcdef0123456789abcdef01234567",
		"go1.26.5",
		now,
	)
	configuration, policyBytes, err = loadOIDCSecurityConfigurationForBuild(
		withoutNoncePath,
		withoutNonceCertification,
		withoutNonceTrust,
		"0123456789abcdef0123456789abcdef01234567",
		"go1.26.5",
		now,
	)
	clear(policyBytes)
	if err != nil || configuration.DPoP.Nonce.set {
		t.Fatalf("load configuration without nonce policy: %#v, %v", configuration.DPoP.Nonce, err)
	}
}

func TestLoadOIDCSecurityConfigurationRejectsAmbiguousOrUnsafeFiles(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	keysets := writeStartupFile(t, directory, "keysets.json", `{}`)
	policy := writeStartupFile(t, directory, "api.cedar", `permit(principal, action, resource);`)
	evidence, evidenceHash := writeStartupCapacityEvidence(t, directory)
	valid := startupConfiguration(keysets, policy, evidence, evidenceHash)

	tests := map[string]func() string{
		"old contract": func() string {
			return strings.Replace(valid, oidcSecurityConfigurationContract, "dataground.api-security/oidc-dpop/v4", 1)
		},
		"duplicate member": func() string {
			return strings.Replace(valid, `"issuer":`, `"issuer":"https://duplicate.example.invalid","issuer":`, 1)
		},
		"unknown member": func() string {
			return strings.Replace(valid, `"contract":`, `"unknown":true,"contract":`, 1)
		},
		"invalid duration": func() string {
			return strings.Replace(valid, `"maximumLifetime":"1h"`, `"maximumLifetime":"forever"`, 1)
		},
		"invalid provider": func() string {
			return strings.Replace(valid, `"id":"primary"`, `"id":""`, 1)
		},
		"invalid provider registry digest": func() string {
			return strings.Replace(valid, strings.Repeat("1", sha256.Size*2), "invalid", 1)
		},
		"missing duration": func() string {
			return strings.Replace(valid, `"clockSkew":"30s",`, ``, 1)
		},
		"invalid nonce lifetime": func() string {
			return strings.Replace(valid, `"lifetime":"1m"`, `"lifetime":"1h"`, 1)
		},
		"invalid nonce overlap": func() string {
			return strings.Replace(valid, `"maximumActivePerKey":4`, `"maximumActivePerKey":0`, 1)
		},
		"null nonce policy": func() string {
			return strings.Replace(
				valid,
				`"nonce":{"lifetime":"1m","maximumActivePerKey":4}`,
				`"nonce":null`,
				1,
			)
		},
		"unknown nonce member": func() string {
			return strings.Replace(valid, `"nonce":{`, `"nonce":{"unknown":true,`, 1)
		},
		"unsupported admission generation": func() string {
			return strings.Replace(
				valid,
				`"generation":1`,
				`"generation":18446744073709551615`,
				1,
			)
		},
		"trailing data": func() string { return valid + `{}` },
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := writeStartupFile(t, t.TempDir(), "security.json", mutate())
			if _, _, err := loadOIDCSecurityConfigurationForBuild(
				path,
				"",
				"",
				"0123456789abcdef0123456789abcdef01234567",
				"go1.26.5",
				time.Now().UTC(),
			); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}

	unsafePath := writeStartupFile(t, directory, "unsafe.json", valid)
	if err := os.Chmod(unsafePath, 0o666); err != nil {
		t.Fatalf("make configuration unsafe: %v", err)
	}
	if _, _, err := loadOIDCSecurityConfigurationForBuild(
		unsafePath,
		"",
		"",
		"0123456789abcdef0123456789abcdef01234567",
		"go1.26.5",
		time.Now().UTC(),
	); err == nil {
		t.Fatal("group-writable configuration was accepted")
	}
	symlink := filepath.Join(directory, "security-link.json")
	if err := os.Symlink(writeStartupFile(t, directory, "target.json", valid), symlink); err != nil {
		t.Fatalf("create configuration symlink: %v", err)
	}
	if _, _, err := loadOIDCSecurityConfigurationForBuild(
		symlink,
		"",
		"",
		"0123456789abcdef0123456789abcdef01234567",
		"go1.26.5",
		time.Now().UTC(),
	); err == nil {
		t.Fatal("configuration symlink was accepted")
	}
}

func TestLoadOIDCSecurityConfigurationRejectsUnboundCapacityEvidence(t *testing.T) {
	t.Parallel()
	const revision = "0123456789abcdef0123456789abcdef01234567"
	directory := t.TempDir()
	keysets := writeStartupFile(t, directory, "keysets.json", `{}`)
	policy := writeStartupFile(t, directory, "api.cedar", `permit(principal, action, resource);`)
	evidence, evidenceHash := writeStartupCapacityEvidence(t, directory)

	load := func(configuration, sourceRevision, goVersion string, certify bool) error {
		testDirectory := t.TempDir()
		path := writeStartupFile(t, testDirectory, "security.json", configuration)
		certification, trust := "", ""
		if certify {
			certification, trust = writeStartupReleaseCertification(
				t,
				testDirectory,
				path,
				policy,
				evidence,
				sourceRevision,
				goVersion,
				time.Now().UTC(),
			)
		}
		_, policyBytes, err := loadOIDCSecurityConfigurationForBuild(
			path,
			certification,
			trust,
			sourceRevision,
			goVersion,
			time.Now().UTC(),
		)
		clear(policyBytes)
		return err
	}
	valid := startupConfiguration(keysets, policy, evidence, evidenceHash)
	if err := load(valid, revision, "go1.26.5", true); err != nil {
		t.Fatalf("load bound capacity evidence: %v", err)
	}
	if err := load(valid, "1123456789abcdef0123456789abcdef01234567", "go1.26.5", false); err == nil {
		t.Fatal("evidence from another revision was accepted")
	}
	if err := load(valid, revision, "go1.26.6", false); err == nil {
		t.Fatal("evidence from another Go runtime was accepted")
	}
	wrongHash := strings.Repeat("0", sha256.Size*2)
	if err := load(startupConfiguration(keysets, policy, evidence, wrongHash), revision, "go1.26.5", false); err == nil {
		t.Fatal("evidence with a mismatched digest was accepted")
	}
	if err := os.Chmod(evidence, 0o640); err != nil {
		t.Fatalf("make capacity evidence group-readable: %v", err)
	}
	if err := load(valid, revision, "go1.26.5", false); err == nil {
		t.Fatal("non-owner-only capacity evidence was accepted")
	}
}

func TestLoadOIDCSecurityConfigurationRejectsUnsignedOrMismatchedCertification(t *testing.T) {
	t.Parallel()
	const revision = "0123456789abcdef0123456789abcdef01234567"
	directory := t.TempDir()
	keysets := writeStartupFile(t, directory, "keysets.json", `{}`)
	policy := writeStartupFile(t, directory, "api.cedar", `permit(principal, action, resource);`)
	evidence, evidenceHash := writeStartupCapacityEvidence(t, directory)
	configurationPath := writeStartupFile(
		t,
		directory,
		"security.json",
		startupConfiguration(keysets, policy, evidence, evidenceHash),
	)
	now := time.Now().UTC()
	certification, trust := writeStartupReleaseCertification(
		t, directory, configurationPath, policy, evidence, revision, "go1.26.5", now,
	)

	if _, _, err := loadOIDCSecurityConfigurationForBuild(
		configurationPath, "", trust, revision, "go1.26.5", now,
	); err == nil {
		t.Fatal("missing release certification was accepted")
	}
	otherConfiguration := writeStartupFile(
		t,
		directory,
		"other-security.json",
		startupConfiguration(keysets, policy, evidence, evidenceHash),
	)
	if _, _, err := loadOIDCSecurityConfigurationForBuild(
		otherConfiguration, certification, trust, revision, "go1.26.5", now,
	); err == nil {
		t.Fatal("certification for another configuration path was accepted")
	}
}

func TestOIDCSecurityRequiresExplicitLoopbackListener(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"127.0.0.1:8080", "[::1]:8080"} {
		if err := requireExplicitLoopbackAddress(address); err != nil {
			t.Fatalf("loopback address %q rejected: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", "localhost:8080", ":8080", "invalid"} {
		if err := requireExplicitLoopbackAddress(address); err == nil {
			t.Fatalf("non-explicit loopback address %q accepted", address)
		}
	}
}

func startupConfiguration(keysets, policy, evidence, evidenceHash string) string {
	configuration := map[string]any{
		"contract":              oidcSecurityConfigurationContract,
		"issuer":                "https://identity.example.invalid",
		"externalOrigin":        "https://api.example.invalid",
		"keysetPublicationFile": keysets,
		"algorithms":            []string{"EdDSA"},
		"provider": map[string]any{
			"id": "primary", "registrySha256": strings.Repeat("1", sha256.Size*2),
		},
		"jwt": map[string]any{
			"clockSkew": "30s", "maximumLifetime": "1h",
		},
		"dpop": map[string]any{
			"clockSkew": "30s", "maximumProofAge": "1m",
			"nonce": map[string]any{"lifetime": "1m", "maximumActivePerKey": 4},
		},
		"keysetRefresh": map[string]any{
			"interval": "1m", "timeout": "5s",
		},
		"admission": map[string]any{
			"generation": uint64(1), "window": "1m", "globalBurst": 100, "isolationDomainBurst": 20, "credentialBurst": 10,
			"deploymentProfile": "team", "capacityEvidenceFile": evidence, "capacityEvidenceSha256": evidenceHash,
		},
		"authorization": map[string]any{
			"policySetId": "deployment-api", "policyFile": policy,
		},
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func writeStartupCapacityEvidence(t *testing.T, directory string) (string, string) {
	return writeStartupCapacityEvidenceWithNonce(t, directory, true)
}

func writeStartupCapacityEvidenceWithNonce(t *testing.T, directory string, nonceEnabled bool) (string, string) {
	t.Helper()
	policyDigest := startupPolicyDigest(time.Minute, 100, 20, 10)
	bursts := []uint32{10, 20, 100}
	names := []string{"credential", "isolation-domain", "global"}
	phases := make([]map[string]any, 0, len(names))
	for index, name := range names {
		phases = append(phases, map[string]any{
			"name": name, "generation": index + 1, "attempts": 200, "workers": 20,
			"allowed": bursts[index], "denied": 200 - bursts[index],
			"durationNanoseconds":       int64(100 * time.Millisecond),
			"p50LatencyNanoseconds":     int64(time.Millisecond),
			"p95LatencyNanoseconds":     int64(2 * time.Millisecond),
			"p99LatencyNanoseconds":     int64(3 * time.Millisecond),
			"maximumLatencyNanoseconds": int64(4 * time.Millisecond),
			"completedPerSecondMilli":   2_000_000,
			"p99LatencyAccepted":        true, "minimumThroughputAccepted": true,
		})
	}
	nonceNames := []string{"nonce-issue-shared-key", "nonce-issue-distinct-keys", "nonce-validate"}
	nonceChallenges := []uint32{200, 200, 0}
	nonceValidated := []uint32{0, 0, 200}
	nonceRows := []uint32{4, 200, 20}
	noncePhases := make([]map[string]any, 0, len(nonceNames))
	for index, name := range nonceNames {
		if !nonceEnabled {
			break
		}
		noncePhases = append(noncePhases, map[string]any{
			"name": name, "attempts": 200, "workers": 20,
			"challenges": nonceChallenges[index], "validated": nonceValidated[index], "activeRows": nonceRows[index],
			"durationNanoseconds":       int64(100 * time.Millisecond),
			"p50LatencyNanoseconds":     int64(time.Millisecond),
			"p95LatencyNanoseconds":     int64(2 * time.Millisecond),
			"p99LatencyNanoseconds":     int64(3 * time.Millisecond),
			"maximumLatencyNanoseconds": int64(4 * time.Millisecond),
			"completedPerSecondMilli":   2_000_000,
			"p99LatencyAccepted":        true, "minimumThroughputAccepted": true,
			"lifetimeAccepted": true, "activeRowsAccepted": true,
		})
	}
	evidence := map[string]any{
		"contract":                 "dataground.authentication-rate-limit-capacity-evidence/v3",
		"runId":                    "cap_0123456789abcdefghij",
		"sourceRevision":           "0123456789abcdef0123456789abcdef01234567",
		"deploymentProfile":        "team",
		"databaseName":             "dataground_capacity",
		"goVersion":                "go1.26.5",
		"postgresqlServerVersion":  180005,
		"postgresqlMaxConnections": 100,
		"recordedAt":               time.Now().UTC().Format(time.RFC3339Nano),
		"accepted":                 true,
		"policy": map[string]any{
			"windowNanoseconds": int64(time.Minute), "globalBurst": 100,
			"isolationDomainBurst": 20, "credentialBurst": 10,
			"canonicalPolicyDigestHex": policyDigest,
		},
		"maximumP99LatencyNanoseconds": int64(100 * time.Millisecond),
		"minimumThroughputPerSecond":   50,
		"phases":                       phases,
		"dpopNonce":                    map[string]any{"enabled": nonceEnabled},
		"dpopNoncePhases":              noncePhases,
	}
	if nonceEnabled {
		evidence["dpopNonce"] = map[string]any{
			"enabled": true, "lifetimeNanoseconds": int64(time.Minute), "maximumActivePerKey": 4,
			"attemptsPerPhase": 200, "workers": 20,
			"maximumP99LatencyNanoseconds": int64(100 * time.Millisecond), "minimumThroughputPerSecond": 50,
		}
	}
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatalf("encode capacity evidence: %v", err)
	}
	encoded = append(encoded, '\n')
	path := writeStartupFile(t, directory, "capacity.json", string(encoded))
	digest := sha256.Sum256(encoded)
	return path, hex.EncodeToString(digest[:])
}

func writeStartupReleaseCertification(
	t *testing.T,
	directory string,
	configurationPath string,
	policyPath string,
	evidencePath string,
	sourceRevision string,
	goVersion string,
	now time.Time,
) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate release certification key: %v", err)
	}
	trust := releasecert.TrustProfile{
		Contract: releasecert.TrustContract,
		Keys: []releasecert.TrustedKey{{
			KeyID:     "release_test_key",
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		}},
	}
	trustPath := writeStartupCanonicalFile(t, directory, "release-trust.json", trust)
	trustDigest := startupFileDigest(t, trustPath)
	statement := releasecert.Statement{
		Contract:           releasecert.StatementContract,
		ReleaseID:          "release_test_01",
		SourceRevision:     sourceRevision,
		GoVersion:          goVersion,
		DeploymentProfile:  "team",
		TrustProfileSHA256: trustDigest,
		IssuedAt:           now.Add(-time.Minute).Format(time.RFC3339Nano),
		ExpiresAt:          now.Add(time.Hour).Format(time.RFC3339Nano),
		ReviewerID:         "reviewer_test",
		Reason:             "reviewed test release evidence",
		Artifacts: []releasecert.Artifact{
			{Kind: "admission-capacity-evidence", File: evidencePath, SHA256: startupFileDigest(t, evidencePath)},
			{Kind: "api-authorization-policy", File: policyPath, SHA256: startupFileDigest(t, policyPath)},
			{Kind: "oidc-security-configuration", File: configurationPath, SHA256: startupFileDigest(t, configurationPath)},
		},
	}
	statementPath := writeStartupCanonicalFile(t, directory, "release-statement.json", statement)
	messagePath := filepath.Join(directory, "release-signing-message")
	if err := releasecert.PrepareSigningMessage(releasecert.PrepareRequest{
		StatementFile:      statementPath,
		TrustProfileFile:   trustPath,
		SigningMessageFile: messagePath,
		SourceRevision:     sourceRevision,
		GoVersion:          goVersion,
		Now:                now,
	}); err != nil {
		t.Fatalf("prepare release certification signing message: %v", err)
	}
	message, err := os.ReadFile(messagePath)
	if err != nil {
		t.Fatalf("read release certification signing message: %v", err)
	}
	signaturePath := writeStartupCanonicalFile(t, directory, "release-signature.json", releasecert.Signature{
		Contract:  releasecert.SignatureContract,
		KeyID:     "release_test_key",
		Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, message)),
	})
	certificationPath := filepath.Join(directory, "release-certification.json")
	if err := releasecert.Install(releasecert.InstallRequest{
		StatementFile:    statementPath,
		SignatureFile:    signaturePath,
		TrustProfileFile: trustPath,
		OutputFile:       certificationPath,
		SourceRevision:   sourceRevision,
		GoVersion:        goVersion,
		Now:              now,
	}); err != nil {
		t.Fatalf("install release certification: %v", err)
	}
	return certificationPath, trustPath
}

func writeStartupCanonicalFile(t *testing.T, directory, name string, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	encoded = append(encoded, '\n')
	return writeStartupFile(t, directory, name, string(encoded))
}

func startupFileDigest(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func startupPolicyDigest(window time.Duration, global, domain, credential uint32) string {
	var encoded [20]byte
	binary.BigEndian.PutUint64(encoded[0:8], uint64(window))
	binary.BigEndian.PutUint32(encoded[8:12], global)
	binary.BigEndian.PutUint32(encoded[12:16], domain)
	binary.BigEndian.PutUint32(encoded[16:20], credential)
	hash := sha256.New()
	_, _ = hash.Write([]byte("dataground:authentication-rate-limit:policy:v1\x00"))
	_, _ = hash.Write(encoded[:])
	return hex.EncodeToString(hash.Sum(nil))
}

func writeStartupFile(t *testing.T, directory, name, content string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}
