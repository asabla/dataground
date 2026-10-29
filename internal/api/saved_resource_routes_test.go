package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
)

func TestSavedResourceReferencePrincipalSeparationReplayAndIndependentReads(t *testing.T) {
	authentication := tokenAuthenticator{principals: map[string]authn.Principal{}}
	for token, input := range map[string]authn.PrincipalInput{
		"saved-human-token-local-only-000001": {ID: "usr_saved_test", Kind: authn.PrincipalHuman},
		"saved-service-token-local-only-0001": {ID: "usr_saved_test", Kind: authn.PrincipalService},
		"saved-other-token-local-only-000001": {ID: "usr_saved_other", Kind: authn.PrincipalHuman},
	} {
		input.Issuer = "fixture"
		input.Subject = input.ID
		input.Audience = authn.APIAudience
		input.IsolationDomains = []string{testDomain, otherTestDomain}
		principal, err := authn.NewPrincipal(input)
		if err != nil {
			t.Fatal(err)
		}
		authentication.principals[token] = principal
	}
	policy, err := authz.NewStaticCedarAuthorizer(authz.StaticCedarConfig{PolicySetID: "saved-resource-test", Schema: authz.CanonicalAPICedarSchema(), Policies: []byte(`permit(principal, action in [DataGround::Action::"createWorkspace",DataGround::Action::"listSavedResources",DataGround::Action::"setSavedResource"], resource);`)})
	if err != nil {
		t.Fatal(err)
	}
	h, err := api.NewHandler(authentication, policy)
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, method, path, key, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	human, service, other := "saved-human-token-local-only-000001", "saved-service-token-local-only-0001", "saved-other-token-local-only-000001"
	root := "/v1/isolation-domains/" + testDomain
	path := root + "/saved-resources"
	created := call(human, "POST", root+"/workspaces", "saved-workspace", `{"name":"Saved research"}`, 201)
	var workspace domain.Workspace
	if json.Unmarshal(created.Body.Bytes(), &workspace) != nil {
		t.Fatal("bad fixture")
	}
	body := `{"kind":"workspace","resourceId":"` + workspace.Metadata.ID + `","saved":true}`
	first := call(human, "POST", path, "saved-first", body, 200)
	if first.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable mutation")
	}
	if repeat := call(human, "POST", path, "saved-first", body, 200); repeat.Body.String() != first.Body.String() {
		t.Fatal("replay changed")
	}
	for _, token := range []string{other, service} {
		read := call(token, "GET", path, "", "", 200)
		if !strings.Contains(read.Body.String(), `"items":[]`) {
			t.Fatal("foreign saved reference leaked")
		}
		call(token, "POST", path, "saved-first", body, 409)
	}
	call(service, "POST", path, "saved-service", body, 200)
	read := call(human, "GET", path, "", "", 200)
	if !strings.Contains(read.Body.String(), "Saved research") || read.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("saved metadata missing")
	}
	call(human, "GET", root+"/workspaces/"+workspace.Metadata.ID, "", "", 403)
	call(human, "GET", root+"/search?q=Saved", "", "", 403)
	foreign := strings.Replace(path, testDomain, otherTestDomain, 1)
	call(human, "POST", foreign, "saved-foreign", body, 404)
	removed := strings.Replace(body, `true`, `false`, 1)
	call(human, "POST", path, "saved-remove", removed, 200)
	call(human, "POST", path, "saved-first", body, 200) // Receipt replay cannot resurrect the reference.
	if read := call(human, "GET", path, "", "", 200); !strings.Contains(read.Body.String(), `"items":[]`) {
		t.Fatal("old replay changed current state")
	}
	if read := call(service, "GET", path, "", "", 200); !strings.Contains(read.Body.String(), "Saved research") {
		t.Fatal("removal crossed principal kinds")
	}
	call(human, "POST", path, "saved-absent-remove", `{"kind":"workspace","resourceId":"wsp_00000000000000000999","saved":false}`, 200)
	call(human, "POST", path, "saved-absent-add", `{"kind":"workspace","resourceId":"wsp_00000000000000000999","saved":true}`, 404)
	call(human, "GET", path+"?principalId=usr_saved_other", "", "", 400)
	call(human, http.MethodPost, path, "saved-malformed", `{"kind":"workspace","resourceId":"`+workspace.Metadata.ID+`","saved":true,"principalId":"usr_other"}`, 400)
}
