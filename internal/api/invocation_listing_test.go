package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
)

func TestInvocationCursorBindsVersionScopeAndStableBoundary(t *testing.T) {
	item := domain.InvocationSummary{
		Metadata:  domain.ResourceMetadata{ID: "inv_00000000000000000001", IsolationDomainID: "iso_00000000000000000001", CreatedAt: time.Date(2026, 8, 1, 12, 0, 0, 123456000, time.UTC)},
		ServiceID: "svc_00000000000000000001",
	}
	response := httptest.NewRecorder()
	writeInvocationPage(response, httptest.NewRequest("GET", "/", nil), []domain.InvocationSummary{item}, true)
	var page invocationPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	query := "limit=1&cursor=" + page.NextCursor
	limit, cursor, err := parseInvocationListQuery(query, item.Metadata.IsolationDomainID, item.ServiceID)
	if err != nil || limit != 1 || cursor.ID != item.Metadata.ID || !cursor.CreatedAt.Equal(item.Metadata.CreatedAt) {
		t.Fatalf("round trip = %d, %#v, %v", limit, cursor, err)
	}
	for name, change := range map[string]func(*invocationListCursor){
		"version":      func(c *invocationListCursor) { c.Version++ },
		"domain":       func(c *invocationListCursor) { c.IsolationDomainID = "iso_00000000000000000002" },
		"service":      func(c *invocationListCursor) { c.ServiceID = "svc_00000000000000000002" },
		"identifier":   func(c *invocationListCursor) { c.ID = "bad" },
		"missing time": func(c *invocationListCursor) { c.CreatedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *cursor
			change(&changed)
			encoded, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := parseInvocationListQuery("cursor="+base64.RawURLEncoding.EncodeToString(encoded), item.Metadata.IsolationDomainID, item.ServiceID); err == nil {
				t.Fatal("invalid cursor accepted")
			}
		})
	}
	for _, raw := range []string{"limit=0", "limit=101", "limit=01", "limit=1&limit=2", "cursor=", "cursor=x&cursor=x", "cursor=" + strings.Repeat("a", 513), "other=1", "limit=%ZZ", "limit=1;other=2"} {
		if _, _, err := parseInvocationListQuery(raw, item.Metadata.IsolationDomainID, item.ServiceID); err == nil {
			t.Fatalf("invalid query accepted: %q", raw)
		}
	}
	encoded, _ := json.Marshal(cursor)
	for _, content := range []string{string(encoded) + "{}", strings.Replace(string(encoded), "{", `{"version":1,`, 1), " " + string(encoded)} {
		if _, _, err := parseInvocationListQuery(url.Values{"cursor": {base64.RawURLEncoding.EncodeToString([]byte(content))}}.Encode(), item.Metadata.IsolationDomainID, item.ServiceID); err == nil {
			t.Fatal("non-canonical cursor accepted")
		}
	}
}
