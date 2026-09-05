package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
)

func TestAliasListingContinuesAfterWithdrawalAndExcludesOtherServices(t *testing.T) {
	handler := newHandler(t)
	service := createService(t, handler, testDomain, "alias-discovery")
	revision := createPublishedRevision(t, handler, testDomain, service.Metadata.ID)
	other := createService(t, handler, testDomain, "other-alias-service")
	otherRevision := createPublishedRevision(t, handler, testDomain, other.Metadata.ID)
	assignAlias(t, handler, testDomain, other.Metadata.ID, otherRevision.Metadata.ID, "other-route")
	for _, name := range []string{"z", "a0", "a-1", "a"} {
		performJSON[api.ServiceAlias](t, handler, http.MethodPut, serviceAliasPath(testDomain, service.Metadata.ID, name), "discover-"+name, map[string]any{"revisionId": revision.Metadata.ID}, http.StatusOK)
	}
	type page struct {
		Items      []api.ServiceAlias `json:"items"`
		NextCursor string             `json:"nextCursor"`
	}
	path := "/v1/isolation-domains/" + testDomain + "/agent-services/" + service.Metadata.ID + "/aliases"
	first := performJSON[page](t, handler, http.MethodGet, path+"?limit=2", "", nil, http.StatusOK)
	if len(first.Items) != 2 || first.Items[0].Name != "a" || first.Items[1].Name != "a-1" || first.NextCursor == "" {
		t.Fatalf("first page: %+v", first)
	}
	for _, name := range []string{"a-1", "z"} {
		performJSON[api.ServiceAlias](t, handler, http.MethodPost, serviceAliasPath(testDomain, service.Metadata.ID, name)+"/actions/withdraw", "withdraw-list-"+name, map[string]any{"expectedVersion": 1}, http.StatusOK)
	}
	second := performJSON[page](t, handler, http.MethodGet, path+"?limit=2&cursor="+first.NextCursor, "", nil, http.StatusOK)
	if len(second.Items) != 1 || second.Items[0].Name != "a0" || second.NextCursor != "" {
		t.Fatalf("continuation: %+v", second)
	}
	// Recreated names before the cursor appear on refresh, never as duplicates in continuation.
	performJSON[api.ServiceAlias](t, handler, http.MethodPut, serviceAliasPath(testDomain, service.Metadata.ID, "a-1"), "recreate-list", map[string]any{"revisionId": revision.Metadata.ID}, http.StatusOK)
	refreshed := performJSON[page](t, handler, http.MethodGet, path, "", nil, http.StatusOK)
	if len(refreshed.Items) != 3 || refreshed.Items[1].Metadata.Version != 3 || refreshed.Items[1].WithdrawnAt != nil {
		t.Fatalf("refresh: %+v", refreshed)
	}
	for _, item := range refreshed.Items {
		if item.ServiceID != service.Metadata.ID || item.Metadata.IsolationDomainID != testDomain {
			t.Fatal("listing crossed scope")
		}
	}
	wrongPath := "/v1/isolation-domains/" + testDomain + "/agent-services/" + other.Metadata.ID + "/aliases?cursor=" + first.NextCursor
	if got := perform(t, handler, http.MethodGet, wrongPath, "", nil, nil); got.Code != http.StatusBadRequest {
		t.Fatal("accepted cursor from another service")
	}
	missing := "/v1/isolation-domains/" + testDomain + "/agent-services/svc_00000000000000000099/aliases"
	if got := perform(t, handler, http.MethodGet, missing, "", nil, nil); got.Code != http.StatusNotFound {
		t.Fatal("missing service was not distinguished from empty routes")
	}
	empty := createService(t, handler, testDomain, "no-routes")
	result := performJSON[page](t, handler, http.MethodGet, "/v1/isolation-domains/"+testDomain+"/agent-services/"+empty.Metadata.ID+"/aliases", "", nil, http.StatusOK)
	if result.Items == nil || len(result.Items) != 0 || result.NextCursor != "" {
		t.Fatal("empty page must be an empty array")
	}
}

func TestAliasListingAuthorizesExactServiceBeforeParsing(t *testing.T) {
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler, err := api.NewHandler(authenticator, authorizerFunc(func(_ context.Context, request authz.Request) error {
		called = true
		if request.Action != authz.ListServiceAliases || request.ResourceType != authz.AgentService || request.ResourceID != "svc_00000000000000000001" || request.IsolationDomainID != testDomain {
			t.Fatal("wrong authorization scope")
		}
		return authz.ErrDenied
	}))
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/isolation-domains/" + testDomain + "/agent-services/svc_00000000000000000001/aliases?limit=bad"
	response := perform(t, handler, http.MethodGet, path, "", nil, nil)
	if !called || response.Code != http.StatusForbidden {
		t.Fatal("list parsed before authorization")
	}
}
