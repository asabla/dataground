package reconcile

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

const (
	CodexAppServerRuntimeProfileV1          = "codex.app-server/v1"
	maximumCodexInvocationPromptBytes       = 256 << 10
	maximumCodexInvocationOutputSchemaBytes = 256 << 10
)

var (
	ErrInvocationRuntimeProfileUnsupported = errors.New("invocation runtime profile is unsupported")
	ErrInvocationRuntimeInputInvalid       = errors.New("invocation runtime input is invalid")
)

// CodexInvocationRuntimeRequestBuilder maps the first pinned Codex profile from
// durable invocation state. It deliberately exposes no caller-controlled
// native model, working-directory, approval, or sandbox overrides.
type CodexInvocationRuntimeRequestBuilder struct{}

func (CodexInvocationRuntimeRequestBuilder) BuildInvocationRuntimeRequest(
	target persistence.InvocationRuntimeTarget,
) (dgruntime.StartRequest, error) {
	if target.RuntimeProfile != CodexAppServerRuntimeProfileV1 {
		return dgruntime.StartRequest{}, fmt.Errorf(
			"%w: %q",
			ErrInvocationRuntimeProfileUnsupported,
			target.RuntimeProfile,
		)
	}
	if len(target.Input) != 1 {
		return dgruntime.StartRequest{}, fmt.Errorf(
			"%w: codex v1 requires exactly one prompt field",
			ErrInvocationRuntimeInputInvalid,
		)
	}
	prompt, ok := target.Input["prompt"].(string)
	if !ok ||
		prompt == "" ||
		len(prompt) > maximumCodexInvocationPromptBytes ||
		!utf8.ValidString(prompt) ||
		strings.ContainsRune(prompt, '\x00') {
		return dgruntime.StartRequest{}, fmt.Errorf(
			"%w: codex v1 prompt is missing or invalid",
			ErrInvocationRuntimeInputInvalid,
		)
	}
	outputSchema, err := cloneCodexInvocationOutputSchema(target.OutputSchema)
	if err != nil {
		return dgruntime.StartRequest{}, err
	}
	return dgruntime.StartRequest{
		Prompt:       prompt,
		OutputSchema: outputSchema,
		ApprovalMode: dgruntime.ApprovalLocked,
		SandboxMode:  dgruntime.SandboxReadOnly,
	}, nil
}

func cloneCodexInvocationOutputSchema(value map[string]any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maximumCodexInvocationOutputSchemaBytes {
		return nil, ErrInvocationRuntimeOutputSchemaInvalid
	}
	var cloned map[string]any
	if err := json.Unmarshal(encoded, &cloned); err != nil || cloned == nil {
		return nil, ErrInvocationRuntimeOutputSchemaInvalid
	}
	return cloned, nil
}

var _ InvocationRuntimeRequestBuilder = CodexInvocationRuntimeRequestBuilder{}
