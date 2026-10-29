package api_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/domain"
)

func TestServiceSearchPrecedesPaginationAndBindsContinuation(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		t.Run(fmt.Sprint(workspace), func(t *testing.T) {
			h := newHandler(t)
			root := serviceCollectionPath(testDomain)
			if workspace {
				r := workspaceRequest(h, "POST", "/v1/isolation-domains/"+testDomain+"/workspaces", "search-workspace", `{"name":"Research"}`)
				var w domain.Workspace
				if r.Code != 201 || json.Unmarshal(r.Body.Bytes(), &w) != nil {
					t.Fatal(r.Code, r.Body.String())
				}
				root = "/v1/isolation-domains/" + testDomain + "/workspaces/" + w.Metadata.ID + "/agent-services"
			}
			// Put a newer nonmatch ahead of both matches. Filtering a loaded page would miss them.
			for i, body := range []string{`{"name":"Alpha 100%_\\ report","description":"North"}`, `{"name":"Beta","description":"ALPHA 100%_\\ notes"}`, `{"name":"Other"}`} {
				r := workspaceRequest(h, "POST", root, fmt.Sprintf("search-create-%d", i), body)
				if r.Code != 201 {
					t.Fatal(r.Code, r.Body.String())
				}
			}
			type page struct {
				Items      []domain.AgentService `json:"items"`
				NextCursor string                `json:"nextCursor"`
			}
			read := func(query string) page {
				t.Helper()
				r := workspaceRequest(h, "GET", root+"?"+query, "", "")
				var p page
				if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &p) != nil || r.Header().Get("Cache-Control") != "no-store" {
					t.Fatal(r.Code, r.Body.String())
				}
				return p
			}
			q := url.QueryEscape("alpha 100%_\\")
			first := read("q=" + q + "&limit=1")
			if len(first.Items) != 1 || first.Items[0].Name != "Beta" || first.NextCursor == "" {
				t.Fatal("filter must precede pagination", first)
			}
			tail := read("q=" + url.QueryEscape("ALPHA 100%_\\") + "&limit=1&cursor=" + first.NextCursor)
			if len(tail.Items) != 1 || tail.Items[0].Name != "Alpha 100%_\\ report" || tail.NextCursor != "" {
				t.Fatal("continuation changed", tail)
			}
			exact := read("q=" + tail.Items[0].Metadata.ID)
			if len(exact.Items) != 1 {
				t.Fatal("ID search failed", exact)
			}
			if len(read("q=missing").Items) != 0 {
				t.Fatal("missing search matched")
			}
			for _, query := range []string{"q=other&cursor=" + first.NextCursor, "cursor=" + first.NextCursor, "q=a&q=b", "q=%00", "q=%C2%85", "q=%FF", "q=%ZZ", "q=" + strings.Repeat("x", 129), "q=alpha;limit=1"} {
				if r := workspaceRequest(h, "GET", root+"?"+query, "", ""); r.Code != 400 {
					t.Fatalf("invalid search accepted %q: %d", query, r.Code)
				}
			}
		})
	}
}
