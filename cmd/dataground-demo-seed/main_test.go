package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
)

type demoIdentities map[string]*authn.DevelopmentAuthenticator

func (identities demoIdentities) Authenticate(ctx context.Context, token []byte) (authn.Principal, error) {
	for _, authenticator := range identities {
		if principal, err := authenticator.Authenticate(ctx, token); err == nil {
			return principal, nil
		}
	}
	return authn.Principal{}, authn.ErrInvalidCredential
}

func TestSeedReferenceAPIReplaysAndIsolatesDemoData(t *testing.T) {
	const firstDomain = "iso_00000000000000000001"
	const secondDomain = "iso_00000000000000000002"
	const firstController = "usr_00000000000000000001"
	const secondController = "usr_00000000000000000002"
	const observer = "usr_00000000000000000003"
	firstToken, secondToken, observerToken := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	identities := demoIdentities{}
	for _, identity := range []struct{ id, scope, token string }{
		{firstController, firstDomain, firstToken},
		{secondController, secondDomain, secondToken},
		{observer, firstDomain, observerToken},
	} {
		authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{
			BearerToken: []byte(identity.token), PrincipalID: identity.id, IsolationDomainID: identity.scope,
		})
		if err != nil {
			t.Fatal(err)
		}
		identities[identity.id] = authenticator
	}
	authorizer, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{
		PolicySetID: "reference-demo-test",
		Schema:      authz.CanonicalAPICedarSchema(),
		Policies: []byte(fmt.Sprintf(`
permit(principal == DataGround::User::"%s", action, resource)
when { context.isolationDomainID == "%s" };
permit(principal == DataGround::User::"%s", action, resource)
when { context.isolationDomainID == "%s" };
permit(principal == DataGround::User::"%s", action == DataGround::Action::"readInvocation", resource)
when { context.isolationDomainID == "%s" };
`, firstController, firstDomain, secondController, secondDomain, observer, firstDomain)),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewHandler(identities, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	clients := make([]demoAPI, 0, 3)
	for _, binding := range []struct{ scope, token string }{
		{firstDomain, firstToken}, {secondDomain, secondToken}, {firstDomain, observerToken},
	} {
		client, err := newDemoAPI(server.URL, binding.scope, binding.token)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, client)
	}
	ctx := t.Context()
	first, err := seed(ctx, clients[0])
	if err != nil {
		t.Fatal(err)
	}
	replay, err := seed(ctx, clients[0])
	if err != nil || !reflect.DeepEqual(first, replay) {
		t.Fatalf("seed replay changed identities or outcomes: %v", err)
	}
	second, err := seed(ctx, clients[1])
	if err != nil || second.WorkspaceID == first.WorkspaceID || second.ServiceID == first.ServiceID {
		t.Fatalf("second domain did not get an independent workspace and service: %v", err)
	}
	runs, err := request[struct {
		Items []domain.InvocationSummary `json:"items"`
	}](ctx, clients[0], http.MethodGet, "/agent-services/"+first.ServiceID+"/invocations", "", nil)
	if err != nil || len(runs.Items) != 4 {
		t.Fatalf("seed did not produce exactly four visible runs: %v", err)
	}
	artifactRun := first.Runs[3]
	invocationPath := "/invocations/" + artifactRun.ID
	artifact, err := request[domain.ArtifactDescriptor](ctx, clients[0], http.MethodGet,
		invocationPath+"/artifacts/"+artifactRun.ArtifactIDs[0], "", nil)
	if err != nil || artifact.State != "available" || artifact.InvocationID != artifactRun.ID {
		t.Fatalf("demo artifact is unavailable: %v", err)
	}
	if _, err := request[domain.Invocation](ctx, clients[2], http.MethodGet, invocationPath, "", nil); err != nil {
		t.Fatalf("observer could not inspect the run: %v", err)
	}
	if _, err := seed(ctx, clients[2]); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("observer could seed data: %v", err)
	}
	foreign := clients[0]
	foreign.scope = clients[1].scope
	if _, err := request[domain.Workspace](ctx, foreign, http.MethodGet, "/workspaces/"+second.WorkspaceID, "", nil); err == nil ||
		!strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("first controller could read the other domain: %v", err)
	}
}

func TestSeedRejectsRemoteOriginAndRedirects(t *testing.T) {
	const domain = "iso_00000000000000000001"
	token := strings.Repeat("a", 32)
	if _, err := newDemoAPI("http://example.test", domain, token); err == nil {
		t.Fatal("remote origin accepted")
	}
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the demo client followed a redirect")
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client, err := newDemoAPI(redirect.URL, domain, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed(t.Context(), client); err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("redirect was not rejected: %v", err)
	}
}
