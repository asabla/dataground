package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/api"
)

func TestReferenceInvocationOutputContractAndReplay(t *testing.T) {
	handler := newHandler(t)
	service := createService(t, handler, testDomain, "output-service")
	for index, valid := range []bool{false, true} {
		kind := "integer"
		if valid {
			kind = "string"
		}
		schema := map[string]any{"type": "object", "required": []string{"message"}, "properties": map[string]any{"message": map[string]any{"type": kind}}, "additionalProperties": false}
		key := fmt.Sprintf("output-%d", index)
		draft := performJSON[api.ServiceRevision](t, handler, http.MethodPost, revisionCollectionPath(testDomain, service.Metadata.ID), key+"-draft", map[string]any{"runtimeProfile": "reference/v1", "requiredCapabilities": []string{}, "outputSchema": schema}, http.StatusCreated)
		revision := performJSON[api.ServiceRevision](t, handler, http.MethodPost, "/v1/isolation-domains/"+testDomain+"/service-revisions/"+draft.Metadata.ID+"/actions/publish", key+"-publish", map[string]any{"expectedVersion": 1}, http.StatusOK)
		performJSON[api.ServiceAlias](t, handler, http.MethodPut, serviceAliasPath(testDomain, service.Metadata.ID, "stable"), key+"-route", map[string]any{"revisionId": revision.Metadata.ID, "expectedVersion": index}, http.StatusOK)
		path := "/v1/isolation-domains/" + testDomain + "/agent-services/" + service.Metadata.ID + "/invocations"
		body := map[string]any{"alias": "stable", "input": map[string]any{"scenario": "success"}}
		accepted := perform(t, handler, http.MethodPost, path, key+"-invoke", body, nil)
		acceptedBytes := accepted.Body.String()
		var invocation api.Invocation
		decodeResponse(t, accepted, &invocation)
		if accepted.Code != http.StatusAccepted {
			t.Fatal(accepted.Body.String())
		}
		operation := performJSON[api.Operation](t, handler, http.MethodGet, "/v1/isolation-domains/"+testDomain+"/operations/"+invocation.OperationID, "", nil, http.StatusOK)
		if valid {
			if invocation.State != "succeeded" || invocation.Result["message"] == nil || invocation.Error != nil {
				t.Fatalf("valid result rejected: %+v", invocation)
			}
		} else {
			if invocation.State != "failed" || invocation.Result != nil || invocation.Error == nil || invocation.Error.Code != "INVOCATION_OUTPUT_INVALID" || invocation.Error.CorrelationID != invocation.CorrelationID || invocation.CompletedAt == nil {
				t.Fatalf("invalid result admitted: %+v", invocation)
			}
			if operation.ObservedState != "failed" || operation.TerminalResult != nil || operation.Error == nil || operation.Error.Code != invocation.Error.Code {
				t.Fatalf("operation failure diverged: %+v", operation)
			}
			events := perform(t, handler, http.MethodGet, "/v1/isolation-domains/"+testDomain+"/invocations/"+invocation.Metadata.ID+"/events", "", nil, nil)
			if strings.Contains(events.Body.String(), "lifecycle.succeeded") || !strings.Contains(events.Body.String(), "lifecycle.failed") {
				t.Fatalf("journal exposed invalid completion: %s", events.Body.String())
			}
		}
		cancelled := performJSON[api.Invocation](t, handler, http.MethodPost, path, key+"-cancelled", map[string]any{"alias": "stable", "input": map[string]any{"scenario": "cancellation"}}, http.StatusAccepted)
		if cancelled.State != "cancelled" || cancelled.Result != nil || cancelled.Error != nil {
			t.Fatalf("output validation changed cancellation: %+v", cancelled)
		}
		replay := perform(t, handler, http.MethodPost, path, key+"-invoke", body, nil)
		if replay.Body.String() != acceptedBytes {
			t.Fatal("completion replay changed")
		}
	}
}
