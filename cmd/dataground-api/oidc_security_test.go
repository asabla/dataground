package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

	configuration, policyBytes, err := loadOIDCSecurityConfigurationForBuild(
		configurationPath,
		"0123456789abcdef0123456789abcdef01234567",
		"go1.26.5",
	)
	if err != nil {
		t.Fatalf("load OIDC security configuration: %v", err)
	}
	if configuration.Contract != oidcSecurityConfigurationContract ||
		configuration.KeysetPublicationFile != keysets ||
		configuration.Authorization.PolicyFile != policy ||
		configuration.JWT.MaximumLifetime.value != time.Hour ||
		configuration.Admission.Generation != 1 ||
		configuration.Admission.CredentialBurst != 10 {
		t.Fatalf("loaded configuration = %#v", configuration)
	}
	if string(policyBytes) != `permit(principal, action, resource);` {
		t.Fatalf("loaded policy = %q", policyBytes)
	}
	clear(policyBytes)
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
			return strings.Replace(valid, oidcSecurityConfigurationContract, "dataground.api-security/oidc-dpop/v1", 1)
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
		"missing duration": func() string {
			return strings.Replace(valid, `"clockSkew":"30s",`, ``, 1)
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
				"0123456789abcdef0123456789abcdef01234567",
				"go1.26.5",
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
		"0123456789abcdef0123456789abcdef01234567",
		"go1.26.5",
	); err == nil {
		t.Fatal("group-writable configuration was accepted")
	}
	symlink := filepath.Join(directory, "security-link.json")
	if err := os.Symlink(writeStartupFile(t, directory, "target.json", valid), symlink); err != nil {
		t.Fatalf("create configuration symlink: %v", err)
	}
	if _, _, err := loadOIDCSecurityConfigurationForBuild(
		symlink,
		"0123456789abcdef0123456789abcdef01234567",
		"go1.26.5",
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

	load := func(configuration, sourceRevision, goVersion string) error {
		path := writeStartupFile(t, t.TempDir(), "security.json", configuration)
		_, policyBytes, err := loadOIDCSecurityConfigurationForBuild(path, sourceRevision, goVersion)
		clear(policyBytes)
		return err
	}
	valid := startupConfiguration(keysets, policy, evidence, evidenceHash)
	if err := load(valid, revision, "go1.26.5"); err != nil {
		t.Fatalf("load bound capacity evidence: %v", err)
	}
	if err := load(valid, "1123456789abcdef0123456789abcdef01234567", "go1.26.5"); err == nil {
		t.Fatal("evidence from another revision was accepted")
	}
	if err := load(valid, revision, "go1.26.6"); err == nil {
		t.Fatal("evidence from another Go runtime was accepted")
	}
	wrongHash := strings.Repeat("0", sha256.Size*2)
	if err := load(startupConfiguration(keysets, policy, evidence, wrongHash), revision, "go1.26.5"); err == nil {
		t.Fatal("evidence with a mismatched digest was accepted")
	}
	if err := os.Chmod(evidence, 0o640); err != nil {
		t.Fatalf("make capacity evidence group-readable: %v", err)
	}
	if err := load(valid, revision, "go1.26.5"); err == nil {
		t.Fatal("non-owner-only capacity evidence was accepted")
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
		"jwt": map[string]any{
			"clockSkew": "30s", "maximumLifetime": "1h",
		},
		"dpop": map[string]any{
			"clockSkew": "30s", "maximumProofAge": "1m",
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
	evidence := map[string]any{
		"contract":                 "dataground.authentication-rate-limit-capacity-evidence/v2",
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
