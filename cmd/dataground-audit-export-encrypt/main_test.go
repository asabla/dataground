package main

import (
	"context"
	"testing"
)

func TestParseEncryptionArguments(t *testing.T) {
	common := []string{
		"-envelope-file", "/run/dataground/audit/envelope.json",
		"-trust-file", "/run/dataground/audit/export-trust.json",
		"-recipient-trust-file", "/run/dataground/audit/recipient-trust.json",
	}
	encrypt := append(append([]string(nil), common...),
		"-recipient-encryption-key", "archive_encryption_key_01",
		"-output-file", "/run/dataground/audit/encrypted.json",
	)
	request, err := parseArguments(encrypt)
	if err != nil || request.verifyFile != "" || request.outputFile == "" {
		t.Fatalf("parse encryption arguments: %#v %v", request, err)
	}
	verify := append(append([]string(nil), common...),
		"-verify-file", "/run/dataground/audit/encrypted.json",
	)
	request, err = parseArguments(verify)
	if err != nil || request.verifyFile == "" || request.outputFile != "" {
		t.Fatalf("parse verification arguments: %#v %v", request, err)
	}
}

func TestParseEncryptionArgumentsRejectsMixedModes(t *testing.T) {
	_, err := parseArguments([]string{
		"-envelope-file", "/run/dataground/audit/envelope.json",
		"-trust-file", "/run/dataground/audit/export-trust.json",
		"-recipient-trust-file", "/run/dataground/audit/recipient-trust.json",
		"-verify-file", "/run/dataground/audit/encrypted.json",
		"-output-file", "/run/dataground/audit/other.json",
	})
	if err == nil {
		t.Fatal("mixed encryption modes were accepted")
	}
}

func TestRunRejectsCancelledContextBeforeFileAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := run(ctx, []string{
		"-envelope-file", "/run/dataground/audit/envelope.json",
		"-trust-file", "/run/dataground/audit/export-trust.json",
		"-recipient-trust-file", "/run/dataground/audit/recipient-trust.json",
		"-verify-file", "/run/dataground/audit/encrypted.json",
	})
	if err == nil {
		t.Fatal("cancelled encryption context was accepted")
	}
}
