package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestRuntimeCertificationVerifierUsesExactScopeAndReplayState(t *testing.T) {
	t.Parallel()
	config, err := loadRuntimeCertificationConfig(mapEnvironment(validGovernedEnvironment()))
	if err != nil {
		t.Fatalf("load runtime certification: %v", err)
	}
	var program string
	var arguments []string
	checker, err := newRuntimeCertificationChecker(config, nodeRuntimeCertificationVerifier{
		run: func(_ context.Context, name string, values ...string) error {
			program = name
			arguments = slices.Clone(values)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("new runtime certification checker: %v", err)
	}
	if err := checker.Check(context.Background()); err != nil {
		t.Fatalf("check runtime certification: %v", err)
	}
	if program != "node" {
		t.Fatalf("verifier program = %q", program)
	}
	for _, required := range []string{
		runtimeCertificationVerifier,
		config.manifestFile,
		config.evidenceFile,
		config.acceptanceFile,
		config.target.isolationDomainID,
		config.target.serviceID,
		config.target.revisionID,
		governedCertificationProfile,
		config.sourceRevision,
		config.manifestSHA256,
		"3",
		"rtcert_0123456789abcdefghij",
		"rtcert_abcdefghij0123456789",
	} {
		if !slices.Contains(arguments, required) {
			t.Fatalf("verifier arguments do not contain %q: %#v", required, arguments)
		}
	}
}

func TestRuntimeCertificationReadinessIsRevalidatedAfterFailureAndRestart(t *testing.T) {
	t.Parallel()
	config, err := loadRuntimeCertificationConfig(mapEnvironment(validGovernedEnvironment()))
	if err != nil {
		t.Fatalf("load runtime certification: %v", err)
	}
	calls := 0
	verifier := nodeRuntimeCertificationVerifier{
		run: func(context.Context, string, ...string) error {
			calls++
			if calls == 2 {
				return errors.New("withdrawn")
			}
			return nil
		},
	}
	checker, err := newRuntimeCertificationChecker(config, verifier)
	if err != nil {
		t.Fatalf("new checker: %v", err)
	}
	if err := checker.Check(context.Background()); err != nil {
		t.Fatalf("initial readiness: %v", err)
	}
	if err := checker.Check(context.Background()); !errors.Is(err, ErrRuntimeCertificationUnavailable) {
		t.Fatalf("changed readiness error = %v", err)
	}
	restarted, err := newRuntimeCertificationChecker(config, verifier)
	if err != nil {
		t.Fatalf("restart checker: %v", err)
	}
	if err := restarted.Check(context.Background()); err != nil {
		t.Fatalf("restart revalidation: %v", err)
	}
	if calls != 3 {
		t.Fatalf("verifier calls = %d, want 3", calls)
	}
}

func TestCertifiedInvocationAuthorizerRejectsSubstitutedScopeBeforeDelegate(t *testing.T) {
	t.Parallel()
	config, err := loadRuntimeCertificationConfig(mapEnvironment(validGovernedEnvironment()))
	if err != nil {
		t.Fatalf("load runtime certification: %v", err)
	}
	checker, err := newRuntimeCertificationChecker(config, nodeRuntimeCertificationVerifier{
		run: func(context.Context, string, ...string) error { return nil },
	})
	if err != nil {
		t.Fatalf("new checker: %v", err)
	}
	delegate := &recordingInvocationAuthorizer{}
	authorizer := &certifiedInvocationAuthorizer{
		delegate: delegate, readiness: checker, target: config.target,
	}
	target := persistence.InvocationAdmissionTarget{
		IsolationDomainID: config.target.isolationDomainID,
		ServiceID:         config.target.serviceID,
		RevisionID:        "rev_abcdefghij0123456789",
	}
	err = authorizer.AuthorizeInvocationAdmission(context.Background(), target)
	if !errors.Is(err, reconcile.ErrEffectInvalid) ||
		!errors.Is(err, ErrRuntimeCertificationScopeMismatch) {
		t.Fatalf("substituted scope error = %v", err)
	}
	if delegate.calls != 0 {
		t.Fatalf("delegate calls = %d", delegate.calls)
	}
}

func TestRuntimeCertificationConfigRejectsMissingOrMalformedActivationState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{
			name: "missing manifest",
			mutate: func(values map[string]string) {
				delete(values, "DATAGROUND_RUNTIME_CERTIFICATION_MANIFEST")
			},
		},
		{
			name: "absolute evidence",
			mutate: func(values map[string]string) {
				values["DATAGROUND_RUNTIME_CONFORMANCE_EVIDENCE"] = "/tmp/evidence.json"
			},
		},
		{
			name: "invalid digest",
			mutate: func(values map[string]string) {
				values["DATAGROUND_RUNTIME_CERTIFICATION_SHA256"] = "sha256:bad"
			},
		},
		{
			name: "zero generation",
			mutate: func(values map[string]string) {
				values["DATAGROUND_RUNTIME_CERTIFICATION_MINIMUM_GENERATION"] = "0"
			},
		},
		{
			name: "invalid rejected id",
			mutate: func(values map[string]string) {
				values["DATAGROUND_RUNTIME_CERTIFICATION_REJECTED_IDS"] = "rtcert_bad"
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values := validGovernedEnvironment()
			test.mutate(values)
			if _, err := loadRuntimeCertificationConfig(mapEnvironment(values)); err == nil {
				t.Fatal("invalid runtime certification activation was accepted")
			}
		})
	}
}

type recordingInvocationAuthorizer struct {
	calls int
}

func (authorizer *recordingInvocationAuthorizer) AuthorizeInvocationAdmission(
	context.Context,
	persistence.InvocationAdmissionTarget,
) error {
	authorizer.calls++
	return nil
}

func (authorizer *recordingInvocationAuthorizer) AuthorizeInvocationRuntime(
	context.Context,
	persistence.InvocationRuntimeTarget,
	dgruntime.StartRequest,
) error {
	authorizer.calls++
	return nil
}

func (authorizer *recordingInvocationAuthorizer) AuthorizeInvocationApproval(
	context.Context,
	persistence.InvocationRuntimeApproval,
	string,
) error {
	authorizer.calls++
	return nil
}

func (authorizer *recordingInvocationAuthorizer) AuthorizeInvocationCancellation(
	context.Context,
	persistence.InvocationCancellationTarget,
) error {
	authorizer.calls++
	return nil
}
