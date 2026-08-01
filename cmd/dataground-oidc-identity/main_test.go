package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/authn"
)

func TestReadOIDCIdentityRequestAcceptsStrictOwnerOnlyOperations(t *testing.T) {
	t.Parallel()

	for name, document := range map[string]string{
		"register": `{
  "operation": "register",
  "isolationDomainId": "iso_00000000000000000001",
  "issuer": "https://identity.example.invalid/realms/dataground",
  "subject": "subject-0001",
  "principalId": "usr_00000000000000000001",
  "principalKind": "human",
  "actorId": "usr_00000000000000000002",
  "reason": "initial identity registration",
  "correlationId": "cor_00000000000000000001"
}`,
		"revoke": `{
  "operation": "revoke",
  "isolationDomainId": "iso_00000000000000000001",
  "issuer": "https://identity.example.invalid/realms/dataground",
  "subject": "subject-0001",
  "principalId": "usr_00000000000000000001",
  "actorId": "usr_00000000000000000002",
  "reason": "remove domain access",
  "correlationId": "cor_00000000000000000002"
}`,
	} {
		name, document := name, document
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := writeRequestFile(t, document, 0o600)
			request, err := readOIDCIdentityRequest(path)
			if err != nil {
				t.Fatalf("read request: %v", err)
			}
			if request.Operation != name {
				t.Fatalf("operation = %q, want %q", request.Operation, name)
			}
			if name == "register" && request.PrincipalKind != authn.PrincipalHuman {
				t.Fatalf("principal kind = %q", request.PrincipalKind)
			}
		})
	}
}

func TestReadOIDCIdentityRequestRejectsUnsafeDocuments(t *testing.T) {
	t.Parallel()

	valid := `{
  "operation": "register",
  "isolationDomainId": "iso_00000000000000000001",
  "issuer": "https://identity.example.invalid/realms/dataground",
  "subject": "subject-0001",
  "principalId": "usr_00000000000000000001",
  "principalKind": "human",
  "actorId": "usr_00000000000000000002",
  "reason": "initial identity registration",
  "correlationId": "cor_00000000000000000001"
}`
	for name, document := range map[string]string{
		"unknown field":   strings.Replace(valid, `"operation": "register"`, `"operation": "register", "roles": ["admin"]`, 1),
		"trailing data":   valid + ` {}`,
		"invalid issuer":  strings.Replace(valid, "https://identity.example.invalid", "http://identity.example.invalid", 1),
		"control reason":  strings.Replace(valid, "initial identity registration", "initial\\tidentity registration", 1),
		"revocation kind": strings.Replace(valid, `"operation": "register"`, `"operation": "revoke"`, 1),
	} {
		name, document := name, document
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := readOIDCIdentityRequest(writeRequestFile(t, document, 0o600)); err == nil {
				t.Fatal("unsafe request was accepted")
			}
		})
	}

	if _, err := readOIDCIdentityRequest(writeRequestFile(t, valid, 0o644)); err == nil {
		t.Fatal("group-readable request was accepted")
	}
}

func writeRequestFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}
