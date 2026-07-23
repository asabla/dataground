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
	maximumCodexInvocationArtifacts         = 32
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
	if len(target.Input) < 1 || len(target.Input) > 2 {
		return dgruntime.StartRequest{}, fmt.Errorf(
			"%w: codex v1 accepts only prompt and optional artifacts",
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
	artifacts, err := mapCodexInvocationArtifacts(target.Input)
	if err != nil {
		return dgruntime.StartRequest{}, err
	}
	outputSchema, err := cloneCodexInvocationOutputSchema(target.OutputSchema)
	if err != nil {
		return dgruntime.StartRequest{}, err
	}
	return dgruntime.StartRequest{
		Prompt:       prompt,
		OutputSchema: outputSchema,
		Artifacts:    artifacts,
		ApprovalMode: dgruntime.ApprovalLocked,
		SandboxMode:  codexInvocationSandboxMode(artifacts),
	}, nil
}

func mapCodexInvocationArtifacts(input map[string]any) ([]dgruntime.ArtifactDeclaration, error) {
	value, found := input["artifacts"]
	if !found {
		if len(input) != 1 {
			return nil, ErrInvocationRuntimeInputInvalid
		}
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 || len(items) > maximumCodexInvocationArtifacts {
		return nil, ErrInvocationRuntimeInputInvalid
	}
	artifacts := make([]dgruntime.ArtifactDeclaration, 0, len(items))
	for _, item := range items {
		fields, ok := item.(map[string]any)
		if !ok || len(fields) != 5 {
			return nil, ErrInvocationRuntimeInputInvalid
		}
		artifact := dgruntime.ArtifactDeclaration{}
		var fieldsOK bool
		artifact.ID, fieldsOK = fields["id"].(string)
		if !fieldsOK {
			return nil, ErrInvocationRuntimeInputInvalid
		}
		artifact.Name, fieldsOK = fields["name"].(string)
		if !fieldsOK {
			return nil, ErrInvocationRuntimeInputInvalid
		}
		artifact.SandboxPath, fieldsOK = fields["sandboxPath"].(string)
		if !fieldsOK {
			return nil, ErrInvocationRuntimeInputInvalid
		}
		artifact.MediaType, fieldsOK = fields["mediaType"].(string)
		if !fieldsOK {
			return nil, ErrInvocationRuntimeInputInvalid
		}
		artifact.Kind, fieldsOK = fields["kind"].(string)
		if !fieldsOK {
			return nil, ErrInvocationRuntimeInputInvalid
		}
		artifacts = append(artifacts, artifact)
	}
	if err := validateInvocationRuntimeArtifacts(artifacts); err != nil {
		return nil, errors.Join(ErrInvocationRuntimeInputInvalid, err)
	}
	return artifacts, nil
}

func codexInvocationSandboxMode(artifacts []dgruntime.ArtifactDeclaration) dgruntime.SandboxMode {
	if len(artifacts) > 0 {
		return dgruntime.SandboxWorkspaceWrite
	}
	return dgruntime.SandboxReadOnly
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
