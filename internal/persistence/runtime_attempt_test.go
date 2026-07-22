package persistence

import (
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
)

func TestRuntimeAttemptAcceptsOnlySafePreTurnEffectStates(t *testing.T) {
	claim := OperationClaim{
		Kind:                OperationKindInvocation,
		IsolationDomainID:   "iso_runtime",
		ID:                  "op_runtime",
		ResourceID:          "inv_runtime",
		Command:             "invoke",
		ObservedState:       "running",
		StateMachineVersion: 2,
		LeaseOwner:          "worker-runtime",
		FencingToken:        7,
		DeadlineAt:          time.Now().Add(time.Hour),
	}
	effect := EffectRecord{
		IsolationDomainID: claim.IsolationDomainID,
		OperationKind:     claim.Kind,
		OperationID:       claim.ID,
		EffectID: identity.Derived(
			"eff",
			claim.IsolationDomainID+":"+claim.Kind+":"+claim.ID+":run-invocation",
		),
		Phase: "run-invocation",
	}
	for _, status := range []string{"prepared", "failed", "unknown"} {
		effect.Status = status
		if !validInvocationRuntimeAttempt(claim, effect) {
			t.Fatalf("safe pre-turn effect status %q was rejected", status)
		}
	}
	for _, status := range []string{"", "succeeded", "invalid"} {
		effect.Status = status
		if validInvocationRuntimeAttempt(claim, effect) {
			t.Fatalf("unsafe runtime effect status %q was accepted", status)
		}
	}
}
