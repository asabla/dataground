package reconcile

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

func TestInvocationRuntimeOutputMaterializesText(t *testing.T) {
	output := mustNewInvocationRuntimeOutput(t, nil)
	first := dgruntime.Event{
		Sequence: 1,
		Type:     "output.text.delta",
		Payload:  map[string]any{"text": "durable "},
	}
	output.Observe(first)
	output.Observe(dgruntime.Event{
		Sequence: 2,
		Type:     "activity.tool.completed",
		Payload:  map[string]any{"kind": "tool"},
	})
	output.Observe(first)
	output.Observe(dgruntime.Event{
		Sequence: 3,
		Type:     "output.text.delta",
		Payload:  map[string]any{"text": "output"},
	})

	result, err := output.Result()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"status": "succeeded",
		"output": map[string]any{"text": "durable output"},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("runtime output = %#v, want %#v", result, want)
	}
}

func TestInvocationRuntimeOutputMaterializesStructuredValue(t *testing.T) {
	output := mustNewInvocationRuntimeOutput(t, map[string]any{"type": "object"})
	output.Observe(dgruntime.Event{
		Sequence: 1,
		Type:     "output.text.delta",
		Payload:  map[string]any{"text": `{"answer":`},
	})
	output.Observe(dgruntime.Event{
		Sequence: 2,
		Type:     "output.text.delta",
		Payload:  map[string]any{"text": `42}`},
	})

	result, err := output.Result()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result["output"])
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"answer":42}` {
		t.Fatalf("structured output = %s", encoded)
	}
}

func TestInvocationRuntimeOutputRejectsInvalidContent(t *testing.T) {
	tests := map[string]func(*invocationRuntimeOutput){
		"empty structured value": func(*invocationRuntimeOutput) {},
		"multiple structured values": func(output *invocationRuntimeOutput) {
			output.Observe(dgruntime.Event{
				Sequence: 1,
				Type:     "output.text.delta",
				Payload:  map[string]any{"text": `{} {}`},
			})
		},
		"invalid text payload": func(output *invocationRuntimeOutput) {
			output.Observe(dgruntime.Event{
				Sequence: 1,
				Type:     "output.text.delta",
				Payload:  map[string]any{"text": true},
			})
		},
		"oversized output": func(output *invocationRuntimeOutput) {
			output.Observe(dgruntime.Event{
				Sequence: 1,
				Type:     "output.text.delta",
				Payload: map[string]any{
					"text": strings.Repeat("x", maximumInvocationRuntimeOutputBytes+1),
				},
			})
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			output := mustNewInvocationRuntimeOutput(t, map[string]any{"type": "object"})
			prepare(output)
			if result, err := output.Result(); result != nil ||
				!errors.Is(err, ErrInvocationRuntimeOutputInvalid) {
				t.Fatalf("invalid runtime output = (%#v, %v)", result, err)
			}
		})
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
		Type:     "output.text.delta",
		Payload:  map[string]any{"text": "{\"answer\":42}"},
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
