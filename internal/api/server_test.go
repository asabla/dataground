package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/reference"
)

const (
	testDomain      = "iso_00000000000000000001"
	otherTestDomain = "iso_00000000000000000002"
	testActor       = "usr_00000000000000000001"
	testToken       = "development-token-with-at-least-thirty-two-bytes"
)

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/livez", "/readyz"} {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()

			newHandler(t).ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("expected no-store cache policy")
			}
			if response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("expected JSON content type")
			}
			if response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("expected nosniff content policy")
			}

			var body struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Status != "ok" {
				t.Fatalf("expected ok status, got %q", body.Status)
			}
		})
	}
}

func TestReferenceRuntimeLifecycleAndReplay(t *testing.T) {
	t.Parallel()

	handler := newHandler(t)
	service := createService(t, handler, testDomain, "service-create-0001")

	replayed := performJSON[api.AgentService](t, handler, http.MethodPost, serviceCollectionPath(testDomain), "service-create-0001", map[string]any{"name": "Reference service"}, http.StatusCreated)
	if replayed.Metadata.ID != service.Metadata.ID {
		t.Fatalf("idempotent replay changed service ID: %q != %q", replayed.Metadata.ID, service.Metadata.ID)
	}

	conflict := perform(t, handler, http.MethodPost, serviceCollectionPath(testDomain), "service-create-0001", map[string]any{"name": "Different service"}, nil)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("expected idempotency conflict, got %d", conflict.Code)
	}
	var conflictBody api.ErrorEnvelope
	decodeResponse(t, conflict, &conflictBody)
	if conflictBody.Error.Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("expected stable idempotency error, got %q", conflictBody.Error.Code)
	}

	revision := createPublishedRevision(t, handler, testDomain, service.Metadata.ID)
	alias := performJSON[api.ServiceAlias](
		t,
		handler,
		http.MethodPut,
		fmt.Sprintf("/v1/isolation-domains/%s/agent-services/%s/aliases/stable", testDomain, service.Metadata.ID),
		"alias-assign-0001",
		map[string]any{"revisionId": revision.Metadata.ID, "expectedVersion": 0},
		http.StatusOK,
	)
	if alias.RevisionID != revision.Metadata.ID {
		t.Fatalf("alias target mismatch: %q", alias.RevisionID)
	}

	invocation := invoke(t, handler, testDomain, service.Metadata.ID, reference.ScenarioSuccess, "invoke-success-0001")
	if invocation.State != "succeeded" {
		t.Fatalf("expected succeeded invocation, got %q", invocation.State)
	}
	if invocation.Usage == nil || invocation.Usage.TotalTokens != 20 {
		t.Fatalf("expected normalized usage, got %#v", invocation.Usage)
	}

	invocationPath := fmt.Sprintf("/v1/isolation-domains/%s/invocations/%s", testDomain, invocation.Metadata.ID)
	observed := performJSON[api.Invocation](t, handler, http.MethodGet, invocationPath, "", nil, http.StatusOK)
	if observed.RevisionID != revision.Metadata.ID || observed.OperationID == "" {
		t.Fatalf("invocation lost revision or operation identity: %#v", observed)
	}

	journal := streamEvents(t, handler, invocationPath+"/events", "")
	wantTypes := []string{
		"lifecycle.started",
		"output.text.delta",
		"activity.tool.started",
		"activity.tool.completed",
		"activity.process.started",
		"activity.process.completed",
		"usage.recorded",
		"lifecycle.succeeded",
	}
	assertEventTypes(t, journal, wantTypes)

	replayedJournal := streamEvents(t, handler, invocationPath+"/events", "6")
	assertEventTypes(t, replayedJournal, wantTypes[6:])
	if replayedJournal[0].ID != journal[6].ID {
		t.Fatal("reconnect replay changed event identity")
	}

	crossDomainPath := fmt.Sprintf("/v1/isolation-domains/%s/invocations/%s", otherTestDomain, invocation.Metadata.ID)
	crossDomain := perform(t, handler, http.MethodGet, crossDomainPath, "", nil, nil)
	if crossDomain.Code != http.StatusForbidden {
		t.Fatalf("expected cross-domain lookup to fail closed, got %d", crossDomain.Code)
	}
}

func TestArtifactAndUnknownEventRemainGoverned(t *testing.T) {
	t.Parallel()

	handler := newHandler(t)
	service := createService(t, handler, testDomain, "artifact-service-0001")
	revision := createPublishedRevision(t, handler, testDomain, service.Metadata.ID)
	assignAlias(t, handler, testDomain, service.Metadata.ID, revision.Metadata.ID, "artifact-alias-0001")

	artifactInvocation := invoke(t, handler, testDomain, service.Metadata.ID, reference.ScenarioArtifact, "invoke-artifact-0001")
	if len(artifactInvocation.ArtifactIDs) != 1 {
		t.Fatalf("expected one artifact reference, got %v", artifactInvocation.ArtifactIDs)
	}
	artifactPath := fmt.Sprintf(
		"/v1/isolation-domains/%s/invocations/%s/artifacts/%s",
		testDomain,
		artifactInvocation.Metadata.ID,
		artifactInvocation.ArtifactIDs[0],
	)
	artifact := performJSON[api.ArtifactDescriptor](t, handler, http.MethodGet, artifactPath, "", nil, http.StatusOK)
	if artifact.State != "available" || !strings.HasPrefix(artifact.Digest, "sha256:") {
		t.Fatalf("artifact descriptor is incomplete: %#v", artifact)
	}
	journalPath := fmt.Sprintf("/v1/isolation-domains/%s/invocations/%s/events", testDomain, artifactInvocation.Metadata.ID)
	journal := streamEvents(t, handler, journalPath, "")
	encoded, err := json.Marshal(journal[1].Payload)
	if err != nil {
		t.Fatalf("encode artifact event: %v", err)
	}
	if bytes.Contains(encoded, []byte("content")) {
		t.Fatal("artifact event inlined content")
	}

	unknownInvocation := invoke(t, handler, testDomain, service.Metadata.ID, reference.ScenarioUnknownOptional, "invoke-unknown-0001")
	unknownPath := fmt.Sprintf("/v1/isolation-domains/%s/invocations/%s/events", testDomain, unknownInvocation.Metadata.ID)
	unknownJournal := streamEvents(t, handler, unknownPath, "")
	if unknownJournal[1].Type != "runtime.future.signal" || len(unknownJournal[1].Extensions) != 1 {
		t.Fatalf("unknown optional event was not preserved: %#v", unknownJournal[1])
	}
}

func TestWaitingInvocationCanBeCancelledAndReplayed(t *testing.T) {
	t.Parallel()

	handler := newHandler(t)
	service := createService(t, handler, testDomain, "cancel-service-0001")
	revision := createPublishedRevision(t, handler, testDomain, service.Metadata.ID)
	assignAlias(t, handler, testDomain, service.Metadata.ID, revision.Metadata.ID, "cancel-alias-0001")
	invocation := invoke(t, handler, testDomain, service.Metadata.ID, reference.ScenarioQuestion, "invoke-question-0001")
	if invocation.State != "waiting" {
		t.Fatalf("expected waiting invocation, got %q", invocation.State)
	}

	cancelPath := fmt.Sprintf("/v1/isolation-domains/%s/invocations/%s/actions/cancel", testDomain, invocation.Metadata.ID)
	cancelled := performJSON[api.Invocation](t, handler, http.MethodPost, cancelPath, "cancel-invocation-0001", map[string]any{"reason": "test"}, http.StatusOK)
	if cancelled.State != "cancelled" || cancelled.CompletedAt == nil {
		t.Fatalf("expected terminal cancellation, got %#v", cancelled)
	}
	replayed := performJSON[api.Invocation](t, handler, http.MethodPost, cancelPath, "cancel-invocation-0001", map[string]any{"reason": "test"}, http.StatusOK)
	if replayed.Metadata.Version != cancelled.Metadata.Version {
		t.Fatal("idempotent cancellation repeated the state transition")
	}

	journalPath := fmt.Sprintf("/v1/isolation-domains/%s/invocations/%s/events", testDomain, invocation.Metadata.ID)
	journal := streamEvents(t, handler, journalPath, "3")
	assertEventTypes(t, journal, []string{"lifecycle.cancelled"})
}

func TestAllReferenceRuntimeOutcomesReachTheAPI(t *testing.T) {
	t.Parallel()

	handler := newHandler(t)
	service := createService(t, handler, testDomain, "scenario-service-0001")
	revision := createPublishedRevision(t, handler, testDomain, service.Metadata.ID)
	assignAlias(t, handler, testDomain, service.Metadata.ID, revision.Metadata.ID, "scenario-alias-0001")

	tests := []struct {
		scenario  string
		state     string
		events    int
		retryable *bool
	}{
		{scenario: reference.ScenarioApproval, state: "waiting", events: 3},
		{scenario: reference.ScenarioCancellation, state: "cancelled", events: 2},
		{scenario: reference.ScenarioRetryableFailure, state: "failed", events: 3, retryable: boolPointer(true)},
		{scenario: reference.ScenarioTerminalFailure, state: "failed", events: 3, retryable: boolPointer(false)},
		{scenario: reference.ScenarioDuplicate, state: "succeeded", events: 3},
		{scenario: reference.ScenarioOutOfOrder, state: "succeeded", events: 4},
	}
	for _, test := range tests {
		test := test
		t.Run(test.scenario, func(t *testing.T) {
			invocation := invoke(t, handler, testDomain, service.Metadata.ID, test.scenario, "scenario-invoke-"+test.scenario)
			if invocation.State != test.state {
				t.Fatalf("expected state %q, got %q", test.state, invocation.State)
			}
			if test.retryable != nil {
				if invocation.Error == nil || invocation.Error.Retryable != *test.retryable || invocation.Error.CorrelationID != invocation.CorrelationID {
					t.Fatalf("expected correlated retryable=%v error, got %#v", *test.retryable, invocation.Error)
				}
			}
			path := fmt.Sprintf("/v1/isolation-domains/%s/invocations/%s/events", testDomain, invocation.Metadata.ID)
			journal := streamEvents(t, handler, path, "")
			if len(journal) != test.events {
				t.Fatalf("expected %d normalized events, got %d", test.events, len(journal))
			}
			for index, event := range journal {
				if event.Sequence != uint64(index+1) {
					t.Fatalf("expected sequence %d, got %d", index+1, event.Sequence)
				}
			}
		})
	}
}

func TestMutationsRequireValidIdempotencyKeyAndStrictJSON(t *testing.T) {
	t.Parallel()

	handler := newHandler(t)
	missingKey := perform(t, handler, http.MethodPost, serviceCollectionPath(testDomain), "", map[string]any{"name": "Service"}, nil)
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("expected missing idempotency key rejection, got %d", missingKey.Code)
	}

	unknownField := perform(t, handler, http.MethodPost, serviceCollectionPath(testDomain), "strict-json-0001", map[string]any{"name": "Service", "unexpected": true}, nil)
	if unknownField.Code != http.StatusBadRequest {
		t.Fatalf("expected unknown field rejection, got %d", unknownField.Code)
	}
	var body api.ErrorEnvelope
	decodeResponse(t, unknownField, &body)
	if body.Error.CorrelationID == "" || body.Error.Retryable {
		t.Fatalf("expected safe correlated validation error, got %#v", body.Error)
	}

	unsupportedMedia := perform(
		t,
		handler,
		http.MethodPost,
		serviceCollectionPath(testDomain),
		"unsupported-media-0001",
		map[string]any{"name": "Service"},
		map[string]string{"Content-Type": "text/plain"},
	)
	if unsupportedMedia.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("expected unsupported media type rejection, got %d", unsupportedMedia.Code)
	}
}

func TestPublicationBlocksUnavailableRuntimeCapabilities(t *testing.T) {
	t.Parallel()

	handler := newHandler(t)
	service := createService(t, handler, testDomain, "capability-service-0001")
	revisionPath := fmt.Sprintf("/v1/isolation-domains/%s/agent-services/%s/revisions", testDomain, service.Metadata.ID)
	revision := performJSON[api.ServiceRevision](
		t,
		handler,
		http.MethodPost,
		revisionPath,
		"capability-revision-0001",
		map[string]any{"runtimeProfile": "reference/v1", "requiredCapabilities": []string{"future-capability"}},
		http.StatusCreated,
	)
	publishPath := fmt.Sprintf("/v1/isolation-domains/%s/service-revisions/%s/actions/publish", testDomain, revision.Metadata.ID)
	response := perform(t, handler, http.MethodPost, publishPath, "capability-publish-0001", map[string]any{"expectedVersion": 1}, nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("expected publication to fail closed, got %d", response.Code)
	}
	var body api.ErrorEnvelope
	decodeResponse(t, response, &body)
	if body.Error.Code != "REQUIRED_CAPABILITY_UNAVAILABLE" {
		t.Fatalf("expected stable capability error, got %q", body.Error.Code)
	}
}

func createService(t *testing.T, handler http.Handler, domainID, key string) api.AgentService {
	t.Helper()
	service := performJSON[api.AgentService](t, handler, http.MethodPost, serviceCollectionPath(domainID), key, map[string]any{"name": "Reference service"}, http.StatusCreated)
	if service.Metadata.CreatedBy != testActor {
		t.Fatalf("service attribution mismatch: %q", service.Metadata.CreatedBy)
	}
	return service
}

func serviceCollectionPath(domainID string) string {
	return fmt.Sprintf("/v1/isolation-domains/%s/agent-services", domainID)
}

func createPublishedRevision(t *testing.T, handler http.Handler, domainID, serviceID string) api.ServiceRevision {
	t.Helper()
	revisionPath := fmt.Sprintf("/v1/isolation-domains/%s/agent-services/%s/revisions", domainID, serviceID)
	revision := performJSON[api.ServiceRevision](
		t,
		handler,
		http.MethodPost,
		revisionPath,
		"revision-create-"+serviceID,
		map[string]any{"runtimeProfile": "reference/v1", "requiredCapabilities": []string{"usage", "cancellation"}},
		http.StatusCreated,
	)
	publishPath := fmt.Sprintf("/v1/isolation-domains/%s/service-revisions/%s/actions/publish", domainID, revision.Metadata.ID)
	return performJSON[api.ServiceRevision](t, handler, http.MethodPost, publishPath, "revision-publish-"+revision.Metadata.ID, map[string]any{"expectedVersion": 1}, http.StatusOK)
}

func assignAlias(t *testing.T, handler http.Handler, domainID, serviceID, revisionID, key string) api.ServiceAlias {
	t.Helper()
	aliasPath := fmt.Sprintf("/v1/isolation-domains/%s/agent-services/%s/aliases/stable", domainID, serviceID)
	return performJSON[api.ServiceAlias](t, handler, http.MethodPut, aliasPath, key, map[string]any{"revisionId": revisionID, "expectedVersion": 0}, http.StatusOK)
}

func invoke(t *testing.T, handler http.Handler, domainID, serviceID, scenario, key string) api.Invocation {
	t.Helper()
	path := fmt.Sprintf("/v1/isolation-domains/%s/agent-services/%s/invocations", domainID, serviceID)
	return performJSON[api.Invocation](t, handler, http.MethodPost, path, key, map[string]any{"alias": "stable", "input": map[string]any{"scenario": scenario}}, http.StatusAccepted)
}

func performJSON[T any](t *testing.T, handler http.Handler, method, path, idempotencyKey string, body any, wantStatus int) T {
	t.Helper()
	response := perform(t, handler, method, path, idempotencyKey, body, nil)
	if response.Code != wantStatus {
		t.Fatalf("%s %s: expected status %d, got %d: %s", method, path, wantStatus, response.Code, response.Body.String())
	}
	var result T
	decodeResponse(t, response, &result)
	return result
}

func perform(t *testing.T, handler http.Handler, method, path, idempotencyKey string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	if strings.HasPrefix(path, "/v1/") {
		request.Header.Set("Authorization", "Bearer "+testToken)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func streamEvents(t *testing.T, handler http.Handler, path, cursor string) []api.EventEnvelope {
	t.Helper()
	headers := map[string]string{}
	if cursor != "" {
		headers["Last-Event-ID"] = cursor
	}
	response := perform(t, handler, http.MethodGet, path, "", nil, headers)
	if response.Code != http.StatusOK {
		t.Fatalf("stream events: expected status 200, got %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("expected event-stream content type, got %q", response.Header().Get("Content-Type"))
	}

	var events []api.EventEnvelope
	for _, block := range strings.Split(strings.TrimSpace(response.Body.String()), "\n\n") {
		lines := strings.Split(block, "\n")
		if len(lines) != 3 {
			t.Fatalf("invalid SSE block %q", block)
		}
		sequence, err := strconv.ParseUint(strings.TrimPrefix(lines[0], "id: "), 10, 64)
		if err != nil {
			t.Fatalf("parse SSE ID: %v", err)
		}
		var event api.EventEnvelope
		if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[2], "data: ")), &event); err != nil {
			t.Fatalf("decode SSE event: %v", err)
		}
		if event.Sequence != sequence || strings.TrimPrefix(lines[1], "event: ") != event.Type {
			t.Fatalf("SSE metadata does not match event envelope: %q", block)
		}
		events = append(events, event)
	}
	return events
}

func assertEventTypes(t *testing.T, events []api.EventEnvelope, want []string) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("expected %d events, got %d", len(want), len(events))
	}
	for index, eventType := range want {
		if events[index].Type != eventType {
			t.Fatalf("event %d: expected %q, got %q", index, eventType, events[index].Type)
		}
		if events[index].ActorID == "" {
			t.Fatalf("event %d is missing actor identity", index)
		}
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func TestHealthEndpointsRejectOtherMethods(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/livez", nil)
	response := httptest.NewRecorder()

	newHandler(t).ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}

func TestProtectedRoutesAuthenticateBeforeReadingRequests(t *testing.T) {
	t.Parallel()

	handler := newHandler(t)
	reader := &countingReader{}
	request := httptest.NewRequest(http.MethodPost, serviceCollectionPath(testDomain), reader)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "unauthenticated-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated status, got %d", response.Code)
	}
	if response.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing bearer challenge")
	}
	if reader.reads != 0 {
		t.Fatalf("request body was read before authentication: %d reads", reader.reads)
	}
}

func TestIdempotencyResponsesArePrincipalBound(t *testing.T) {
	t.Parallel()

	first, err := authn.NewPrincipal(authn.PrincipalInput{
		ID: testActor, Kind: authn.PrincipalHuman, Issuer: "test",
		Subject: testActor, Audience: authn.APIAudience, IsolationDomains: []string{testDomain},
	})
	if err != nil {
		t.Fatalf("create first principal: %v", err)
	}
	secondActor := "usr_00000000000000000002"
	second, err := authn.NewPrincipal(authn.PrincipalInput{
		ID: secondActor, Kind: authn.PrincipalHuman, Issuer: "test",
		Subject: secondActor, Audience: authn.APIAudience, IsolationDomains: []string{testDomain},
	})
	if err != nil {
		t.Fatalf("create second principal: %v", err)
	}
	server := api.NewServer()
	handler, err := server.Handler(tokenAuthenticator{principals: map[string]authn.Principal{
		"first-token-with-at-least-thirty-two-bytes":  first,
		"second-token-with-at-least-thirty-two-bytes": second,
	}}, allowAuthorizer{})
	if err != nil {
		t.Fatalf("create authenticated handler: %v", err)
	}
	path := serviceCollectionPath(testDomain)
	firstResponse := perform(t, handler, http.MethodPost, path, "principal-bound-0001", map[string]any{"name": "Service"}, map[string]string{
		"Authorization": "Bearer first-token-with-at-least-thirty-two-bytes",
	})
	if firstResponse.Code != http.StatusCreated {
		t.Fatalf("first principal create failed: %d", firstResponse.Code)
	}
	secondResponse := perform(t, handler, http.MethodPost, path, "principal-bound-0001", map[string]any{"name": "Service"}, map[string]string{
		"Authorization": "Bearer second-token-with-at-least-thirty-two-bytes",
	})
	if secondResponse.Code != http.StatusConflict {
		t.Fatalf("second principal replayed another identity's response: %d", secondResponse.Code)
	}
	var problem api.ErrorEnvelope
	decodeResponse(t, secondResponse, &problem)
	if problem.Error.Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("unexpected cross-principal idempotency error: %q", problem.Error.Code)
	}
}

func TestProtectedRoutesAuthorizeBeforeReadingRequests(t *testing.T) {
	t.Parallel()

	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{
		BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain,
	})
	if err != nil {
		t.Fatalf("create development authenticator: %v", err)
	}
	handler, err := api.NewHandler(authenticator, authorizerFunc(func(_ context.Context, request authz.Request) error {
		if request.Action != authz.CreateAgentService ||
			request.ResourceType != authz.IsolationDomain ||
			request.ResourceID != testDomain ||
			request.IsolationDomainID != testDomain ||
			request.Principal.ID() != testActor {
			return authz.ErrUnavailable
		}
		return authz.ErrDenied
	}))
	if err != nil {
		t.Fatalf("create authorized handler: %v", err)
	}
	reader := &countingReader{}
	request := httptest.NewRequest(http.MethodPost, serviceCollectionPath(testDomain), reader)
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "authorization-order-0001")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden status, got %d", response.Code)
	}
	var problem api.ErrorEnvelope
	decodeResponse(t, response, &problem)
	if problem.Error.Code != "ACTION_FORBIDDEN" {
		t.Fatalf("unexpected authorization error: %q", problem.Error.Code)
	}
	if reader.reads != 0 {
		t.Fatalf("request body was read before authorization: %d reads", reader.reads)
	}
}

func TestHandlerRejectsTypedNilSecurityDependencies(t *testing.T) {
	t.Parallel()

	var authenticator *authn.DevelopmentAuthenticator
	if _, err := api.NewHandler(authenticator, allowAuthorizer{}); err == nil {
		t.Fatal("typed-nil authenticator was accepted")
	}
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{
		BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain,
	})
	if err != nil {
		t.Fatalf("create development authenticator: %v", err)
	}
	var authorizer *authz.StaticCedarAuthorizer
	if _, err := api.NewHandler(authenticator, authorizer); err == nil {
		t.Fatal("typed-nil authorizer was accepted")
	}
}

func newHandler(t *testing.T) http.Handler {
	t.Helper()
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{
		BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain,
	})
	if err != nil {
		t.Fatalf("create development authenticator: %v", err)
	}
	authorizer, err := authz.NewDevelopmentCedarAuthorizer(testActor, testDomain)
	if err != nil {
		t.Fatalf("create development authorizer: %v", err)
	}
	handler, err := api.NewHandler(authenticator, authorizer)
	if err != nil {
		t.Fatalf("create protected handler: %v", err)
	}
	return handler
}

type tokenAuthenticator struct {
	principals map[string]authn.Principal
}

func (authenticator tokenAuthenticator) Authenticate(_ context.Context, token []byte) (authn.Principal, error) {
	principal, exists := authenticator.principals[string(token)]
	if !exists {
		return authn.Principal{}, authn.ErrInvalidCredential
	}
	return principal, nil
}

type allowAuthorizer struct{}

func (allowAuthorizer) Authorize(_ context.Context, _ authz.Request) error {
	return nil
}

type authorizerFunc func(context.Context, authz.Request) error

func (authorize authorizerFunc) Authorize(ctx context.Context, request authz.Request) error {
	return authorize(ctx, request)
}

type countingReader struct {
	reads int
}

func (reader *countingReader) Read(_ []byte) (int, error) {
	reader.reads++
	return 0, io.EOF
}

var _ authn.Authenticator = tokenAuthenticator{}
var _ authz.Authorizer = allowAuthorizer{}
var _ authz.Authorizer = authorizerFunc(nil)
