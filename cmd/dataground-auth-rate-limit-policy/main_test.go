package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadActivationRequestAcceptsClosedOwnerOnlyInput(t *testing.T) {
	t.Parallel()

	path := writeActivationRequest(t, validActivationRequest())
	request, err := readActivationRequest(context.Background(), path)
	if err != nil {
		t.Fatalf("read activation request: %v", err)
	}
	if request.Contract != authenticationRateLimitPolicyContract ||
		request.Generation != 1 || request.Window.value != time.Minute ||
		request.GlobalBurst != 100 || request.DomainBurst != 20 ||
		request.CredentialBurst != 10 {
		t.Fatalf("activation request = %#v", request)
	}
}

func TestReadActivationRequestRejectsAmbiguousOrUnsafeInput(t *testing.T) {
	t.Parallel()

	valid := validActivationRequest()
	for name, content := range map[string]string{
		"duplicate": strings.Replace(valid, `"generation":1`, `"generation":1,"generation":2`, 1),
		"unknown":   strings.Replace(valid, `"contract":`, `"unknown":true,"contract":`, 1),
		"missing window": strings.Replace(valid, `"window":"1m",`, "", 1),
		"trailing":  valid + `{}`,
	} {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := readActivationRequest(
				context.Background(),
				writeActivationRequest(t, content),
			); err == nil {
				t.Fatal("invalid activation request was accepted")
			}
		})
	}

	unsafe := writeActivationRequest(t, valid)
	if err := os.Chmod(unsafe, 0o620); err != nil {
		t.Fatal(err)
	}
	if _, err := readActivationRequest(context.Background(), unsafe); err == nil {
		t.Fatal("group-writable activation request was accepted")
	}

	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "request.json")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := readActivationRequest(context.Background(), symlink); err == nil {
		t.Fatal("activation request symlink was accepted")
	}
}

func writeActivationRequest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "activation.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validActivationRequest() string {
	return `{
		"contract":"dataground.authentication-rate-limit-policy/v1",
		"generation":1,
		"window":"1m",
		"globalBurst":100,
		"isolationDomainBurst":20,
		"credentialBurst":10,
		"actorId":"usr_0123456789abcdefghij",
		"reason":"reviewed initial admission policy",
		"correlationId":"cor_0123456789abcdefghij"
	}`
}
