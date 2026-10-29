package api_test

import (
	"encoding/json"
	"fmt"
	"github.com/asabla/dataground/internal/domain"
	"net/url"
	"testing"
)

func TestWorkspaceServicesPreserveRelationshipReplayAndScope(t *testing.T) {
	h := newHandler(t)
	root := "/v1/isolation-domains/" + testDomain
	makeWorkspace := func(key string) string {
		t.Helper()
		r := workspaceRequest(h, "POST", root+"/workspaces", key, `{"name":"Research"}`)
		var v domain.Workspace
		if r.Code != 201 || json.Unmarshal(r.Body.Bytes(), &v) != nil {
			t.Fatal(r.Code, r.Body.String())
		}
		return v.Metadata.ID
	}
	first, other := makeWorkspace("first-workspace"), makeWorkspace("other-workspace")
	path := root + "/workspaces/" + first + "/agent-services"
	created := workspaceRequest(h, "POST", path, "service-first", `{"name":"Analysis"}`)
	var service domain.AgentService
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &service) != nil || service.WorkspaceID != first {
		t.Fatal(created.Code, created.Body.String())
	}
	replay := workspaceRequest(h, "POST", path, "service-first", `{"name":"Analysis"}`)
	if replay.Body.String() != created.Body.String() {
		t.Fatal("exact replay changed relationship")
	}
	if r := workspaceRequest(h, "POST", path, "service-first", `{"name":"Changed"}`); r.Code != 409 {
		t.Fatal("conflicting replay accepted", r.Code)
	}
	if r := workspaceRequest(h, "POST", root+"/workspaces/"+other+"/agent-services", "service-first", `{"name":"Analysis"}`); r.Code != 201 {
		t.Fatal("independent workspace request key was rejected", r.Code)
	}
	exact := workspaceRequest(h, "GET", root+"/agent-services/"+service.Metadata.ID, "", "")
	if exact.Code != 200 || exact.Body.String() != created.Body.String() {
		t.Fatal("exact read lost relationship", exact.Code)
	}
	for i := 0; i < 3; i++ {
		if r := workspaceRequest(h, "POST", path, fmt.Sprintf("service-%d", i), `{"name":"Analysis"}`); r.Code != 201 {
			t.Fatal(r.Code)
		}
	}
	unassigned := workspaceRequest(h, "POST", root+"/agent-services", "unassigned", `{"name":"Independent"}`)
	var legacy domain.AgentService
	if unassigned.Code != 201 || json.Unmarshal(unassigned.Body.Bytes(), &legacy) != nil || legacy.WorkspaceID != "" {
		t.Fatal("root creation acquired a workspace")
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		r := workspaceRequest(h, "GET", path+"?limit=2"+cursor, "", "")
		var page struct {
			Items      []domain.AgentService `json:"items"`
			NextCursor string                `json:"nextCursor"`
		}
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &page) != nil || r.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(r.Code, r.Body.String())
		}
		for _, v := range page.Items {
			if seen[v.Metadata.ID] || v.WorkspaceID != first || v.Metadata.ID == legacy.Metadata.ID {
				t.Fatal("page leaked or duplicated a service")
			}
			seen[v.Metadata.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		if r := workspaceRequest(h, "GET", root+"/workspaces/"+other+"/agent-services?cursor="+url.QueryEscape(page.NextCursor), "", ""); r.Code != 400 {
			t.Fatal("foreign workspace cursor accepted")
		}
		cursor = "&cursor=" + url.QueryEscape(page.NextCursor)
	}
	if len(seen) != 4 {
		t.Fatal("missing workspace services", len(seen))
	}
	for _, q := range []string{"limit=0", "limit=101", "limit=01", "limit=1&limit=2", "cursor=", "cursor=invalid", "state=active"} {
		if r := workspaceRequest(h, "GET", path+"?"+q, "", ""); r.Code != 400 {
			t.Fatal("invalid query accepted", q, r.Code)
		}
	}
	for _, method := range []string{"GET", "POST"} {
		r := workspaceRequest(h, method, root+"/workspaces/wsp_00000000000000000999/agent-services", "absent-"+method, `{"name":"Missing"}`)
		if r.Code != 404 {
			t.Fatal("absent workspace accepted", r.Code, r.Body.String())
		}
	}
	if r := workspaceRequest(h, "GET", "/v1/isolation-domains/"+otherTestDomain+"/workspaces/"+first+"/agent-services", "", ""); r.Code != 403 {
		t.Fatal("cross-domain access accepted")
	}
}
