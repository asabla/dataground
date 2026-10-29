package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
)

func TestScopeInvocationCursorBindsStateScopeAndPurpose(t *testing.T) {
	const scope = "iso_00000000000000000001"
	cursor := scopeInvocationCursor{Kind: "scope-invocations", Version: 1, IsolationDomainID: scope, State: "failed", CreatedAt: time.Now().UTC(), ID: "inv_00000000000000000001"}
	encode := func(value scopeInvocationCursor) string {
		data, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(data)
	}
	limit, state, decoded, err := parseScopeInvocationQuery("state=failed&limit=2&cursor="+encode(cursor), scope)
	if err != nil || limit != 2 || state != "failed" || *decoded != cursor {
		t.Fatalf("round trip: %d %s %#v %v", limit, state, decoded, err)
	}
	for _, query := range []string{"state=", "state=future", "state=failed&state=running", "limit=0", "limit=101", "limit=01", "limit=1&limit=2", "cursor=", "other=1", "limit=%ZZ", "state=failed&cursor=" + strings.Repeat("a", 513), "cursor=" + encode(cursor), "state=running&cursor=" + encode(cursor)} {
		if _, _, _, err := parseScopeInvocationQuery(query, scope); err == nil {
			t.Fatalf("invalid query accepted: %s", query)
		}
	}
	for _, mutate := range []func(*scopeInvocationCursor){func(c *scopeInvocationCursor) { c.Kind = "service-invocations" }, func(c *scopeInvocationCursor) { c.Version++ }, func(c *scopeInvocationCursor) { c.IsolationDomainID = "iso_00000000000000000002" }, func(c *scopeInvocationCursor) { c.ID = "bad" }, func(c *scopeInvocationCursor) { c.CreatedAt = time.Time{} }} {
		changed := cursor
		mutate(&changed)
		if _, _, _, err := parseScopeInvocationQuery("state=failed&cursor="+encode(changed), scope); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	canonical, _ := json.Marshal(cursor)
	for _, raw := range []string{" " + string(canonical), string(canonical) + "{}", strings.Replace(string(canonical), "{", `{"version":1,`, 1)} {
		if _, _, _, err := parseScopeInvocationQuery("state=failed&cursor="+base64.RawURLEncoding.EncodeToString([]byte(raw)), scope); err == nil {
			t.Fatal("noncanonical cursor accepted")
		}
	}
}

func TestReferenceScopeInvocationsJoinNamesAndExcludeContent(t *testing.T) {
	server := NewServer()
	const scope = "iso_00000000000000000001"
	const other = "iso_00000000000000000002"
	created := time.Now().UTC()
	for i, id := range []string{"inv_00000000000000000001", "inv_00000000000000000002", "inv_00000000000000000003"} {
		service := "svc_00000000000000000001"
		if i == 1 {
			service = "svc_00000000000000000002"
		}
		server.services[resourceKey(scope, service)] = AgentService{Name: "Analysis " + service}
		invocation := Invocation{Metadata: domain.ResourceMetadata{ID: id, IsolationDomainID: scope, CreatedAt: created}, ServiceID: service, State: "failed", Input: map[string]any{"prompt": "private prompt"}}
		if i == 0 {
			invocation.State = "running"
		}
		server.invocations[resourceKey(scope, id)] = invocation
		invocation.Metadata.IsolationDomainID = other
		server.services[resourceKey(other, service)] = AgentService{Name: "Hidden cross-scope name"}
		server.invocations[resourceKey(other, id)] = invocation
	}
	read := func(query string) scopeInvocationPage {
		t.Helper()
		request := httptest.NewRequest("GET", "/?"+query, nil)
		request.SetPathValue("isolationDomainId", scope)
		response := httptest.NewRecorder()
		server.listScopeInvocations(response, request)
		if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "private prompt") || strings.Contains(response.Body.String(), "Hidden") {
			t.Fatalf("unsafe page: %s", response.Body.String())
		}
		var page scopeInvocationPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := read("state=failed&limit=1")
	if len(first.Items) != 1 || first.Items[0].Metadata.ID != "inv_00000000000000000003" || first.NextCursor == "" {
		t.Fatalf("first page: %#v", first)
	}
	tail := read("state=failed&cursor=" + first.NextCursor)
	if len(tail.Items) != 1 || tail.Items[0].ServiceID == first.Items[0].ServiceID || tail.Items[0].ServiceName != "Analysis "+tail.Items[0].ServiceID || tail.NextCursor != "" {
		t.Fatalf("tail: %#v", tail)
	}
	if len(read("state=waiting").Items) != 0 || len(read("").Items) != 3 {
		t.Fatal("state filter failed")
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.SetPathValue("isolationDomainId", scope)
	response := httptest.NewRecorder()
	(&DurableServer{}).listScopeInvocations(response, request)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "INVOCATION_LIST_UNAVAILABLE") {
		t.Fatalf("database failure: %s", response.Body.String())
	}
}
