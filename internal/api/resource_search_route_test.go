package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
)

func TestResourceSearchAuthorizesDomainBeforeQueryParsing(t *testing.T) {
	t.Parallel()
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler, err := api.NewHandler(authenticator, authorizerFunc(func(_ context.Context, request authz.Request) error {
		called = true
		if request.Action != authz.SearchResources || request.ResourceType != authz.IsolationDomain || request.ResourceID != testDomain || request.IsolationDomainID != testDomain || request.Principal.ID() != testActor {
			t.Fatal("resource search lost authenticated scope")
		}
		return authz.ErrDenied
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/isolation-domains/"+testDomain+"/search?q=x&limit=invalid", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || called {
		t.Fatal("unauthenticated discovery reached authorization")
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || !called {
		t.Fatalf("unauthorized discovery: %d", response.Code)
	}
}

func TestExistingDiscoveryGrantsDoNotGrantResourceSearch(t *testing.T) {
	t.Parallel()
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{PolicySetID: "existing-discovery", Schema: authz.CanonicalAPICedarSchema(), Policies: []byte(`permit(principal, action in [DataGround::Action::"listWorkspaces", DataGround::Action::"listAgentServices", DataGround::Action::"listScopeInvocations", DataGround::Action::"listScopeArtifacts"], resource);`)})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewHandler(authenticator, policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"/workspaces", "/agent-services", "/invocations", "/artifacts", "/search?q=research"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/isolation-domains/"+testDomain+suffix, nil)
		request.Header.Set("Authorization", "Bearer "+testToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		expected := http.StatusOK
		if suffix == "/search?q=research" {
			expected = http.StatusForbidden
		}
		if response.Code != expected {
			t.Fatalf("%s: %d", suffix, response.Code)
		}
	}
}
