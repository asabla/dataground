package reconcile

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestInvocationRuntimeOutputUsesCompletedAnswerInsteadOfProgress(t *testing.T) {
	output := mustNewInvocationRuntimeOutput(t, map[string]any{"type": "object"})
	output.Observe(dgruntime.Event{Sequence: 1, Type: "output.text.delta", Payload: map[string]any{"text": "Checking the data."}})
	output.Observe(dgruntime.Event{Sequence: 2, Type: "output.message.completed", Payload: map[string]any{"text": "Checking the data.", "phase": "commentary"}})
	output.Observe(dgruntime.Event{Sequence: 3, Type: "output.text.delta", Payload: map[string]any{"text": `{"answer":41}`}})
	output.Observe(dgruntime.Event{Sequence: 4, Type: "output.message.completed", Payload: map[string]any{"text": `{"answer":42}`, "phase": "final"}})
	result, err := output.Result()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result["output"])
	if err != nil || string(encoded) != `{"answer":42}` {
		t.Fatalf("authoritative result = %s, %v", encoded, err)
	}
}

func TestInvocationRuntimeOutputSelectsLatestCompletedMessage(t *testing.T) {
	output := mustNewInvocationRuntimeOutput(t, nil)
	for _, event := range []dgruntime.Event{
		{Sequence: 1, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": "first", "phase": "unspecified"}},
		{Sequence: 4, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": "last", "phase": "unspecified"}},
		{Sequence: 2, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": "older", "phase": "final"}},
		{Sequence: 5, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": "progress", "phase": "commentary"}},
		{Sequence: 4, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": "last", "phase": "unspecified"}},
	} {
		output.Observe(event)
	}
	result, err := output.Result()
	want := map[string]any{"status": "succeeded", "output": map[string]any{"text": "last"}}
	if err != nil || !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, %v", result, err)
	}
}

func TestInvocationRuntimeOutputRejectsInvalidContent(t *testing.T) {
	for name, event := range map[string]dgruntime.Event{
		"missing result":  {},
		"preview only":    {Sequence: 1, Type: "output.text.delta", Payload: map[string]any{"text": `{"answer":"yes"}`}},
		"commentary only": {Sequence: 1, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": `{"answer":"yes"}`, "phase": "commentary"}},
		"multiple values": {Sequence: 1, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": `{} {}`, "phase": "final"}},
		"invalid text":    {Sequence: 1, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": true, "phase": "final"}},
		"missing phase":   {Sequence: 1, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": `{}`}},
		"unknown phase":   {Sequence: 1, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": `{}`, "phase": "other"}},
		"extra field":     {Sequence: 1, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": `{}`, "phase": "final", "nativeId": "private"}},
		"oversized":       {Sequence: 1, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": strings.Repeat("x", dgruntime.MaximumMessageTextBytes+1), "phase": "final"}},
		"zero sequence":   {Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": `{}`, "phase": "final"}},
		"schema mismatch": {Sequence: 1, Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": `{"answer":42}`, "phase": "final"}},
	} {
		t.Run(name, func(t *testing.T) {
			output := mustNewInvocationRuntimeOutput(t, map[string]any{"type": "object", "required": []any{"answer"}, "properties": map[string]any{"answer": map[string]any{"type": "string"}}})
			output.Observe(event)
			if result, err := output.Result(); result != nil || !errors.Is(err, ErrInvocationRuntimeOutputInvalid) {
				t.Fatalf("result = %#v, %v", result, err)
			}
		})
	}
}

func TestInvocationRuntimeOutputMissingPlainAnswerFailsClosed(t *testing.T) {
	for _, kind := range []string{"output.text.delta", dgruntime.MessageCompletedEvent} {
		output := mustNewInvocationRuntimeOutput(t, nil)
		output.Observe(dgruntime.Event{Sequence: 1, Type: kind, Payload: map[string]any{"text": "progress", "phase": "commentary"}})
		if _, err := output.Result(); !errors.Is(err, ErrInvocationRuntimeOutputInvalid) {
			t.Fatal("progress became a result", err)
		}
	}
}

func TestInvocationRuntimeOutputBoundsCompletedMessages(t *testing.T) {
	output := mustNewInvocationRuntimeOutput(t, nil)
	for sequence := 1; sequence <= maximumInvocationRuntimeOutputEvents+1; sequence++ {
		output.Observe(dgruntime.Event{Sequence: uint64(sequence), Type: dgruntime.MessageCompletedEvent, Payload: map[string]any{"text": "bounded", "phase": "unspecified"}})
	}
	if _, err := output.Result(); !errors.Is(err, ErrInvocationRuntimeOutputInvalid) {
		t.Fatal("unbounded messages accepted", err)
	}
}

func TestInvocationRuntimeOutputValidatesPersistedSchema(t *testing.T) {
	schema := map[string]any{
		"$defs": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
		"type":     "object",
		"required": []any{"answer"},
		"properties": map[string]any{
			"answer": map[string]any{"$ref": "#/$defs/answer"},
		},
		"additionalProperties": false,
	}
	output := mustNewInvocationRuntimeOutput(t, schema)
	output.Observe(dgruntime.Event{
		Sequence: 1,
		Type:     dgruntime.MessageCompletedEvent,
		Payload:  map[string]any{"text": "{\"answer\":42}", "phase": "final"},
	})
	if result, err := output.Result(); result != nil ||
		!errors.Is(err, ErrInvocationRuntimeOutputInvalid) {
		t.Fatalf("schema-mismatched runtime output = (%#v, %v)", result, err)
	}
}

func TestInvocationRuntimeOutputRejectsUnsafeSchemas(t *testing.T) {
	tests := map[string]map[string]any{
		"invalid type": {
			"type": "not-a-json-schema-type",
		},
		"external reference": {
			"$ref": "https://schemas.example.invalid/output.json",
		},
	}
	for name, schema := range tests {
		t.Run(name, func(t *testing.T) {
			output, err := newInvocationRuntimeOutput(schema)
			if output != nil ||
				!errors.Is(err, ErrInvocationRuntimeOutputSchemaInvalid) {
				t.Fatalf("invalid runtime output schema = (%#v, %v)", output, err)
			}
		})
	}
}

func mustNewInvocationRuntimeOutput(
	t *testing.T,
	schema map[string]any,
) *invocationRuntimeOutput {
	t.Helper()
	output, err := newInvocationRuntimeOutput(schema)
	if err != nil {
		t.Fatal(err)
	}
	return output
}
