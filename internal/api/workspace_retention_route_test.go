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

func TestWorkspaceRetentionAuthorizesMetadataBeforeParsing(t *testing.T) {
	t.Parallel()
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
	if err != nil {
		t.Fatal(err)
	}
	called, allowed := false, false
	handler, err := api.NewHandler(authenticator, authorizerFunc(func(_ context.Context, request authz.Request) error {
		called = true
		if request.Action != authz.ReadWorkspaceRetention || request.ResourceType != authz.Invocation || request.ResourceID != "inv_00000000000000000001" || request.IsolationDomainID != testDomain || request.Principal.ID() != testActor {
			t.Fatal("retention read lost authenticated invocation scope")
		}
		if !allowed {
			return authz.ErrDenied
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/isolation-domains/" + testDomain + "/invocations/inv_00000000000000000001/workspace-retention"
	for _, test := range []struct {
		token, query string
		allow        bool
		status       int
	}{
		{"", "?private=invalid", false, http.StatusUnauthorized},
		{testToken, "?private=invalid", false, http.StatusForbidden},
		{testToken, "?private=invalid", true, http.StatusBadRequest},
		{testToken, "", true, http.StatusNotFound},
	} {
		called, allowed = false, test.allow
		request := httptest.NewRequest(http.MethodGet, path+test.query, nil)
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status || called != (test.token != "") {
			t.Fatalf("retention response %d, authorization %v", response.Code, called)
		}
		if test.allow && response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("retention response can be cached")
		}
	}
}
