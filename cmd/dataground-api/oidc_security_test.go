package main

import (
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
	configurationPath := writeStartupFile(t, directory, "security.json", startupConfiguration(keysets, policy))

	configuration, policyBytes, err := loadOIDCSecurityConfiguration(configurationPath)
	if err != nil {
		t.Fatalf("load OIDC security configuration: %v", err)
	}
	if configuration.Contract != oidcSecurityConfigurationContract ||
		configuration.KeysetPublicationFile != keysets ||
		configuration.Authorization.PolicyFile != policy ||
		configuration.JWT.MaximumLifetime.value != time.Hour ||
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
	valid := startupConfiguration(keysets, policy)

	tests := map[string]func() string{
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
		"trailing data": func() string { return valid + `{}` },
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := writeStartupFile(t, t.TempDir(), "security.json", mutate())
			if _, _, err := loadOIDCSecurityConfiguration(path); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}

	unsafePath := writeStartupFile(t, directory, "unsafe.json", valid)
	if err := os.Chmod(unsafePath, 0o666); err != nil {
		t.Fatalf("make configuration unsafe: %v", err)
	}
	if _, _, err := loadOIDCSecurityConfiguration(unsafePath); err == nil {
		t.Fatal("group-writable configuration was accepted")
	}
	symlink := filepath.Join(directory, "security-link.json")
	if err := os.Symlink(writeStartupFile(t, directory, "target.json", valid), symlink); err != nil {
		t.Fatalf("create configuration symlink: %v", err)
	}
	if _, _, err := loadOIDCSecurityConfiguration(symlink); err == nil {
		t.Fatal("configuration symlink was accepted")
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

func startupConfiguration(keysets, policy string) string {
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
			"window": "1m", "globalBurst": 100, "isolationDomainBurst": 20, "credentialBurst": 10,
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

func writeStartupFile(t *testing.T, directory, name, content string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}
