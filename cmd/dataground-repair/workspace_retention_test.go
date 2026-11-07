package main

import (
	"strings"
	"testing"
	"time"
)

func TestRetentionRecoveryRequiresExactRequest(t *testing.T) {
	args := []string{"--isolation-domain", "iso_fixture", "--operation", "op_fixture", "--expected-generation", "7", "--owner-binding", "sha256:" + strings.Repeat("a", 64), "--actor", "operator", "--correlation-id", "cor_recovery", "--reason-sha256", "sha256:" + strings.Repeat("b", 64), "--deadline-at", "2026-11-07T23:15:00Z"}
	input, err := parseRetentionRecovery(args)
	if err != nil || input.ExpectedGeneration != 7 || input.CorrelationID != "cor_recovery" || input.Deadline.Format(time.RFC3339) != "2026-11-07T23:15:00Z" {
		t.Fatal("exact request changed", err)
	}
	for i := 0; i < len(args); i += 2 {
		v := append([]string{}, args[:i]...)
		v = append(v, args[i+2:]...)
		if _, err := parseRetentionRecovery(v); err == nil {
			t.Fatal("missing field accepted", args[i])
		}
	}
	for _, extra := range []string{"unparsed", "--deadline", "--unknown"} {
		if _, err := parseRetentionRecovery(append(append([]string{}, args...), extra)); err == nil {
			t.Fatal("extra input accepted")
		}
	}
}
