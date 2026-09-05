package domain_test

import (
	"math"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/domain"
)

func TestInvocationOutputContract(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []string{"answer"}, "additionalProperties": false, "properties": map[string]any{"answer": map[string]any{"type": "integer", "minimum": 0}}}
	for _, output := range []map[string]any{{"answer": 0}, {"answer": 12}} {
		if problem := domain.ValidateInvocationOutput(schema, output); problem != nil {
			t.Fatalf("valid output rejected: %v", problem)
		}
	}
	for _, output := range []map[string]any{nil, {}, {"answer": -1}, {"answer": "private-value"}, {"answer": math.NaN()}, {"answer": 1, "private-field": true}} {
		problem := domain.ValidateInvocationOutput(schema, output)
		if problem == nil || problem.Code != "INVOCATION_OUTPUT_INVALID" || problem.Retryable || strings.Contains(problem.Message, "private") || len(problem.FieldErrors) != 0 {
			t.Fatalf("unsafe or missing output failure: %+v", problem)
		}
	}
	for _, schema := range []map[string]any{{"type": "private-invalid"}, {"$ref": "https://private.example/schema"}, {"$ref": "file:///private/schema"}} {
		problem := domain.ValidateInvocationOutput(schema, map[string]any{"private-field": "private-value"})
		if problem == nil || problem.Code != "REVISION_OUTPUT_SCHEMA_INVALID" || strings.Contains(problem.Message, "private") {
			t.Fatalf("schema failure: %+v", problem)
		}
	}
	if problem := domain.ValidateInvocationOutput(nil, map[string]any{"arbitrary": true}); problem != nil {
		t.Fatal(problem)
	}
}
