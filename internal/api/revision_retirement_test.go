package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
)

func TestRevisionRetirementRequiresNoRoutingOrActiveInvocations(t *testing.T) {
	handler := newHandler(t)
	service := createService(t, handler, testDomain, "retirement-service")
	revision := createPublishedRevision(t, handler, testDomain, service.Metadata.ID)
	alias := assignAlias(t, handler, testDomain, service.Metadata.ID, revision.Metadata.ID, "retirement-alias")
	invocation := invoke(t, handler, testDomain, service.Metadata.ID, "question", "retirement-invocation")
	path := "/v1/isolation-domains/" + testDomain + "/service-revisions/" + revision.Metadata.ID + "/actions/retire"
	body := map[string]any{"expectedVersion": revision.Metadata.Version}
	check := func(key, code string) {
		t.Helper()
		r := perform(t, handler, http.MethodPost, path, key, body, nil)
		var problem api.ErrorEnvelope
		decodeResponse(t, r, &problem)
		if r.Code != http.StatusConflict || problem.Error.Code != code {
			t.Fatalf("retirement denial = %d %s", r.Code, problem.Error.Code)
		}
	}
	check("retirement-routed", "REVISION_STILL_ROUTED")
	next := performJSON[api.ServiceRevision](t, handler, http.MethodPost, revisionCollectionPath(testDomain, service.Metadata.ID), "retirement-next-revision", map[string]any{"runtimeProfile": "reference/v1", "requiredCapabilities": []string{}}, http.StatusCreated)
	next = performJSON[api.ServiceRevision](t, handler, http.MethodPost, "/v1/isolation-domains/"+testDomain+"/service-revisions/"+next.Metadata.ID+"/actions/publish", "retirement-next-publish", map[string]any{"expectedVersion": 1}, http.StatusOK)
	performJSON[api.ServiceAlias](t, handler, http.MethodPut, serviceAliasPath(testDomain, service.Metadata.ID, "stable"), "retirement-move-alias", map[string]any{"revisionId": next.Metadata.ID, "expectedVersion": alias.Metadata.Version}, http.StatusOK)
	check("retirement-active", "REVISION_STILL_ACTIVE")
	performJSON[api.Invocation](t, handler, http.MethodPost, "/v1/isolation-domains/"+testDomain+"/invocations/"+invocation.Metadata.ID+"/actions/cancel", "retirement-cancel", map[string]any{}, http.StatusOK)
	retired := performJSON[api.ServiceRevision](t, handler, http.MethodPost, path, "retirement-complete", body, http.StatusOK)
	if retired.State != "retired" || retired.Metadata.Version != revision.Metadata.Version+1 || retired.PublishedAt == nil {
		t.Fatal("retirement lost revision identity or publication history")
	}
	replay := performJSON[api.ServiceRevision](t, handler, http.MethodPost, path, "retirement-complete", body, http.StatusOK)
	if replay.Metadata.Version != retired.Metadata.Version {
		t.Fatal("retirement replay changed the revision")
	}
	check("retirement-stale-version", "VERSION_CONFLICT")
	response := perform(t, handler, http.MethodPut, serviceAliasPath(testDomain, service.Metadata.ID, "old-route"), "retirement-reassign", map[string]any{"revisionId": revision.Metadata.ID, "expectedVersion": 0}, nil)
	if response.Code != http.StatusConflict {
		t.Fatal("retired revision accepted new routing")
	}
	read := performJSON[api.Invocation](t, handler, http.MethodGet, "/v1/isolation-domains/"+testDomain+"/invocations/"+invocation.Metadata.ID, "", nil, http.StatusOK)
	if read.RevisionID != revision.Metadata.ID || read.State != "cancelled" {
		t.Fatal("retirement removed invocation history")
	}
}

func TestRevisionRetirementValidatesAndAuthorizesExactRevision(t *testing.T) {
	handler := newHandler(t)
	service := createService(t, handler, testDomain, "retire-validation-service")
	revision := createPublishedRevision(t, handler, testDomain, service.Metadata.ID)
	path := "/v1/isolation-domains/" + testDomain + "/service-revisions/" + revision.Metadata.ID + "/actions/retire"
	for index, body := range []map[string]any{{}, {"expectedVersion": 0}, {"expectedVersion": -1}, {"expectedVersion": 2, "force": true}} {
		response := perform(t, handler, http.MethodPost, path, string(rune('a'+index))+"-retire-invalid", body, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid retirement status=%d", response.Code)
		}
	}
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	denied, err := api.NewHandler(authenticator, authorizerFunc(func(_ context.Context, request authz.Request) error {
		called = true
		if request.Action != authz.RetireServiceRevision || request.ResourceType != authz.ServiceRevision || request.ResourceID != revision.Metadata.ID || request.IsolationDomainID != testDomain {
			t.Fatal("retirement authorized the wrong resource")
		}
		return authz.ErrDenied
	}))
	if err != nil {
		t.Fatal(err)
	}
	response := perform(t, denied, http.MethodPost, path, "retire-denied", map[string]any{}, nil)
	if !called || response.Code != http.StatusForbidden {
		t.Fatal("retirement parsed the body before authorization")
	}
}
