package persistence

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestInvocationAuthorizationPolicyWithdrawalValidationAndOwnership(t *testing.T) {
	t.Parallel()

	reasonDigest := sha256.Sum256([]byte("emergency withdrawal"))
	withdrawal := InvocationAuthorizationPolicyWithdrawal{
		Contract:          InvocationAuthorizationPolicyWithdrawalContract,
		IsolationDomainID: "iso_00000000000000000001",
		ServiceID:         "svc_00000000000000000001",
		RevisionID:        "rev_00000000000000000001",
		PolicyDigest:      bytes.Repeat([]byte{0x11}, sha256.Size),
		WithdrawnBy:       "emergency_revoker",
		ReasonDigest:      reasonDigest[:],
		CorrelationID:     "cor_00000000000000000001",
	}
	if !withdrawal.Valid() {
		t.Fatal("valid withdrawal was rejected")
	}
	clone := cloneInvocationAuthorizationPolicyWithdrawal(withdrawal)
	clone.PolicyDigest[0] = 0x22
	clone.ReasonDigest[0] = 0x22
	if withdrawal.PolicyDigest[0] == 0x22 || withdrawal.ReasonDigest[0] == 0x22 {
		t.Fatal("withdrawal clone shared digest storage")
	}
	if sameInvocationAuthorizationPolicyWithdrawal(withdrawal, clone) {
		t.Fatal("different withdrawal digests compared equal")
	}

	tests := map[string]func(*InvocationAuthorizationPolicyWithdrawal){
		"contract": func(candidate *InvocationAuthorizationPolicyWithdrawal) {
			candidate.Contract = "dataground.invocation-authorization-policy-withdrawal/v2"
		},
		"domain": func(candidate *InvocationAuthorizationPolicyWithdrawal) {
			candidate.IsolationDomainID = "iso_other"
		},
		"service": func(candidate *InvocationAuthorizationPolicyWithdrawal) {
			candidate.ServiceID = "svc_other"
		},
		"revision": func(candidate *InvocationAuthorizationPolicyWithdrawal) {
			candidate.RevisionID = "rev_other"
		},
		"policy digest": func(candidate *InvocationAuthorizationPolicyWithdrawal) {
			candidate.PolicyDigest = candidate.PolicyDigest[:sha256.Size-1]
		},
		"actor": func(candidate *InvocationAuthorizationPolicyWithdrawal) {
			candidate.WithdrawnBy = ""
		},
		"reason digest": func(candidate *InvocationAuthorizationPolicyWithdrawal) {
			candidate.ReasonDigest = nil
		},
		"correlation": func(candidate *InvocationAuthorizationPolicyWithdrawal) {
			candidate.CorrelationID = "cor_other"
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := cloneInvocationAuthorizationPolicyWithdrawal(withdrawal)
			mutate(&candidate)
			if candidate.Valid() {
				t.Fatal("invalid withdrawal was accepted")
			}
		})
	}
}
