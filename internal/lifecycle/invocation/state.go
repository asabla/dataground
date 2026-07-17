package invocation

import "fmt"

const StateMachineVersion = 1

type State string

const (
	StateQueued     State = "queued"
	StateStarting   State = "starting"
	StateRunning    State = "running"
	StateWaiting    State = "waiting"
	StateCancelling State = "cancelling"
	StateObserving  State = "observing"
	StateSucceeded  State = "succeeded"
	StateFailed     State = "failed"
	StateCancelled  State = "cancelled"
)

type Command string

const (
	CommandInvoke Command = "invoke"
	CommandCancel Command = "cancel"
	CommandRepair Command = "repair"
)

type ErrorClassification string

const (
	ErrorRetryable ErrorClassification = "retryable"
	ErrorTerminal  ErrorClassification = "terminal"
	ErrorCancelled ErrorClassification = "cancelled"
	ErrorUnknown   ErrorClassification = "unknown"
)

func CanTransition(from, to State) bool {
	if from == to && !IsTerminal(from) {
		return true
	}
	allowed := map[State]map[State]struct{}{
		StateQueued: {
			StateStarting:   {},
			StateCancelling: {},
			StateFailed:     {},
		},
		StateStarting: {
			StateRunning:    {},
			StateObserving:  {},
			StateFailed:     {},
			StateCancelling: {},
		},
		StateRunning: {
			StateWaiting:    {},
			StateObserving:  {},
			StateSucceeded:  {},
			StateFailed:     {},
			StateCancelling: {},
		},
		StateWaiting: {
			StateRunning:    {},
			StateFailed:     {},
			StateCancelling: {},
		},
		StateCancelling: {
			StateObserving: {},
			StateCancelled: {},
			StateFailed:    {},
		},
		StateObserving: {
			StateRunning:    {},
			StateSucceeded:  {},
			StateFailed:     {},
			StateCancelled:  {},
			StateCancelling: {},
		},
		StateFailed: {
			StateQueued: {},
		},
	}
	_, ok := allowed[from][to]
	return ok
}

func ValidateTransition(from, to State) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("invocation transition %q -> %q is not allowed", from, to)
	}
	return nil
}

func IsTerminal(state State) bool {
	return state == StateSucceeded || state == StateFailed || state == StateCancelled
}

func AllowsEffect(command Command, state State, phase string) bool {
	if IsTerminal(state) {
		return false
	}
	if command == CommandCancel || state == StateCancelling {
		return phase == "cancel-invocation"
	}
	return (command == CommandInvoke || command == CommandRepair) && state == StateStarting && phase == "start-invocation"
}
