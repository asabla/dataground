package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/persistence"
)

func TestParseArgumentsBindsExactPolicyDigest(t *testing.T) {
	t.Parallel()

	digest := strings.Repeat("a", sha256.Size*2)
	request, err := parseArguments([]string{
		"-isolation-domain", "iso_00000000000000000001",
		"-service", "svc_00000000000000000001",
		"-revision", "rev_00000000000000000001",
		"-policy-digest", "sha256:" + digest,
		"-actor", "emergency_revoker",
		"-reason", "policy grants unsafe capability",
		"-correlation-id", "cor_00000000000000000001",
	})
	if err != nil {
		t.Fatalf("parse withdrawal: %v", err)
	}
	if request.isolationDomainID != "iso_00000000000000000001" ||
		request.serviceID != "svc_00000000000000000001" ||
		request.revisionID != "rev_00000000000000000001" ||
		request.actorID != "emergency_revoker" ||
		request.reason != "policy grants unsafe capability" ||
		request.correlationID != "cor_00000000000000000001" ||
		!bytes.Equal(request.policyDigest, bytes.Repeat([]byte{0xaa}, sha256.Size)) {
		t.Fatalf("parsed request = %#v", request)
	}
	withdrawal := newPolicyWithdrawal(request)
	if !withdrawal.Valid() {
		t.Fatalf("constructed withdrawal = %#v", withdrawal)
	}
	if bytes.Equal(withdrawal.ReasonDigest, []byte(request.reason)) {
		t.Fatal("withdrawal retained free-text reason")
	}
}

func TestParseArgumentsRejectsUnsafeInput(t *testing.T) {
	t.Parallel()

	valid := []string{
		"-isolation-domain", "iso_00000000000000000001",
		"-service", "svc_00000000000000000001",
		"-revision", "rev_00000000000000000001",
		"-policy-digest", "sha256:" + strings.Repeat("a", sha256.Size*2),
		"-actor", "emergency_revoker",
		"-reason", "withdraw",
		"-correlation-id", "cor_00000000000000000001",
	}
	tests := []struct {
		name      string
		arguments []string
	}{
		{
			name:      "missing",
			arguments: valid[:len(valid)-2],
		},
		{
			name:      "positional",
			arguments: append(append([]string(nil), valid...), "unexpected"),
		},
		{
			name:      "digest prefix",
			arguments: replaceArgument(valid, "-policy-digest", strings.Repeat("a", 64)),
		},
		{
			name:      "digest length",
			arguments: replaceArgument(valid, "-policy-digest", "sha256:aa"),
		},
		{
			name: "digest uppercase",
			arguments: replaceArgument(
				valid,
				"-policy-digest",
				"sha256:"+strings.Repeat("A", sha256.Size*2),
			),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseArguments(test.arguments); err == nil {
				t.Fatal("unsafe arguments were accepted")
			}
		})
	}
}

func TestExecuteRequestUsesRepositoryBoundary(t *testing.T) {
	t.Parallel()

	expected := persistence.InvocationAuthorizationPolicyWithdrawal{
		Contract:          persistence.InvocationAuthorizationPolicyWithdrawalContract,
		IsolationDomainID: "iso_00000000000000000001",
		ServiceID:         "svc_00000000000000000001",
		RevisionID:        "rev_00000000000000000001",
		PolicyDigest:      bytes.Repeat([]byte{0x11}, sha256.Size),
		WithdrawnBy:       "emergency_revoker",
		ReasonDigest:      bytes.Repeat([]byte{0x22}, sha256.Size),
		CorrelationID:     "cor_00000000000000000001",
	}
	sentinel := errors.New("sentinel")
	repository := policyWithdrawalRepositoryFunc(func(
		_ context.Context,
		got persistence.InvocationAuthorizationPolicyWithdrawal,
	) error {
		if !sameWithdrawal(got, expected) {
			t.Fatalf("withdrawal = %#v, want %#v", got, expected)
		}
		return sentinel
	})
	if err := executeRequest(
		context.Background(),
		repository,
		expected,
	); !errors.Is(err, sentinel) {
		t.Fatalf("execute error = %v", err)
	}
	if err := executeRequest(context.Background(), nil, expected); err == nil {
		t.Fatal("nil repository was accepted")
	}
}

func replaceArgument(arguments []string, name string, value string) []string {
	replaced := append([]string(nil), arguments...)
	for index := range replaced {
		if replaced[index] == name {
			replaced[index+1] = value
			return replaced
		}
	}
	return replaced
}

func sameWithdrawal(
	left persistence.InvocationAuthorizationPolicyWithdrawal,
	right persistence.InvocationAuthorizationPolicyWithdrawal,
) bool {
	return left.Contract == right.Contract &&
		left.IsolationDomainID == right.IsolationDomainID &&
		left.ServiceID == right.ServiceID &&
		left.RevisionID == right.RevisionID &&
		bytes.Equal(left.PolicyDigest, right.PolicyDigest) &&
		left.WithdrawnBy == right.WithdrawnBy &&
		bytes.Equal(left.ReasonDigest, right.ReasonDigest) &&
		left.CorrelationID == right.CorrelationID
}

type policyWithdrawalRepositoryFunc func(
	context.Context,
	persistence.InvocationAuthorizationPolicyWithdrawal,
) error

func (function policyWithdrawalRepositoryFunc) WithdrawInvocationAuthorizationPolicy(
	ctx context.Context,
	withdrawal persistence.InvocationAuthorizationPolicyWithdrawal,
) error {
	return function(ctx, withdrawal)
}
