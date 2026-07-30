package canarylauncher

import (
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	"github.com/asabla/dataground/internal/security/canarycollect"
)

func TestSandboxCreateStageClassifiesIdentityConflict(t *testing.T) {
	if got := sandboxCreateStage(execution.ErrStateConflict); got != FailureStageSandboxCreateConflict {
		t.Fatalf("sandboxCreateStage(ErrStateConflict) = %q, want %q", got, FailureStageSandboxCreateConflict)
	}
}

func TestSandboxCreateStageClassifiesOwnedBoundaryFailures(t *testing.T) {
	cases := []struct {
		err  error
		want FailureStage
	}{
		{openshell.ErrPolicyWorkspaceFailure, FailureStageSandboxCreatePolicy},
		{openshell.ErrProviderFailure, FailureStageSandboxCreateProvider},
		{openshell.ErrProviderObservation, FailureStageSandboxCreateObservation},
	}
	for _, test := range cases {
		if got := sandboxCreateStage(test.err); got != test.want {
			t.Fatalf("sandboxCreateStage(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestProviderSettingsStagePreservesOwnedBoundary(t *testing.T) {
	cases := []struct {
		err  error
		want FailureStage
	}{
		{openshell.ErrProviderSettingsObservation, FailureStageProviderSettingsObservation},
		{openshell.ErrProviderSettingsMutation, FailureStageProviderSettingsMutation},
		{openshell.ErrProviderSettingsVerification, FailureStageProviderSettingsVerification},
	}
	for _, test := range cases {
		if got := providerSettingsStage(test.err); got != test.want {
			t.Fatalf("providerSettingsStage(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestSandboxReadinessStagePreservesTerminalState(t *testing.T) {
	cases := []struct {
		state string
		want  FailureStage
	}{
		{"error", FailureStageSandboxReadyError},
		{"terminated", FailureStageSandboxReadyTerminal},
		{"provisioning", FailureStageSandboxReadyTimeout},
	}
	for _, test := range cases {
		if got := sandboxReadinessStage(&sandboxReadinessError{state: test.state}); got != test.want {
			t.Fatalf("sandboxReadinessStage(%q) = %q, want %q", test.state, got, test.want)
		}
	}
}

func TestCollectionFailureStagePreservesSafeCauseAndSurface(t *testing.T) {
	stage := FailureStageSandboxEnvironment
	for _, test := range []struct {
		err  error
		want FailureStage
	}{
		{canarycollect.ErrCanaryDetected, FailureStage("collection-sandbox-environment-canary-detected")},
		{canarycollect.ErrScanInputLimit, FailureStage("collection-sandbox-environment-input-limit")},
		{canarycollect.ErrSourceClose, FailureStage("collection-sandbox-environment-source-close")},
	} {
		if got := collectionFailureStage(stage, test.err); got != test.want {
			t.Fatalf("collectionFailureStage(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}
