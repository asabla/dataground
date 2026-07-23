package reconcile

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

const (
	maximumInvocationRuntimeOutputBytes  = 192 << 10
	maximumInvocationRuntimeOutputEvents = 4096
)

var ErrInvocationRuntimeOutputInvalid = errors.New("invocation runtime output is invalid")

type invocationRuntimeOutput struct {
	structured bool
	text       bytes.Buffer
	seen       map[uint64]struct{}
	invalid    bool
}

func newInvocationRuntimeOutput(outputSchema map[string]any) *invocationRuntimeOutput {
	return &invocationRuntimeOutput{
		structured: outputSchema != nil,
		seen:       make(map[uint64]struct{}),
	}
}

// Observe accepts only events already acknowledged by the fenced event sink.
// Runtime-source replay is ignored so it cannot duplicate the declared output.
func (output *invocationRuntimeOutput) Observe(event dgruntime.Event) {
	if event.Type != "output.text.delta" {
		return
	}
	if _, found := output.seen[event.Sequence]; found {
		return
	}
	if len(output.seen) >= maximumInvocationRuntimeOutputEvents {
		output.invalid = true
		return
	}
	output.seen[event.Sequence] = struct{}{}
	value, ok := event.Payload["text"].(string)
	if !ok {
		output.invalid = true
		return
	}
	if len(value) > maximumInvocationRuntimeOutputBytes-output.text.Len() {
		output.invalid = true
		return
	}
	_, _ = output.text.WriteString(value)
}

func (output *invocationRuntimeOutput) Result() (map[string]any, error) {
	if output.invalid {
		return nil, ErrInvocationRuntimeOutputInvalid
	}
	var value any = map[string]any{"text": output.text.String()}
	if output.structured {
		if output.text.Len() == 0 {
			return nil, ErrInvocationRuntimeOutputInvalid
		}
		decoder := json.NewDecoder(bytes.NewReader(output.text.Bytes()))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, ErrInvocationRuntimeOutputInvalid
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, ErrInvocationRuntimeOutputInvalid
		}
	}
	result := map[string]any{"status": "succeeded", "output": value}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maximumInvocationRuntimeOutputBytes {
		return nil, ErrInvocationRuntimeOutputInvalid
	}
	return result, nil
}
