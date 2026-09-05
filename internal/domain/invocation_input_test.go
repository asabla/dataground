package domain_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/asabla/dataground/internal/domain"
)

func TestInvocationInputContractValidatesJSONSemantics(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(`{
		"type":"object","additionalProperties":false,"required":["name","count","enabled","items"],
		"$defs":{"item":{"type":"object","required":["id"],"properties":{"id":{"type":"integer","minimum":1}},"additionalProperties":false}},
		"properties":{"name":{"type":"string","minLength":1,"maxLength":2},"count":{"type":"integer","minimum":1,"maximum":5},"enabled":{"type":"boolean"},"items":{"type":"array","minItems":1,"items":{"$ref":"#/$defs/item"}}}
	}`), &schema); err != nil {
		t.Fatal(err)
	}
	valid := func() map[string]any {
		return map[string]any{"name": "😀é", "count": 2, "enabled": false, "items": []any{map[string]any{"id": 1}}}
	}
	if problem := domain.ValidateInvocationInput(schema, valid()); problem != nil {
		t.Fatalf("valid JSON input rejected: %#v", problem)
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing required":   func(v map[string]any) { delete(v, "enabled") },
		"wrong type":         func(v map[string]any) { v["enabled"] = "false" },
		"fractional integer": func(v map[string]any) { v["count"] = 1.5 },
		"numeric bound":      func(v map[string]any) { v["count"] = 6 },
		"unicode length":     func(v map[string]any) { v["name"] = "😀éx" },
		"extra property":     func(v map[string]any) { v["secret-field"] = "secret-value" },
		"nested reference":   func(v map[string]any) { v["items"] = []any{map[string]any{"id": 0}} },
	} {
		t.Run(name, func(t *testing.T) {
			value := valid()
			mutate(value)
			problem := domain.ValidateInvocationInput(schema, value)
			if problem == nil || problem.Code != "INVOCATION_INPUT_INVALID" || problem.Retryable || len(problem.FieldErrors) != 0 {
				t.Fatalf("invalid input result = %#v", problem)
			}
		})
	}
	for _, absent := range []map[string]any{nil, {}} {
		if problem := domain.ValidateInvocationInput(absent, valid()); problem != nil {
			t.Fatal("absent or unconstrained contract rejected valid input")
		}
	}
}

func TestInvocationInputContractCannotLoadExternalResourcesOrLeakDiagnostics(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	for _, schema := range []map[string]any{
		{"type": "private-invalid-type"},
		{"$ref": server.URL + "/private-schema"},
		{"$ref": "file:///private/schema.json"},
		{"$ref": "#/$defs/missing-private-definition"},
		{"$schema": server.URL + "/private-metaschema"},
	} {
		problem := domain.ValidateInvocationInput(schema, map[string]any{"private-field": "private-value"})
		if problem == nil || problem.Code != "REVISION_INPUT_SCHEMA_INVALID" || problem.Message != "The service revision input contract cannot be validated." || problem.Retryable {
			t.Fatalf("unsafe schema result = %#v", problem)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("schema validation contacted an external resource")
	}
}
