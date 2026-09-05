package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/api"
)

func TestInvocationAdmissionValidatesResolvedInputAndPreservesReplay(t *testing.T) {
	handler := newHandler(t)
	service := createService(t, handler, testDomain, "input-contract-service")
	create := func(schema map[string]any, key string) api.ServiceRevision {
		t.Helper()
		draft := performJSON[api.ServiceRevision](t, handler, http.MethodPost, revisionCollectionPath(testDomain, service.Metadata.ID), key+"-draft", map[string]any{"runtimeProfile": "reference/v1", "requiredCapabilities": []string{}, "inputSchema": schema}, http.StatusCreated)
		return performJSON[api.ServiceRevision](t, handler, http.MethodPost, "/v1/isolation-domains/"+testDomain+"/service-revisions/"+draft.Metadata.ID+"/actions/publish", key+"-publish", map[string]any{"expectedVersion": 1}, http.StatusOK)
	}
	revision := create(map[string]any{"type": "object", "required": []string{"prompt"}, "properties": map[string]any{"prompt": map[string]any{"type": "string", "minLength": 1}}, "additionalProperties": false}, "input-first")
	alias := assignAlias(t, handler, testDomain, service.Metadata.ID, revision.Metadata.ID, "input-route")
	path := "/v1/isolation-domains/" + testDomain + "/agent-services/" + service.Metadata.ID + "/invocations"
	for index, value := range []map[string]any{{}, {"prompt": 42}, {"prompt": ""}, {"prompt": "private-prompt", "extra": "private-value"}} {
		response := perform(t, handler, http.MethodPost, path, fmt.Sprintf("invalid-input-%d", index), map[string]any{"alias": "stable", "input": value}, nil)
		var envelope api.ErrorEnvelope
		decodeResponse(t, response, &envelope)
		if response.Code != http.StatusBadRequest || envelope.Error.Code != "INVOCATION_INPUT_INVALID" || envelope.Error.CorrelationID == "" || envelope.Error.Retryable || strings.Contains(response.Body.String(), "private-") {
			t.Fatalf("invalid input response = %d %s", response.Code, response.Body.String())
		}
	}
	page := perform(t, handler, http.MethodGet, path, "", nil, nil)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `"items":[]`) {
		t.Fatal("invalid input created an invocation")
	}
	body := map[string]any{"alias": "stable", "input": map[string]any{"prompt": "valid"}}
	accepted := performJSON[api.Invocation](t, handler, http.MethodPost, path, "input-accepted", body, http.StatusAccepted)
	next := create(map[string]any{"type": "object", "required": []string{"count"}, "properties": map[string]any{"count": map[string]any{"type": "integer"}}}, "input-next")
	performJSON[api.ServiceAlias](t, handler, http.MethodPut, serviceAliasPath(testDomain, service.Metadata.ID, "stable"), "input-move-route", map[string]any{"revisionId": next.Metadata.ID, "expectedVersion": alias.Metadata.Version}, http.StatusOK)
	replayed := performJSON[api.Invocation](t, handler, http.MethodPost, path, "input-accepted", body, http.StatusAccepted)
	if replayed.Metadata.ID != accepted.Metadata.ID || replayed.RevisionID != revision.Metadata.ID {
		t.Fatal("accepted replay was revalidated against a different revision")
	}
	fresh := perform(t, handler, http.MethodPost, path, "input-new-attempt", body, nil)
	if fresh.Code != http.StatusBadRequest {
		t.Fatal("new invocation did not validate the newly resolved revision")
	}
}

func TestInvocationAdmissionRejectsUnverifiableSchemaSafely(t *testing.T) {
	handler := newHandler(t)
	service := createService(t, handler, testDomain, "unsafe-input-service")
	draft := performJSON[api.ServiceRevision](t, handler, http.MethodPost, revisionCollectionPath(testDomain, service.Metadata.ID), "unsafe-input-draft", map[string]any{"runtimeProfile": "reference/v1", "inputSchema": map[string]any{"$ref": "https://private.example/schema"}}, http.StatusCreated)
	revision := performJSON[api.ServiceRevision](t, handler, http.MethodPost, "/v1/isolation-domains/"+testDomain+"/service-revisions/"+draft.Metadata.ID+"/actions/publish", "unsafe-input-publish", map[string]any{"expectedVersion": 1}, http.StatusOK)
	assignAlias(t, handler, testDomain, service.Metadata.ID, revision.Metadata.ID, "unsafe-input-alias")
	response := perform(t, handler, http.MethodPost, "/v1/isolation-domains/"+testDomain+"/agent-services/"+service.Metadata.ID+"/invocations", "unsafe-input-invoke", map[string]any{"alias": "stable", "input": map[string]any{}}, nil)
	var envelope api.ErrorEnvelope
	decodeResponse(t, response, &envelope)
	if response.Code != http.StatusConflict || envelope.Error.Code != "REVISION_INPUT_SCHEMA_INVALID" || strings.Contains(response.Body.String(), "private.example") {
		t.Fatalf("unsafe schema response = %d %s", response.Code, response.Body.String())
	}
}
