package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
)

func workspaceRequest(h http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestWorkspaceReferenceCreationReplayAndScopedDiscovery(t *testing.T) {
	h := newHandler(t)
	path := "/v1/isolation-domains/" + testDomain + "/workspaces"
	first := workspaceRequest(h, "POST", path, "workspace-first", `{"name":" Research ","description":"Project"}`)
	if first.Code != 201 {
		t.Fatal(first.Code, first.Body.String())
	}
	var value domain.Workspace
	_ = json.Unmarshal(first.Body.Bytes(), &value)
	if value.Name != "Research" || value.Metadata.CreatedBy != testActor || value.Metadata.IsolationDomainID != testDomain {
		t.Fatal("workspace attribution changed")
	}
	replay := workspaceRequest(h, "POST", path, "workspace-first", `{"name":" Research ","description":"Project"}`)
	if replay.Body.String() != first.Body.String() {
		t.Fatal("replay changed identity")
	}
	if got := workspaceRequest(h, "POST", path, "workspace-first", `{"name":"changed"}`); got.Code != 409 {
		t.Fatal("conflicting replay accepted", got.Code)
	}
	for i := 0; i < 3; i++ {
		if got := workspaceRequest(h, "POST", path, fmt.Sprintf("workspace-%d", i), `{"name":"Research"}`); got.Code != 201 {
			t.Fatal(got.Code)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		got := workspaceRequest(h, "GET", path+"?limit=2"+cursor, "", "")
		if got.Code != 200 {
			t.Fatal(got.Code, got.Body.String())
		}
		var page struct {
			Items      []domain.Workspace `json:"items"`
			NextCursor string             `json:"nextCursor"`
		}
		_ = json.Unmarshal(got.Body.Bytes(), &page)
		for _, item := range page.Items {
			if seen[item.Metadata.ID] {
				t.Fatal("duplicate page item")
			}
			seen[item.Metadata.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = "&cursor=" + url.QueryEscape(page.NextCursor)
	}
	if len(seen) != 4 {
		t.Fatal("discovery lost entries")
	}
	got := workspaceRequest(h, "GET", path+"/"+value.Metadata.ID, "", "")
	if got.Code != 200 || got.Body.String() != first.Body.String() {
		t.Fatal("exact read changed")
	}
	if got := workspaceRequest(h, "GET", "/v1/isolation-domains/"+otherTestDomain+"/workspaces/"+value.Metadata.ID, "", ""); got.Code != 403 {
		t.Fatal("foreign scope accepted")
	}
	if got := workspaceRequest(h, "GET", path+"/wsp_00000000000000000999", "", ""); got.Code != 404 {
		t.Fatal("missing identity not reported")
	}
	for i, body := range []string{`{"Name":"x"}`, `{"name":"x","Description":null}`, `{"name":"x","description":null}`, `{"name":""}`, `{"name":"x","description":"\u0000"}`, `{"name":"x","path":"/private"}`, `{"name":"` + strings.Repeat("界", 43) + `"}`, `{"name":"x","description":"` + strings.Repeat("x", 2049) + `"}`, "{\"name\":\"" + string([]byte{0xff}) + "\"}"} {
		if got := workspaceRequest(h, "POST", path, fmt.Sprintf("workspace-invalid-%d", i), body); got.Code != 400 {
			t.Fatal("invalid metadata accepted", i, got.Code)
		}
	}
	for _, query := range []string{"limit=0", "limit=101", "limit=01", "limit=1&limit=2", "cursor=", "cursor=invalid", "unexpected=1"} {
		if got := workspaceRequest(h, "GET", path+"?"+query, "", ""); got.Code != 400 {
			t.Fatal("invalid query accepted", query, got.Code)
		}
	}
}
func TestWorkspaceRoutesBindIndependentActionsBeforeBodyRead(t *testing.T) {
	authentication, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/isolation-domains/" + testDomain + "/workspaces"
	for _, tc := range []struct {
		method, path string
		action       authz.Action
		kind         authz.ResourceType
		id           string
	}{{"POST", path, authz.CreateWorkspace, authz.IsolationDomain, testDomain}, {"GET", path, authz.ListWorkspaces, authz.IsolationDomain, testDomain}, {"GET", path + "/wsp_00000000000000000001", authz.ReadWorkspace, authz.Workspace, "wsp_00000000000000000001"}} {
		calls := 0
		h, err := api.NewHandler(authentication, authorizerFunc(func(_ context.Context, r authz.Request) error {
			calls++
			if r.Action != tc.action || r.ResourceType != tc.kind || r.ResourceID != tc.id || r.IsolationDomainID != testDomain {
				t.Error("authorization target changed")
			}
			return authz.ErrDenied
		}))
		if err != nil {
			t.Fatal(err)
		}
		reader := &countingReader{}
		r := httptest.NewRequest(tc.method, tc.path, reader)
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 || calls != 1 || reader.reads != 0 {
			t.Fatal("denial boundary changed", w.Code)
		}
	}
}
