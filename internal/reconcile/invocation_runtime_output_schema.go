package reconcile

import (
	"errors"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const invocationRuntimeOutputSchemaURL = "urn:dataground:invocation-runtime-output"

var ErrInvocationRuntimeOutputSchemaInvalid = errors.New(
	"invocation runtime output schema is invalid",
)

type invocationRuntimeOutputSchema struct {
	compiled *jsonschema.Schema
}

// compileInvocationRuntimeOutputSchema keeps schema loading process-local.
// Standard metaschemas and document-local references remain available, while
// unresolved external resources fail because no URL loader is configured.
func compileInvocationRuntimeOutputSchema(
	value map[string]any,
) (*invocationRuntimeOutputSchema, error) {
	if value == nil {
		return nil, nil
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource(invocationRuntimeOutputSchemaURL, value); err != nil {
		return nil, errors.Join(ErrInvocationRuntimeOutputSchemaInvalid, err)
	}
	compiled, err := compiler.Compile(invocationRuntimeOutputSchemaURL)
	if err != nil {
		return nil, errors.Join(ErrInvocationRuntimeOutputSchemaInvalid, err)
	}
	return &invocationRuntimeOutputSchema{compiled: compiled}, nil
}

func (schema *invocationRuntimeOutputSchema) Validate(value any) error {
	if schema == nil || schema.compiled == nil {
		return ErrInvocationRuntimeOutputSchemaInvalid
	}
	return schema.compiled.Validate(value)
}
