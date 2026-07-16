package reference

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const (
	ScenarioSuccess          = "success"
	ScenarioQuestion         = "question"
	ScenarioApproval         = "approval"
	ScenarioArtifact         = "artifact"
	ScenarioCancellation     = "cancellation"
	ScenarioRetryableFailure = "retryable_failure"
	ScenarioTerminalFailure  = "terminal_failure"
	ScenarioDuplicate        = "duplicate"
	ScenarioOutOfOrder       = "out_of_order"
	ScenarioUnknownOptional  = "unknown_optional"
)

var ErrConflictingDuplicate = errors.New("conflicting duplicate event")

type Event struct {
	Key        string                     `json:"key"`
	Sequence   uint64                     `json:"sequence"`
	Type       string                     `json:"type"`
	Payload    map[string]any             `json:"payload"`
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

type Fixture struct {
	Scenario string  `json:"scenario"`
	Events   []Event `json:"events"`
}

func Capabilities() map[string]string {
	return map[string]string{
		"artifact":     "supported",
		"cancellation": "supported",
		"process":      "supported",
		"question":     "supported",
		"approval":     "supported",
		"tool":         "supported",
		"usage":        "supported",
	}
}

func Scenarios() []string {
	return []string{
		ScenarioSuccess,
		ScenarioQuestion,
		ScenarioApproval,
		ScenarioArtifact,
		ScenarioCancellation,
		ScenarioRetryableFailure,
		ScenarioTerminalFailure,
		ScenarioDuplicate,
		ScenarioOutOfOrder,
		ScenarioUnknownOptional,
	}
}

func Load(scenario string) (Fixture, error) {
	if scenario == "" {
		scenario = ScenarioSuccess
	}

	started := event("started", 1, "lifecycle.started", map[string]any{"message": "Reference runtime started."})
	succeeded := event("succeeded", 8, "lifecycle.succeeded", map[string]any{"message": "Reference runtime completed."})

	var events []Event
	switch scenario {
	case ScenarioSuccess:
		events = []Event{
			started,
			event("text", 2, "output.text.delta", map[string]any{"text": "Reference runtime completed."}),
			event("tool-started", 3, "activity.tool.started", map[string]any{"name": "reference.lookup", "callId": "call_reference"}),
			event("tool-completed", 4, "activity.tool.completed", map[string]any{"callId": "call_reference", "status": "succeeded"}),
			event("process-started", 5, "activity.process.started", map[string]any{"name": "reference-process"}),
			event("process-completed", 6, "activity.process.completed", map[string]any{"name": "reference-process", "exitCode": 0}),
			event("usage", 7, "usage.recorded", map[string]any{"inputTokens": 12, "outputTokens": 8, "totalTokens": 20}),
			succeeded,
		}
	case ScenarioQuestion:
		events = []Event{
			started,
			event("question", 2, "interaction.question.requested", map[string]any{"questionId": "question_reference", "prompt": "Choose a reference option."}),
			event("waiting", 3, "lifecycle.waiting", map[string]any{"reason": "question"}),
		}
	case ScenarioApproval:
		events = []Event{
			started,
			event("approval", 2, "interaction.approval.requested", map[string]any{"approvalId": "approval_reference", "action": "reference.read", "scope": "fixture"}),
			event("waiting", 3, "lifecycle.waiting", map[string]any{"reason": "approval"}),
		}
	case ScenarioArtifact:
		events = []Event{
			started,
			event("artifact", 2, "artifact.available", map[string]any{"name": "reference-result.json", "kind": "structured-output", "mediaType": "application/json", "sizeBytes": 2097152, "digest": "sha256:1b93a4b13f9917ba7e33ebf29560b17d50593f23bc1dfeeec961ae0cfabcb9e6", "sensitive": true}),
			event("artifact-succeeded", 3, "lifecycle.succeeded", map[string]any{"message": "Artifact finalized."}),
		}
	case ScenarioCancellation:
		events = []Event{
			started,
			event("cancelled", 2, "lifecycle.cancelled", map[string]any{"reason": "fixture cancellation"}),
		}
	case ScenarioRetryableFailure:
		events = []Event{
			started,
			event("retryable-error", 2, "error.occurred", map[string]any{"code": "REFERENCE_RUNTIME_UNAVAILABLE", "message": "Reference runtime is temporarily unavailable.", "retryable": true}),
			event("retryable-failed", 3, "lifecycle.failed", map[string]any{"code": "REFERENCE_RUNTIME_UNAVAILABLE", "retryable": true}),
		}
	case ScenarioTerminalFailure:
		events = []Event{
			started,
			event("terminal-error", 2, "error.occurred", map[string]any{"code": "REFERENCE_INPUT_REJECTED", "message": "Reference input was rejected.", "retryable": false}),
			event("terminal-failed", 3, "lifecycle.failed", map[string]any{"code": "REFERENCE_INPUT_REJECTED", "retryable": false}),
		}
	case ScenarioDuplicate:
		text := event("duplicate-text", 2, "output.text.delta", map[string]any{"text": "Delivered twice, recorded once."})
		events = []Event{started, text, text, event("duplicate-succeeded", 3, "lifecycle.succeeded", map[string]any{"message": "Duplicate ignored."})}
	case ScenarioOutOfOrder:
		events = []Event{
			started,
			event("out-of-order-third", 3, "usage.recorded", map[string]any{"inputTokens": 2, "outputTokens": 3, "totalTokens": 5}),
			event("out-of-order-second", 2, "output.text.delta", map[string]any{"text": "Events are journaled in sequence."}),
			event("out-of-order-succeeded", 4, "lifecycle.succeeded", map[string]any{"message": "Events reordered."}),
		}
	case ScenarioUnknownOptional:
		events = []Event{
			started,
			{
				Key:      "future-signal",
				Sequence: 2,
				Type:     "runtime.future.signal",
				Payload:  map[string]any{"meaning": "safe to ignore", "futureField": true},
				Extensions: map[string]json.RawMessage{
					"dev.dataground.reference": json.RawMessage(`{"fixture":"unknown_optional"}`),
				},
			},
			event("future-succeeded", 3, "lifecycle.succeeded", map[string]any{"message": "Unknown optional event preserved."}),
		}
	default:
		return Fixture{}, fmt.Errorf("unknown reference scenario %q", scenario)
	}

	return Fixture{Scenario: scenario, Events: events}, nil
}

func Normalize(events []Event) ([]Event, error) {
	byKey := make(map[string]Event, len(events))
	for _, candidate := range events {
		if candidate.Key == "" || candidate.Sequence == 0 || candidate.Type == "" || candidate.Payload == nil {
			return nil, errors.New("reference event is missing a required field")
		}
		if previous, exists := byKey[candidate.Key]; exists {
			equal, err := equalEvent(previous, candidate)
			if err != nil {
				return nil, fmt.Errorf("compare duplicate event: %w", err)
			}
			if !equal {
				return nil, fmt.Errorf("%w: %s", ErrConflictingDuplicate, candidate.Key)
			}
			continue
		}
		byKey[candidate.Key] = candidate
	}

	normalized := make([]Event, 0, len(byKey))
	for _, candidate := range byKey {
		normalized = append(normalized, candidate)
	}
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left].Sequence < normalized[right].Sequence
	})
	for index, candidate := range normalized {
		expected := uint64(index + 1)
		if candidate.Sequence != expected {
			return nil, fmt.Errorf("event sequence has gap: expected %d, got %d", expected, candidate.Sequence)
		}
	}

	return normalized, nil
}

func event(key string, sequence uint64, eventType string, payload map[string]any) Event {
	return Event{Key: key, Sequence: sequence, Type: eventType, Payload: payload}
}

func equalEvent(left, right Event) (bool, error) {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false, err
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}
