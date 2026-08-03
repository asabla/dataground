package main

import "testing"

func TestRunRejectsIncompleteModes(t *testing.T) {
	for _, arguments := range [][]string{
		nil,
		{"-trust-file", "/tmp/trust.json"},
		{"-trust-file", "/tmp/trust.json", "-verify-file", "/tmp/envelope.json", "-export-file", "/tmp/export.json"},
		{"-trust-file", "/tmp/trust.json", "-signing-message-file", "/tmp/message", "-signature-file", "/tmp/signature.json"},
		{"-trust-file", "/tmp/trust.json", "-export-file", "/tmp/export.json", "-signature-file", "/tmp/signature.json"},
	} {
		if err := run(arguments); err == nil {
			t.Fatalf("run(%q) succeeded", arguments)
		}
	}
}
