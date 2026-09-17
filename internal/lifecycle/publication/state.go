package publication

import "fmt"

const StateMachineVersion = 1

// AtomicDevelopmentVersion records a terminal operator publication whose
// validation and database effects commit together, without queued effects.
const AtomicDevelopmentVersion = 2

// QueuedDevelopmentVersion verifies and commits one governed publication under
// its current lease. It has no reference or external publication effect.
const QueuedDevelopmentVersion = 3

// AuthorizedDevelopmentVersion additionally requires audited publication authority.
const AuthorizedDevelopmentVersion = 4

type State string

const (
	StateQueued     State = "queued"
	StateValidating State = "validating"
	StateApplying   State = "applying"
	StateObserving  State = "observing"
	StatePublished  State = "published"
	StateFailed     State = "failed"
	StateCancelled  State = "cancelled"
)

type Command string

const (
	CommandPublish Command = "publish"
	CommandCancel  Command = "cancel"
	CommandRepair  Command = "repair"
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
			StateValidating: {},
			StateFailed:     {},
			StateCancelled:  {},
		},
		StateValidating: {
			StateApplying:  {},
			StateFailed:    {},
			StateCancelled: {},
		},
		StateApplying: {
			StateObserving: {},
			StateFailed:    {},
			StateCancelled: {},
		},
		StateObserving: {
			StateApplying:  {},
			StatePublished: {},
			StateFailed:    {},
			StateCancelled: {},
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
		return fmt.Errorf("publication transition %q -> %q is not allowed", from, to)
	}
	return nil
}

func IsTerminal(state State) bool {
	return state == StatePublished || state == StateFailed || state == StateCancelled
}

func AllowsEffect(command Command, state State, phase string) bool {
	if IsTerminal(state) {
		return false
	}
	if command == CommandCancel {
		return phase == "cancel-publication"
	}
	return (command == CommandPublish || command == CommandRepair) && state == StateApplying && phase == "publish-revision"
}
