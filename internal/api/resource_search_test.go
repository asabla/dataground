package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
)

func TestResourceSearchCursorBindsQueryKindScopeAndPurpose(t *testing.T) {
	const scope = "iso_00000000000000000001"
	cursor := resourceSearchCursor{Kind: "resource-search", Version: 1, IsolationDomainID: scope, Search: discoverySearchDigest("Research"), ResourceKind: "workspace", CreatedAt: time.Now().UTC(), ID: "wsp_00000000000000000001"}
	encode := func(c resourceSearchCursor) string {
		b, _ := json.Marshal(c)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	query := "q=RESEARCH&kind=workspace&limit=2&cursor=" + encode(cursor)
	limit, q, kind, decoded, err := parseResourceSearch(query, scope)
	if err != nil || limit != 2 || q != "RESEARCH" || kind != "workspace" || *decoded != cursor {
		t.Fatalf("round trip: %d %s %s %#v %v", limit, q, kind, decoded, err)
	}
	for _, raw := range []string{"", "q=", "q=x&q=y", "q=%ZZ", "q=%FF", "q=a%00b", "q=" + strings.Repeat("é", 129), "q=x&kind=", "q=x&kind=future", "q=x&kind=artifact&kind=workspace", "q=x&limit=0", "q=x&limit=101", "q=x&limit=01", "q=x&limit=1&limit=2", "q=x&cursor=", "q=x&cursor=" + strings.Repeat("a", 513), "q=x&other=y", "q=Research&cursor=" + encode(cursor), "q=Research&kind=artifact&cursor=" + encode(cursor), "q=other&kind=workspace&cursor=" + encode(cursor)} {
		if _, _, _, _, err := parseResourceSearch(raw, scope); err == nil {
			t.Fatalf("accepted invalid query: %q", raw)
		}
	}
	for _, mutate := range []func(*resourceSearchCursor){func(c *resourceSearchCursor) { c.Kind = "scope-artifacts" }, func(c *resourceSearchCursor) { c.Version++ }, func(c *resourceSearchCursor) { c.IsolationDomainID = "iso_00000000000000000002" }, func(c *resourceSearchCursor) { c.ID = "bad" }, func(c *resourceSearchCursor) { c.CreatedAt = time.Time{} }} {
		changed := cursor
		mutate(&changed)
		if _, _, _, _, err := parseResourceSearch("q=Research&kind=workspace&cursor="+encode(changed), scope); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	canonical, _ := json.Marshal(cursor)
	for _, raw := range []string{" " + string(canonical), string(canonical) + "{}", strings.Replace(string(canonical), "{", `{"version":1,`, 1)} {
		if _, _, _, _, err := parseResourceSearch("q=Research&kind=workspace&cursor="+base64.RawURLEncoding.EncodeToString([]byte(raw)), scope); err == nil {
			t.Fatal("noncanonical cursor accepted")
		}
	}
	if _, _, _, _, err := parseResourceSearch("q="+url.QueryEscape(strings.Repeat("é", 128)), scope); err != nil {
		t.Fatal("Unicode limit rejected", err)
	}
}

func TestReferenceResourceSearchFiltersBeforePaginationAndExcludesContent(t *testing.T) {
	server := NewServer()
	const scope = "iso_00000000000000000001"
	const other = "iso_00000000000000000002"
	const service = "svc_00000000000000000001"
	const invocation = "inv_00000000000000000001"
	const workspace = "wsp_00000000000000000001"
	const artifact = "art_00000000000000000001"
	created := time.Now().UTC()
	metadata := func(scope, id string) domain.ResourceMetadata {
		return domain.ResourceMetadata{ID: id, IsolationDomainID: scope, CreatedAt: created, CreatedBy: "private actor"}
	}
	for _, target := range []string{scope, other} {
		name := "Quarterly research"
		if target == other {
			name = "Foreign research"
		}
		server.workspaces[resourceKey(target, workspace)] = domain.Workspace{Metadata: metadata(target, workspace), Name: name, Description: "Budget_%"}
		server.services[resourceKey(target, service)] = AgentService{Metadata: metadata(target, service), Name: name, WorkspaceID: workspace}
		server.invocations[resourceKey(target, invocation)] = Invocation{Metadata: metadata(target, invocation), ServiceID: service, State: "waiting", Input: map[string]any{"prompt": "private prompt"}, Result: map[string]any{"message": "private output"}}
		server.artifacts[resourceKey(target, artifact)] = domain.ArtifactDescriptor{Metadata: metadata(target, artifact), Name: name + ".txt", InvocationID: invocation, State: "available", Sensitive: true}
	}
	// Newer nonmatches must not consume the requested result limit.
	for i := 2; i < 15; i++ {
		id := fmt.Sprintf("svc_%020d", i)
		m := metadata(scope, id)
		m.CreatedAt = created.Add(time.Hour)
		server.services[resourceKey(scope, id)] = AgentService{Metadata: m, Name: "Other"}
	}
	read := func(query string) resourceSearchPage {
		t.Helper()
		request := httptest.NewRequest("GET", "/?"+query, nil)
		request.SetPathValue("isolationDomainId", scope)
		response := httptest.NewRecorder()
		server.searchResources(response, request)
		if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("search: %d %s", response.Code, response.Body.String())
		}
		for _, secret := range []string{"Foreign", "private prompt", "private output", "private actor"} {
			if strings.Contains(response.Body.String(), secret) {
				t.Fatal("search leaked", secret)
			}
		}
		var page resourceSearchPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	page := read("q=RESEARCH&limit=1")
	var items []domain.ResourceSearchResult
	for {
		items = append(items, page.Items...)
		if page.NextCursor == "" {
			break
		}
		if len(items) > 4 {
			t.Fatal("pagination did not advance")
		}
		page = read("q=research&limit=1&cursor=" + page.NextCursor)
	}
	expected := []string{workspace, service, invocation, artifact}
	if len(items) != 4 {
		t.Fatalf("result count: %d", len(items))
	}
	for i, item := range items {
		if item.Metadata.ID != expected[i] || item.Metadata.IsolationDomainID != scope {
			t.Fatalf("unexpected result: %#v", item)
		}
	}
	if items[2].ServiceID != service || items[2].State != "waiting" || items[3].InvocationID != invocation || items[3].ServiceID != service {
		t.Fatal("missing navigation metadata")
	}
	for _, kind := range []string{"workspace", "agent-service", "invocation", "artifact"} {
		result := read("q=research&kind=" + kind)
		if len(result.Items) != 1 || result.Items[0].Kind != kind {
			t.Fatal("kind filter failed", kind)
		}
	}
	if len(read("q=Budget_%25").Items) != 1 || len(read("q=absent_%25").Items) != 0 || len(read("q="+artifact).Items) != 1 {
		t.Fatal("literal or ID search failed")
	}
	request := httptest.NewRequest("GET", "/?q=research", nil)
	request.SetPathValue("isolationDomainId", scope)
	response := httptest.NewRecorder()
	(&DurableServer{}).searchResources(response, request)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "RESOURCE_SEARCH_UNAVAILABLE") || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unsafe unavailable response", response.Body.String())
	}
}
