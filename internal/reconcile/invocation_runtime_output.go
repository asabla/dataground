package reconcile

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

const (
	maximumInvocationRuntimeOutputBytes  = 192 << 10
	maximumInvocationRuntimeOutputEvents = 4096
)

var ErrInvocationRuntimeOutputInvalid = errors.New("invocation runtime output is invalid")

type invocationRuntimeOutput struct {
	structured      bool
	text            string
	messageSequence uint64
	sawDelta        bool
	seen            map[uint64]struct{}
	validator       *invocationRuntimeOutputSchema
	invalid         bool
}

func newInvocationRuntimeOutput(
	outputSchema map[string]any,
) (*invocationRuntimeOutput, error) {
	validator, err := compileInvocationRuntimeOutputSchema(outputSchema)
	if err != nil {
		return nil, err
	}
	return &invocationRuntimeOutput{
		structured: outputSchema != nil,
		seen:       make(map[uint64]struct{}),
		validator:  validator,
	}, nil
}

// Observe accepts only events already acknowledged by the fenced event sink.
// Runtime-source replay is ignored so it cannot duplicate the declared output.
func (output *invocationRuntimeOutput) Observe(event dgruntime.Event) {
	if event.Type == "output.text.delta" {
		output.sawDelta = true
		return
	}
	if event.Type != dgruntime.MessageCompletedEvent {
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
	message, err := dgruntime.ParseCompletedMessage(event.Payload)
	if err != nil || event.Sequence == 0 {
		output.invalid = true
		return
	}
	// Completion snapshots replace previews. Commentary is retained in the
	// event journal, but cannot become the invocation's declared result.
	if message.Phase != "commentary" && event.Sequence > output.messageSequence {
		output.text = message.Text
		output.messageSequence = event.Sequence
	}
}

func (output *invocationRuntimeOutput) Result() (map[string]any, error) {
	if output.invalid || (output.messageSequence == 0 && (output.sawDelta || len(output.seen) > 0)) {
		return nil, ErrInvocationRuntimeOutputInvalid
	}
	var value any = map[string]any{"text": output.text}
	if output.structured {
		if output.messageSequence == 0 || len(output.text) == 0 {
			return nil, ErrInvocationRuntimeOutputInvalid
		}
		decoder := json.NewDecoder(strings.NewReader(output.text))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return nil, ErrInvocationRuntimeOutputInvalid
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, ErrInvocationRuntimeOutputInvalid
		}
		if err := output.validator.Validate(value); err != nil {
			return nil, errors.Join(ErrInvocationRuntimeOutputInvalid, err)
		}
	}
	result := map[string]any{"status": "succeeded", "output": value}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maximumInvocationRuntimeOutputBytes {
		return nil, ErrInvocationRuntimeOutputInvalid
	}
	return result, nil
}
