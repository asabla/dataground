package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
)

func TestInvocationArtifactCursorBindsVersionScopeAndStableBoundary(t *testing.T) {
	item := domain.ArtifactDescriptor{
		Metadata:     domain.ResourceMetadata{ID: "art_00000000000000000001", IsolationDomainID: "iso_00000000000000000001", CreatedAt: time.Date(2026, 8, 1, 12, 0, 0, 123456000, time.UTC)},
		InvocationID: "inv_00000000000000000001",
	}
	response := httptest.NewRecorder()
	writeInvocationArtifactPage(response, httptest.NewRequest("GET", "/", nil), []domain.ArtifactDescriptor{item}, true)
	var page invocationArtifactPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	query := "limit=1&cursor=" + page.NextCursor
	limit, cursor, err := parseInvocationArtifactListQuery(query, item.Metadata.IsolationDomainID, item.InvocationID)
	if err != nil || limit != 1 || cursor.ID != item.Metadata.ID || !cursor.CreatedAt.Equal(item.Metadata.CreatedAt) {
		t.Fatalf("round trip = %d, %#v, %v", limit, cursor, err)
	}
	for name, change := range map[string]func(*invocationArtifactListCursor){
		"version":      func(c *invocationArtifactListCursor) { c.Version++ },
		"domain":       func(c *invocationArtifactListCursor) { c.IsolationDomainID = "iso_00000000000000000002" },
		"service":      func(c *invocationArtifactListCursor) { c.InvocationID = "inv_00000000000000000002" },
		"identifier":   func(c *invocationArtifactListCursor) { c.ID = "bad" },
		"missing time": func(c *invocationArtifactListCursor) { c.CreatedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *cursor
			change(&changed)
			encoded, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := parseInvocationArtifactListQuery("cursor="+base64.RawURLEncoding.EncodeToString(encoded), item.Metadata.IsolationDomainID, item.InvocationID); err == nil {
				t.Fatal("invalid cursor accepted")
			}
		})
	}
	for _, raw := range []string{"limit=0", "limit=101", "limit=01", "limit=1&limit=2", "cursor=", "cursor=x&cursor=x", "cursor=" + strings.Repeat("a", 513), "other=1", "limit=%ZZ", "limit=1;other=2"} {
		if _, _, err := parseInvocationArtifactListQuery(raw, item.Metadata.IsolationDomainID, item.InvocationID); err == nil {
			t.Fatalf("invalid query accepted: %q", raw)
		}
	}
	encoded, _ := json.Marshal(cursor)
	for _, content := range []string{string(encoded) + "{}", strings.Replace(string(encoded), "{", `{"version":1,`, 1), " " + string(encoded)} {
		if _, _, err := parseInvocationArtifactListQuery(url.Values{"cursor": {base64.RawURLEncoding.EncodeToString([]byte(content))}}.Encode(), item.Metadata.IsolationDomainID, item.InvocationID); err == nil {
			t.Fatal("non-canonical cursor accepted")
		}
	}
}

func TestReferenceArtifactDiscoveryDoesNotInventDurableArtifacts(t *testing.T) {
	server := NewServer()
	const domainID = "iso_00000000000000000001"
	const invocationID = "inv_00000000000000000001"
	server.invocations[resourceKey(domainID, invocationID)] = Invocation{}
	for _, test := range []struct {
		domain, invocation, query string
		status                    int
	}{
		{domainID, invocationID, "", 200},
		{"iso_00000000000000000002", invocationID, "", 404},
		{domainID, "inv_00000000000000000002", "", 404},
		{domainID, invocationID, "?limit=0", 400},
	} {
		request := httptest.NewRequest("GET", "/"+test.query, nil)
		request.SetPathValue("isolationDomainId", test.domain)
		request.SetPathValue("invocationId", test.invocation)
		response := httptest.NewRecorder()
		server.listInvocationArtifacts(response, request)
		if response.Code != test.status || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("reference discovery: %d", response.Code)
		}
		if response.Code == 200 && strings.TrimSpace(response.Body.String()) != `{"items":[]}` {
			t.Fatal("reference discovery invented artifacts")
		}
	}
}

func TestArtifactDiscoveryWithholdsDatabaseFailure(t *testing.T) {
	server := &DurableServer{}
	request := httptest.NewRequest("GET", "/", nil)
	request.SetPathValue("isolationDomainId", "iso_00000000000000000001")
	request.SetPathValue("invocationId", "inv_00000000000000000001")
	response := httptest.NewRecorder()
	request = request.WithContext(context.WithValue(request.Context(), authenticatedCorrelationKey{}, "cor_00000000000000000001"))
	server.listInvocationArtifacts(response, request)
	var problem ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if response.Code != 503 || problem.Error.Code != "INVOCATION_ARTIFACT_LIST_UNAVAILABLE" || !problem.Error.Retryable || problem.Error.CorrelationID != "cor_00000000000000000001" {
		t.Fatalf("unsafe discovery failure: %s", response.Body.String())
	}
}

func TestReferenceArtifactDiscoveryPagesMetadataWithinInvocation(t *testing.T) {
	server := NewServer()
	const scope = "iso_00000000000000000001"
	const invocation = "inv_00000000000000000001"
	server.invocations[resourceKey(scope, invocation)] = Invocation{}
	now := time.Now().UTC()
	for _, id := range []string{"art_00000000000000000001", "art_00000000000000000002", "art_00000000000000000003"} {
		item := ArtifactDescriptor{Metadata: domain.ResourceMetadata{ID: id, IsolationDomainID: scope, CreatedAt: now}, InvocationID: invocation, Name: "Visible report.txt", Sensitive: true}
		server.artifacts[artifactKey(scope, invocation, id)] = item
		other := item
		other.Metadata.IsolationDomainID = "iso_00000000000000000002"
		other.Name = "Hidden cross-scope report"
		server.artifacts[artifactKey(other.Metadata.IsolationDomainID, invocation, id)] = other
		other = item
		other.InvocationID = "inv_00000000000000000002"
		other.Name = "Hidden cross-run report"
		server.artifacts[artifactKey(scope, other.InvocationID, id)] = other
	}
	read := func(query string) invocationArtifactPage {
		t.Helper()
		request := httptest.NewRequest("GET", "/?"+query, nil)
		request.SetPathValue("isolationDomainId", scope)
		request.SetPathValue("invocationId", invocation)
		response := httptest.NewRecorder()
		server.listInvocationArtifacts(response, request)
		if response.Code != 200 || strings.Contains(response.Body.String(), "Hidden") {
			t.Fatalf("unsafe list: %s", response.Body.String())
		}
		var page invocationArtifactPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := read("limit=1")
	if len(first.Items) != 1 || first.Items[0].Metadata.ID != "art_00000000000000000003" || !first.Items[0].Sensitive || first.NextCursor == "" {
		t.Fatalf("first page: %#v", first)
	}
	tail := read("limit=2&cursor=" + first.NextCursor)
	if len(tail.Items) != 2 || tail.Items[0].Metadata.ID != "art_00000000000000000002" || tail.Items[1].Metadata.ID != "art_00000000000000000001" || tail.NextCursor != "" {
		t.Fatalf("continuation: %#v", tail)
	}
}
