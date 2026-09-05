package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
)

func TestAliasWithdrawalStopsNewRoutingAndAllowsLastRevisionRetirement(t *testing.T) {
	handler := newHandler(t)
	service := createService(t, handler, testDomain, "withdraw-service")
	revision := createPublishedRevision(t, handler, testDomain, service.Metadata.ID)
	original := assignAlias(t, handler, testDomain, service.Metadata.ID, revision.Metadata.ID, "withdraw-initial")
	accepted := invoke(t, handler, testDomain, service.Metadata.ID, "question", "withdraw-invocation")
	aliasPath := serviceAliasPath(testDomain, service.Metadata.ID, "stable")
	path := aliasPath + "/actions/withdraw"
	body := map[string]any{"expectedVersion": original.Metadata.Version}
	withdrawn := performJSON[api.ServiceAlias](t, handler, http.MethodPost, path, "withdraw-first", body, http.StatusOK)
	if withdrawn.WithdrawnAt == nil || withdrawn.Metadata.ID != original.Metadata.ID || withdrawn.Metadata.Version != 2 {
		t.Fatal("withdrawal lost alias history")
	}
	if response := perform(t, handler, http.MethodGet, aliasPath, "", nil, nil); response.Code != http.StatusNotFound {
		t.Fatal("withdrawn alias remained discoverable")
	}
	response := perform(t, handler, http.MethodPost, "/v1/isolation-domains/"+testDomain+"/agent-services/"+service.Metadata.ID+"/invocations", "withdraw-new-invoke", map[string]any{"alias": "stable", "input": map[string]any{"prompt": "question"}}, nil)
	if response.Code != http.StatusNotFound {
		t.Fatal("withdrawn alias accepted a new invocation")
	}
	replayed := invoke(t, handler, testDomain, service.Metadata.ID, "question", "withdraw-invocation")
	if replayed.Metadata.ID != accepted.Metadata.ID {
		t.Fatal("withdrawal changed accepted invocation replay")
	}
	recreated := performJSON[api.ServiceAlias](t, handler, http.MethodPut, aliasPath, "withdraw-recreate", map[string]any{"revisionId": revision.Metadata.ID, "expectedVersion": 0}, http.StatusOK)
	if recreated.WithdrawnAt != nil || recreated.Metadata.Version != 3 || recreated.Metadata.ID != original.Metadata.ID {
		t.Fatal("alias reuse reset identity or version")
	}
	response = perform(t, handler, http.MethodPost, path, "withdraw-stale", body, nil)
	if response.Code != http.StatusConflict {
		t.Fatal("stale version withdrew a reused alias")
	}
	replay := performJSON[api.ServiceAlias](t, handler, http.MethodPost, path, "withdraw-first", body, http.StatusOK)
	if replay.WithdrawnAt == nil || replay.Metadata.Version != 2 {
		t.Fatal("withdrawal replay changed")
	}
	active := performJSON[api.ServiceAlias](t, handler, http.MethodGet, aliasPath, "", nil, http.StatusOK)
	if active.Metadata.Version != 3 {
		t.Fatal("historical replay mutated routing")
	}
	performJSON[api.ServiceAlias](t, handler, http.MethodPost, path, "withdraw-final", map[string]any{"expectedVersion": 3}, http.StatusOK)
	retirePath := "/v1/isolation-domains/" + testDomain + "/service-revisions/" + revision.Metadata.ID + "/actions/retire"
	response = perform(t, handler, http.MethodPost, retirePath, "withdraw-active-retire", map[string]any{"expectedVersion": revision.Metadata.Version}, nil)
	var problem api.ErrorEnvelope
	decodeResponse(t, response, &problem)
	if problem.Error.Code != "REVISION_STILL_ACTIVE" {
		t.Fatalf("active invocation was not preserved: %+v", problem)
	}
	performJSON[api.Invocation](t, handler, http.MethodPost, "/v1/isolation-domains/"+testDomain+"/invocations/"+accepted.Metadata.ID+"/actions/cancel", "withdraw-cancel", map[string]any{}, http.StatusOK)
	retired := performJSON[api.ServiceRevision](t, handler, http.MethodPost, retirePath, "withdraw-retire-last", map[string]any{"expectedVersion": revision.Metadata.Version}, http.StatusOK)
	if retired.State != "retired" {
		t.Fatal("last revision could not retire")
	}
}

func TestAliasWithdrawalAuthorizesBeforeParsingAndValidatesPreconditions(t *testing.T) {
	handler := newHandler(t)
	path := serviceAliasPath(testDomain, "svc_00000000000000000001", "stable") + "/actions/withdraw"
	for index, body := range []map[string]any{{}, {"expectedVersion": 0}, {"expectedVersion": -1}, {"expectedVersion": 1, "force": true}} {
		response := perform(t, handler, http.MethodPost, path, string(rune('a'+index))+"-withdraw-invalid", body, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid withdrawal=%d", response.Code)
		}
	}
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	denied, err := api.NewHandler(authenticator, authorizerFunc(func(_ context.Context, request authz.Request) error {
		called = true
		if request.Action != authz.WithdrawServiceAlias || request.ResourceType != authz.AgentService || request.ResourceID != "svc_00000000000000000001" || request.IsolationDomainID != testDomain {
			t.Fatal("withdrawal authorized the wrong scope")
		}
		return authz.ErrDenied
	}))
	if err != nil {
		t.Fatal(err)
	}
	response := perform(t, denied, http.MethodPost, path, "withdraw-denied", map[string]any{}, nil)
	if !called || response.Code != http.StatusForbidden {
		t.Fatal("withdrawal parsed before authorization")
	}
}
