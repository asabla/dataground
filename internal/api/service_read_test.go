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

func TestExactServiceRead(t *testing.T) {
	handler := newHandler(t)
	service := createService(t, handler, testDomain, "exact-service")
	path := serviceCollectionPath(testDomain) + "/" + service.Metadata.ID
	got := performJSON[api.AgentService](t, handler, http.MethodGet, path, "", nil, http.StatusOK)
	if !reflect.DeepEqual(got, service) {
		t.Fatal("exact service changed")
	}
	missingPath := serviceCollectionPath(testDomain) + "/svc_00000000000000000099"
	response := perform(t, handler, http.MethodGet, missingPath, "", nil, nil)
	var problem api.ErrorEnvelope
	decodeResponse(t, response, &problem)
	if response.Code != http.StatusNotFound || problem.Error.Code != "RESOURCE_NOT_FOUND" || problem.Error.CorrelationID == "" {
		t.Fatal("missing service did not fail safely")
	}
	response = perform(t, handler, http.MethodGet, serviceCollectionPath("iso_00000000000000000002")+"/"+service.Metadata.ID, "", nil, nil)
	if response.Code != http.StatusForbidden {
		t.Fatal("service crossed domain membership")
	}
}

func TestExactServiceReadAuthorizesPathDerivedResource(t *testing.T) {
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler, err := api.NewHandler(authenticator, authorizerFunc(func(_ context.Context, request authz.Request) error {
		called = true
		if request.Action != authz.ReadAgentService || request.ResourceType != authz.AgentService || request.ResourceID != "svc_00000000000000000001" || request.IsolationDomainID != testDomain {
			t.Fatal("wrong exact-service authorization")
		}
		return authz.ErrDenied
	}))
	if err != nil {
		t.Fatal(err)
	}
	response := perform(t, handler, http.MethodGet, "/v1/isolation-domains/"+testDomain+"/agent-services/svc_00000000000000000001", "", nil, nil)
	if !called || response.Code != http.StatusForbidden {
		t.Fatal("service lookup preceded authorization")
	}
}
