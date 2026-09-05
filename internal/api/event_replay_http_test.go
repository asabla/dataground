package api_test

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/api"
)

func TestReferenceBoundedEventReplayPreservesLegacyJournal(t *testing.T) {
	handler := newHandler(t)
	service := createService(t, handler, testDomain, "replay-service")
	revision := createPublishedRevision(t, handler, testDomain, service.Metadata.ID)
	assignAlias(t, handler, testDomain, service.Metadata.ID, revision.Metadata.ID, "replay-route")
	invocation := performJSON[api.Invocation](t, handler, http.MethodPost, "/v1/isolation-domains/"+testDomain+"/agent-services/"+service.Metadata.ID+"/invocations", "replay-invoke", map[string]any{"alias": "stable", "input": map[string]any{"scenario": "success"}}, http.StatusAccepted)
	path := "/v1/isolation-domains/" + testDomain + "/invocations/" + invocation.Metadata.ID + "/events"
	legacy := perform(t, handler, http.MethodGet, path, "", nil, nil)
	if legacy.Code != http.StatusOK || legacy.Header().Get("X-DataGround-Has-More") != "" {
		t.Fatal("unpaged replay changed")
	}
	var joined strings.Builder
	cursor := 0
	for page := 0; page < 10; page++ {
		response := perform(t, handler, http.MethodGet, path+"?limit=2", "", nil, map[string]string{"Last-Event-ID": strconv.Itoa(cursor)})
		if response.Code != http.StatusOK {
			t.Fatalf("page: %d %s", response.Code, response.Body.String())
		}
		text := response.Body.String()
		joined.WriteString(text)
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "id: ") {
				var next int
				if _, err := fmt.Sscanf(line, "id: %d", &next); err != nil || next != cursor+1 {
					t.Fatal("cursor skipped a record")
				}
				cursor = next
			}
		}
		if response.Header().Get("X-DataGround-Has-More") == "false" {
			break
		}
	}
	if joined.String() != legacy.Body.String() || cursor != 8 {
		t.Fatal("paged replay differs from retained journal")
	}
	empty := perform(t, handler, http.MethodGet, path+"?limit=2", "", nil, map[string]string{"Last-Event-ID": "8"})
	if empty.Body.Len() != 0 || empty.Header().Get("X-DataGround-Has-More") != "false" {
		t.Fatal("empty page was not terminal")
	}
}
