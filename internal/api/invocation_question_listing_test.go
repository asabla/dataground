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

func TestInvocationQuestionCursorBindsVersionScopeAndStableBoundary(t *testing.T) {
	item := domain.InvocationQuestionSummary{
		ID: "qst_00000000000000000001", IsolationDomainID: "iso_00000000000000000001", CreatedAt: time.Date(2026, 8, 1, 12, 0, 0, 123456000, time.UTC),
		InvocationID: "inv_00000000000000000001",
	}
	response := httptest.NewRecorder()
	writeInvocationQuestionPage(response, httptest.NewRequest("GET", "/", nil), []domain.InvocationQuestionSummary{item}, true)
	var page invocationQuestionPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	query := "limit=1&cursor=" + page.NextCursor
	limit, cursor, err := parseInvocationQuestionListQuery(query, item.IsolationDomainID, item.InvocationID)
	if err != nil || limit != 1 || cursor.ID != item.ID || !cursor.CreatedAt.Equal(item.CreatedAt) {
		t.Fatalf("round trip = %d, %#v, %v", limit, cursor, err)
	}
	for name, change := range map[string]func(*invocationQuestionListCursor){
		"version":      func(c *invocationQuestionListCursor) { c.Version++ },
		"domain":       func(c *invocationQuestionListCursor) { c.IsolationDomainID = "iso_00000000000000000002" },
		"service":      func(c *invocationQuestionListCursor) { c.InvocationID = "inv_00000000000000000002" },
		"identifier":   func(c *invocationQuestionListCursor) { c.ID = "bad" },
		"missing time": func(c *invocationQuestionListCursor) { c.CreatedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := *cursor
			change(&changed)
			encoded, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := parseInvocationQuestionListQuery("cursor="+base64.RawURLEncoding.EncodeToString(encoded), item.IsolationDomainID, item.InvocationID); err == nil {
				t.Fatal("invalid cursor accepted")
			}
		})
	}
	for _, raw := range []string{"limit=0", "limit=101", "limit=01", "limit=1&limit=2", "cursor=", "cursor=x&cursor=x", "cursor=" + strings.Repeat("a", 513), "other=1", "limit=%ZZ", "limit=1;other=2"} {
		if _, _, err := parseInvocationQuestionListQuery(raw, item.IsolationDomainID, item.InvocationID); err == nil {
			t.Fatalf("invalid query accepted: %q", raw)
		}
	}
	encoded, _ := json.Marshal(cursor)
	for _, content := range []string{string(encoded) + "{}", strings.Replace(string(encoded), "{", `{"version":1,`, 1), " " + string(encoded)} {
		if _, _, err := parseInvocationQuestionListQuery(url.Values{"cursor": {base64.RawURLEncoding.EncodeToString([]byte(content))}}.Encode(), item.IsolationDomainID, item.InvocationID); err == nil {
			t.Fatal("non-canonical cursor accepted")
		}
	}
}

func TestReferenceQuestionDiscoveryDoesNotInventDurableQuestions(t *testing.T) {
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
		server.listInvocationQuestions(response, request)
		if response.Code != test.status || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("reference discovery: %d", response.Code)
		}
		if response.Code == 200 && strings.TrimSpace(response.Body.String()) != `{"items":[]}` {
			t.Fatal("reference discovery invented questions")
		}
	}
}

func TestQuestionDiscoveryWithholdsDatabaseFailure(t *testing.T) {
	server := &DurableServer{}
	request := httptest.NewRequest("GET", "/", nil)
	request.SetPathValue("isolationDomainId", "iso_00000000000000000001")
	request.SetPathValue("invocationId", "inv_00000000000000000001")
	response := httptest.NewRecorder()
	request = request.WithContext(context.WithValue(request.Context(), authenticatedCorrelationKey{}, "cor_00000000000000000001"))
	server.listInvocationQuestions(response, request)
	var problem ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if response.Code != 503 || problem.Error.Code != "INVOCATION_QUESTION_LIST_UNAVAILABLE" || !problem.Error.Retryable || problem.Error.CorrelationID != "cor_00000000000000000001" {
		t.Fatalf("unsafe discovery failure: %s", response.Body.String())
	}
}
