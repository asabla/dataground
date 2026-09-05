package api_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
)

func TestExactRevisionReadPreservesDefinitionAcrossPublicationAndRetirement(t *testing.T) {
	handler := newHandler(t)
	service := createService(t, handler, testDomain, "exact-revision")
	draft := performJSON[api.ServiceRevision](t, handler, http.MethodPost, revisionCollectionPath(testDomain, service.Metadata.ID), "exact-draft", map[string]any{"runtimeProfile": "reference/v1", "requiredCapabilities": []string{}, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"prompt": map[string]any{"type": "string"}}}}, http.StatusCreated)
	path := "/v1/isolation-domains/" + testDomain + "/service-revisions/" + draft.Metadata.ID
	read := performJSON[api.ServiceRevision](t, handler, http.MethodGet, path, "", nil, http.StatusOK)
	if !reflect.DeepEqual(read, draft) {
		t.Fatal("draft read changed definition")
	}
	published := performJSON[api.ServiceRevision](t, handler, http.MethodPost, path+"/actions/publish", "exact-publish", map[string]any{"expectedVersion": 1}, http.StatusOK)
	performJSON[api.ServiceRevision](t, handler, http.MethodPost, revisionCollectionPath(testDomain, service.Metadata.ID), "exact-newer", map[string]any{"runtimeProfile": "reference/v1", "requiredCapabilities": []string{}}, http.StatusCreated)
	read = performJSON[api.ServiceRevision](t, handler, http.MethodGet, path, "", nil, http.StatusOK)
	if !reflect.DeepEqual(read, published) {
		t.Fatal("exact read selected a newer revision or changed publication")
	}
	retired := performJSON[api.ServiceRevision](t, handler, http.MethodPost, path+"/actions/retire", "exact-retire", map[string]any{"expectedVersion": published.Metadata.Version}, http.StatusOK)
	read = performJSON[api.ServiceRevision](t, handler, http.MethodGet, path, "", nil, http.StatusOK)
	if !reflect.DeepEqual(read, retired) || !reflect.DeepEqual(read.InputSchema, draft.InputSchema) {
		t.Fatal("retirement lost historical definition")
	}
	missing := perform(t, handler, http.MethodGet, "/v1/isolation-domains/"+testDomain+"/service-revisions/rev_00000000000000000099", "", nil, nil)
	var problem api.ErrorEnvelope
	decodeResponse(t, missing, &problem)
	if missing.Code != http.StatusNotFound || problem.Error.Code != "RESOURCE_NOT_FOUND" || problem.Error.CorrelationID == "" {
		t.Fatal("missing revision did not fail with safe correlation")
	}
}

func TestExactRevisionReadAuthorizesPathDerivedResource(t *testing.T) {
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler, err := api.NewHandler(authenticator, authorizerFunc(func(_ context.Context, request authz.Request) error {
		called = true
		if request.Action != authz.ReadServiceRevision || request.ResourceType != authz.ServiceRevision || request.ResourceID != "rev_00000000000000000001" || request.IsolationDomainID != testDomain {
			t.Fatal("wrong exact-revision authorization")
		}
		return authz.ErrDenied
	}))
	if err != nil {
		t.Fatal(err)
	}
	response := perform(t, handler, http.MethodGet, "/v1/isolation-domains/"+testDomain+"/service-revisions/rev_00000000000000000001", "", nil, nil)
	if !called || response.Code != http.StatusForbidden {
		t.Fatal("revision lookup preceded authorization")
	}
}
