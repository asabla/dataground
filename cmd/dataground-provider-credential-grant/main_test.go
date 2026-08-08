package main

import (
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func TestParseProviderCredentialGrantChange(t *testing.T) {
	activated := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	expires := activated.Add(time.Hour)
	common := []string{
		"-isolation-domain", "iso_00000000000000000001",
		"-revision", "rev_00000000000000000001",
		"-provider-profile", "codex",
		"-actor", "operator-one",
		"-reason", "authorize the reviewed OpenShell profile",
	}
	activateArgs := append(append([]string{}, common...),
		"-operation", "activate",
		"-generation", "1",
		"-activated-at", activated.Format(time.RFC3339Nano),
		"-expires-at", expires.Format(time.RFC3339Nano),
		"-correlation-id", "cor_00000000000000000001",
	)
	change, err := parseProviderCredentialGrantChange(activateArgs)
	if err != nil {
		t.Fatalf("parse activation: %v", err)
	}
	if change.Operation != "activate" || change.Purpose != persistence.ProviderCredentialPurposeAgentInference ||
		change.ProviderProfile != "codex" || len(change.ReasonDigest) != 32 {
		t.Fatalf("activation = %#v", change)
	}

	revokeArgs := append(append([]string{}, common...),
		"-operation", "revoke",
		"-generation", "2",
		"-correlation-id", "cor_00000000000000000002",
	)
	change, err = parseProviderCredentialGrantChange(revokeArgs)
	if err != nil || change.Operation != "revoke" || !change.ActivatedAt.IsZero() {
		t.Fatalf("parse revocation = (%#v, %v)", change, err)
	}
}

func TestParseProviderCredentialGrantChangeRejectsAmbiguousInputs(t *testing.T) {
	base := []string{
		"-operation", "revoke",
		"-isolation-domain", "iso_00000000000000000001",
		"-revision", "rev_00000000000000000001",
		"-provider-profile", "codex",
		"-generation", "2",
		"-actor", "operator-one",
		"-reason", "revoke the profile",
		"-correlation-id", "cor_00000000000000000001",
	}
	tests := [][]string{
		append(append([]string{}, base...), "-expires-at", time.Now().UTC().Format(time.RFC3339Nano)),
		append(append([]string{}, base...), "-provider-profile", "codex=secret"),
		append(append([]string{}, base...), "-reason", " reason"),
		append(append([]string{}, base...), "trailing"),
	}
	for _, arguments := range tests {
		if _, err := parseProviderCredentialGrantChange(arguments); err == nil {
			t.Fatalf("ambiguous arguments accepted: %q", arguments)
		}
	}
}
