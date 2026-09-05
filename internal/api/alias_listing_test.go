package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/domain"
)

func TestAliasCursorBindsVersionScopeAndStableBoundary(t *testing.T) {
	item := domain.ServiceAlias{
		Metadata:  domain.ResourceMetadata{ID: "als_00000000000000000001", IsolationDomainID: "iso_00000000000000000001"},
		ServiceID: "svc_00000000000000000001",
		Name:      "stable",
	}
	response := httptest.NewRecorder()
	writeAliasPage(response, httptest.NewRequest("GET", "/", nil), []domain.ServiceAlias{item}, true)
	var page serviceAliasPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	query := "limit=1&cursor=" + page.NextCursor
	limit, cursor, err := parseAliasListQuery(query, item.Metadata.IsolationDomainID, item.ServiceID)
	if err != nil || limit != 1 || cursor.Name != item.Name {
		t.Fatalf("round trip = %d, %#v, %v", limit, cursor, err)
	}
	for name, change := range map[string]func(*aliasListCursor){
		"version":      func(c *aliasListCursor) { c.Version++ },
		"domain":       func(c *aliasListCursor) { c.IsolationDomainID = "iso_00000000000000000002" },
		"service":      func(c *aliasListCursor) { c.ServiceID = "svc_00000000000000000002" },
		"name":         func(c *aliasListCursor) { c.Name = "Bad" },
		"missing name": func(c *aliasListCursor) { c.Name = "" },
		"long name":    func(c *aliasListCursor) { c.Name = strings.Repeat("a", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *cursor
			change(&changed)
			encoded, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := parseAliasListQuery("cursor="+base64.RawURLEncoding.EncodeToString(encoded), item.Metadata.IsolationDomainID, item.ServiceID); err == nil {
				t.Fatal("invalid cursor accepted")
			}
		})
	}
	for _, raw := range []string{"limit=0", "limit=101", "limit=01", "limit=1&limit=2", "cursor=", "cursor=x&cursor=x", "cursor=" + strings.Repeat("a", 513), "other=1", "limit=%ZZ", "limit=1;other=2"} {
		if _, _, err := parseAliasListQuery(raw, item.Metadata.IsolationDomainID, item.ServiceID); err == nil {
			t.Fatalf("invalid query accepted: %q", raw)
		}
	}
	encoded, _ := json.Marshal(cursor)
	for _, content := range []string{string(encoded) + "{}", strings.Replace(string(encoded), "{", `{"version":1,`, 1), " " + string(encoded)} {
		if _, _, err := parseAliasListQuery(url.Values{"cursor": {base64.RawURLEncoding.EncodeToString([]byte(content))}}.Encode(), item.Metadata.IsolationDomainID, item.ServiceID); err == nil {
			t.Fatal("non-canonical cursor accepted")
		}
	}
}
