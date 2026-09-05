package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/api"
)

func TestRevisionPublicationRequiresCompilableSchemas(t *testing.T) {
	handler := newHandler(t)
	service := createService(t, handler, testDomain, "schema-publication-service")
	for index, field := range []string{"inputSchema", "outputSchema"} {
		draft := performJSON[api.ServiceRevision](t, handler, http.MethodPost, revisionCollectionPath(testDomain, service.Metadata.ID), fmt.Sprintf("bad-schema-draft-%d", index), map[string]any{"runtimeProfile": "reference/v1", field: map[string]any{"$ref": "https://private.example/schema"}}, http.StatusCreated)
		path := "/v1/isolation-domains/" + testDomain + "/service-revisions/" + draft.Metadata.ID + "/actions/publish"
		for attempt := range 2 {
			response := perform(t, handler, http.MethodPost, path, fmt.Sprintf("bad-schema-publish-%d-%d", index, attempt), map[string]any{"expectedVersion": 1}, nil)
			raw := response.Body.String()
			var envelope api.ErrorEnvelope
			decodeResponse(t, response, &envelope)
			expected := "REVISION_INPUT_SCHEMA_INVALID"
			if field == "outputSchema" {
				expected = "REVISION_OUTPUT_SCHEMA_INVALID"
			}
			if response.Code != http.StatusConflict || envelope.Error.Code != expected || envelope.Error.CorrelationID == "" || envelope.Error.Retryable || strings.Contains(raw, "private.example") {
				t.Fatalf("invalid publication = %d %s", response.Code, raw)
			}
		}
		response := perform(t, handler, http.MethodPut, serviceAliasPath(testDomain, service.Metadata.ID, "stable"), fmt.Sprintf("bad-schema-alias-%d", index), map[string]any{"revisionId": draft.Metadata.ID}, nil)
		if response.Code == http.StatusOK {
			t.Fatal("invalid draft became routable")
		}
	}
	valid := performJSON[api.ServiceRevision](t, handler, http.MethodPost, revisionCollectionPath(testDomain, service.Metadata.ID), "valid-schema-draft", map[string]any{"runtimeProfile": "reference/v1", "inputSchema": map[string]any{"type": "object"}, "outputSchema": map[string]any{"$defs": map[string]any{"result": map[string]any{"type": "string"}}, "$ref": "#/$defs/result"}}, http.StatusCreated)
	path := "/v1/isolation-domains/" + testDomain + "/service-revisions/" + valid.Metadata.ID + "/actions/publish"
	published := performJSON[api.ServiceRevision](t, handler, http.MethodPost, path, "valid-schema-publish", map[string]any{"expectedVersion": 1}, http.StatusOK)
	replayed := performJSON[api.ServiceRevision](t, handler, http.MethodPost, path, "valid-schema-publish", map[string]any{"expectedVersion": 1}, http.StatusOK)
	if published.State != "published" || replayed.Metadata.Version != published.Metadata.Version {
		t.Fatal("valid publication or replay changed")
	}
}
