package invocation_test

import (
	"testing"

	"github.com/asabla/dataground/internal/lifecycle/invocation"
)

func TestInvocationTransitions(t *testing.T) {
	t.Parallel()
	if invocation.StateMachineVersion != 2 {
		t.Fatalf("state machine version = %d, want 2", invocation.StateMachineVersion)
	}

	valid := [][2]invocation.State{
		{invocation.StateQueued, invocation.StateStarting},
		{invocation.StateStarting, invocation.StateRunning},
		{invocation.StateRunning, invocation.StateWaiting},
		{invocation.StateWaiting, invocation.StateCancelling},
		{invocation.StateCancelling, invocation.StateObserving},
		{invocation.StateObserving, invocation.StateCancelled},
		{invocation.StateFailed, invocation.StateQueued},
	}
	for _, transition := range valid {
		if err := invocation.ValidateTransition(transition[0], transition[1]); err != nil {
			t.Errorf("expected transition %q -> %q: %v", transition[0], transition[1], err)
		}
	}

	invalid := [][2]invocation.State{
		{invocation.StateQueued, invocation.StateSucceeded},
		{invocation.StateSucceeded, invocation.StateRunning},
		{invocation.StateCancelled, invocation.StateQueued},
	}
	for _, transition := range invalid {
		if err := invocation.ValidateTransition(transition[0], transition[1]); err == nil {
			t.Errorf("expected transition %q -> %q to fail", transition[0], transition[1])
		}
	}
}

func TestInvocationEffectsRemainBoundToTheirLifecyclePhase(t *testing.T) {
	t.Parallel()

	if !invocation.AllowsEffect(invocation.CommandInvoke, invocation.StateStarting, "start-invocation") {
		t.Fatal("admission effect should be allowed while starting")
	}
	if !invocation.AllowsEffect(invocation.CommandInvoke, invocation.StateRunning, "run-invocation") {
		t.Fatal("runtime effect should be allowed while running")
	}
	if invocation.AllowsEffect(invocation.CommandInvoke, invocation.StateStarting, "run-invocation") ||
		invocation.AllowsEffect(invocation.CommandInvoke, invocation.StateRunning, "start-invocation") {
		t.Fatal("invocation effect phases must not cross lifecycle states")
	}
}

func TestCancellationPreventsInvocationEffects(t *testing.T) {
	t.Parallel()

	if invocation.AllowsEffect(invocation.CommandCancel, invocation.StateStarting, "start-invocation") {
		t.Fatal("cancellation allowed a new invocation effect")
	}
	if !invocation.AllowsEffect(invocation.CommandCancel, invocation.StateCancelling, "cancel-invocation") {
		t.Fatal("cancellation did not allow its cleanup effect")
	}
	if invocation.AllowsEffect(invocation.CommandInvoke, invocation.StateSucceeded, "start-invocation") {
		t.Fatal("terminal invocation allowed a new effect")
	}
}
