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

func TestScopeArtifactDiscoveryAuthorizesDomainBeforeQueryParsing(t *testing.T) {
	t.Parallel()
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler, err := api.NewHandler(authenticator, authorizerFunc(func(_ context.Context, request authz.Request) error {
		called = true
		if request.Action != authz.ListScopeArtifacts || request.ResourceType != authz.IsolationDomain || request.ResourceID != testDomain || request.IsolationDomainID != testDomain || request.Principal.ID() != testActor {
			t.Fatal("scope artifact discovery lost authenticated scope")
		}
		return authz.ErrDenied
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/isolation-domains/"+testDomain+"/artifacts?limit=invalid", nil)
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
