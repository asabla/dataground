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
	// Normalize internal Go callers to the same JSON representation as API and
	// PostgreSQL values. Compilation must never receive unsupported Go types.
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return invalidSchema()
	}
	var document any
	if json.Unmarshal(schemaJSON, &document) != nil {
		return invalidSchema()
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	// The compiler has no external resource loader. Only embedded standard
	// metaschemas and references within this document can resolve.
	const resource = "urn:dataground:invocation-input"
	if compiler.AddResource(resource, document) != nil {
		return invalidSchema()
	}
	compiled, err := compiler.Compile(resource)
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
