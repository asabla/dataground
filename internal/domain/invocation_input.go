package domain

import (
	"encoding/json"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ValidateInvocationInput checks the exact resolved revision's contract. It
// retains no schema or input and returns only closed, content-free failures.
func ValidateInvocationInput(schema, input map[string]any) *APIError {
	if schema == nil {
		return nil
	}
	invalidSchema := func() *APIError {
		return &APIError{
			Code:    "REVISION_INPUT_SCHEMA_INVALID",
			Message: "The service revision input contract cannot be validated.",
		}
	}
	invalidInput := func() *APIError {
		return &APIError{
			Code:    "INVOCATION_INPUT_INVALID",
			Message: "Invocation input does not satisfy the service revision input contract.",
		}
	}
	compiled, err := compileRevisionSchema(schema)
	if err != nil {
		return invalidSchema()
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return invalidInput()
	}
	var value any
	if json.Unmarshal(inputJSON, &value) != nil || compiled.Validate(value) != nil {
		return invalidInput()
	}
	return nil
}

// ValidateRevisionSchemas compiles both publication contracts without loading
// external resources or returning schema contents in public errors.
func ValidateRevisionSchemas(input, output map[string]any) *APIError {
	for _, contract := range []struct {
		schema        map[string]any
		code, message string
	}{
		{input, "REVISION_INPUT_SCHEMA_INVALID", "The service revision input contract cannot be validated."},
		{output, "REVISION_OUTPUT_SCHEMA_INVALID", "The service revision output contract cannot be validated."},
	} {
		if contract.schema == nil {
			continue
		}
		if _, err := compileRevisionSchema(contract.schema); err != nil {
			return &APIError{Code: contract.code, Message: contract.message}
		}
	}
	return nil
}

func compileRevisionSchema(schema map[string]any) (*jsonschema.Schema, error) {
	// Normalize internal Go callers to the same JSON representation as API and
	// PostgreSQL values. Compilation must never receive unsupported Go types.
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var document any
	if err := json.Unmarshal(schemaJSON, &document); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	// The compiler has no external resource loader. Only embedded standard
	// metaschemas and references within this document can resolve.
	const resource = "urn:dataground:invocation-input"
	if err := compiler.AddResource(resource, document); err != nil {
		return nil, err
	}
	compiled, err := compiler.Compile(resource)
	if err != nil {
		return nil, err
	}
	return compiled, nil
}
