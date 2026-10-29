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

func TestScopeArtifactCursorBindsStateScopeAndPurpose(t *testing.T) {
	const scope = "iso_00000000000000000001"
	cursor := scopeArtifactCursor{Kind: "scope-artifacts", Version: 1, IsolationDomainID: scope, State: "failed", CreatedAt: time.Now().UTC(), ID: "art_00000000000000000001"}
	encode := func(value scopeArtifactCursor) string {
		data, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(data)
	}
	limit, state, decoded, err := parseScopeArtifactQuery("state=failed&limit=2&cursor="+encode(cursor), scope)
	if err != nil || limit != 2 || state != "failed" || *decoded != cursor {
		t.Fatalf("round trip: %d %s %#v %v", limit, state, decoded, err)
	}
	for _, query := range []string{"state=", "state=future", "state=failed&state=pending", "limit=0", "limit=101", "limit=01", "limit=1&limit=2", "cursor=", "other=1", "limit=%ZZ", "state=failed&cursor=" + strings.Repeat("a", 513), "cursor=" + encode(cursor), "state=pending&cursor=" + encode(cursor)} {
		if _, _, _, err := parseScopeArtifactQuery(query, scope); err == nil {
			t.Fatalf("invalid query accepted: %s", query)
		}
	}
	for _, mutate := range []func(*scopeArtifactCursor){func(c *scopeArtifactCursor) { c.Kind = "invocation-artifacts" }, func(c *scopeArtifactCursor) { c.Version++ }, func(c *scopeArtifactCursor) { c.IsolationDomainID = "iso_00000000000000000002" }, func(c *scopeArtifactCursor) { c.ID = "bad" }, func(c *scopeArtifactCursor) { c.CreatedAt = time.Time{} }} {
		changed := cursor
		mutate(&changed)
		if _, _, _, err := parseScopeArtifactQuery("state=failed&cursor="+encode(changed), scope); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	canonical, _ := json.Marshal(cursor)
	for _, raw := range []string{" " + string(canonical), string(canonical) + "{}", strings.Replace(string(canonical), "{", `{"version":1,`, 1)} {
		if _, _, _, err := parseScopeArtifactQuery("state=failed&cursor="+base64.RawURLEncoding.EncodeToString([]byte(raw)), scope); err == nil {
			t.Fatal("noncanonical cursor accepted")
		}
	}
}

func TestReferenceScopeArtifactsJoinExactSources(t *testing.T) {
	server := NewServer()
	const scope = "iso_00000000000000000001"
	const other = "iso_00000000000000000002"
	created := time.Now().UTC()
	for i, id := range []string{"art_00000000000000000001", "art_00000000000000000002", "art_00000000000000000003"} {
		service := "svc_00000000000000000001"
		invocation := "inv_00000000000000000001"
		if i == 1 {
			service = "svc_00000000000000000002"
			invocation = "inv_00000000000000000002"
		}
		server.services[resourceKey(scope, service)] = AgentService{Name: "Analysis " + service}
		server.invocations[resourceKey(scope, invocation)] = Invocation{ServiceID: service, RevisionID: "rev_00000000000000000001", Input: map[string]any{"prompt": "private prompt"}}
		item := domain.ArtifactDescriptor{Metadata: domain.ResourceMetadata{ID: id, IsolationDomainID: scope, CreatedAt: created}, InvocationID: invocation, Name: "Output.txt", State: "available", Sensitive: true}
		if i == 0 {
			item.State = "deleted"
		}
		server.artifacts[resourceKey(scope, id)] = item
		item.Metadata.IsolationDomainID = other
		item.Name = "Hidden other scope"
		server.artifacts[resourceKey(other, id)] = item
	}
	read := func(query string) scopeArtifactPage {
		t.Helper()
		request := httptest.NewRequest("GET", "/?"+query, nil)
		request.SetPathValue("isolationDomainId", scope)
		response := httptest.NewRecorder()
		server.listScopeArtifacts(response, request)
		if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "private prompt") || strings.Contains(response.Body.String(), "Hidden") {
			t.Fatalf("unsafe artifact page: %s", response.Body.String())
		}
		var page scopeArtifactPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := read("state=available&limit=1")
	if len(first.Items) != 1 || first.Items[0].Metadata.ID != "art_00000000000000000003" || first.NextCursor == "" {
		t.Fatalf("first page: %#v", first)
	}
	tail := read("state=available&cursor=" + first.NextCursor)
	if len(tail.Items) != 1 || tail.NextCursor != "" || tail.Items[0].ServiceID == first.Items[0].ServiceID || tail.Items[0].InvocationID == first.Items[0].InvocationID || tail.Items[0].ServiceName != "Analysis "+tail.Items[0].ServiceID {
		t.Fatalf("source navigation: %#v", tail)
	}
	if len(read("state=pending").Items) != 0 || len(read("").Items) != 3 {
		t.Fatal("artifact filter failed")
	}
}
func TestDurableScopeArtifactFailureIsSafe(t *testing.T) {
	request := httptest.NewRequest("GET", "/", nil)
	request.SetPathValue("isolationDomainId", "iso_00000000000000000001")
	response := httptest.NewRecorder()
	(&DurableServer{}).listScopeArtifacts(response, request)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "SCOPE_ARTIFACT_LIST_UNAVAILABLE") {
		t.Fatalf("unsafe database failure: %s", response.Body.String())
	}
}
