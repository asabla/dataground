package api

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEventReplayPageBoundsAndFailureAtomicity(t *testing.T) {
	events := make([]EventEnvelope, 502)
	for index := range events {
		events[index] = EventEnvelope{Sequence: uint64(index + 1), Type: "output.text.delta", Payload: map[string]any{"text": "value"}}
	}
	response := httptest.NewRecorder()
	writeInvocationEventReplay(response, events, 0, 500)
	if response.Code != http.StatusOK || response.Header().Get("X-DataGround-Has-More") != "true" || strings.Count(response.Body.String(), "\nevent:") != 500 || response.Body.Len() > maximumEventReplayPageBytes {
		t.Fatalf("record bound failed: %d %d", response.Code, response.Body.Len())
	}
	next := httptest.NewRecorder()
	writeInvocationEventReplay(next, events, 500, 500)
	if next.Header().Get("X-DataGround-Has-More") != "false" || strings.Count(next.Body.String(), "\nevent:") != 2 || !strings.HasPrefix(next.Body.String(), "id: 501\n") {
		t.Fatal("cursor continuation failed")
	}
	for index := range events[:3] {
		events[index].Payload = map[string]any{"text": strings.Repeat("界", 150000)}
	}
	response = httptest.NewRecorder()
	writeInvocationEventReplay(response, events[:3], 0, 500)
	if response.Code != http.StatusOK || response.Body.Len() > maximumEventReplayPageBytes || strings.Count(response.Body.String(), "\nevent:") != 2 || response.Header().Get("X-DataGround-Has-More") != "true" {
		t.Fatal("UTF-8 page byte bound failed")
	}
	events[0].Payload = map[string]any{"private-output": strings.Repeat("x", maximumEventReplayPageBytes)}
	response = httptest.NewRecorder()
	writeInvocationEventReplay(response, events[:1], 0, 1)
	if response.Code != http.StatusRequestEntityTooLarge || response.Header().Get("Content-Type") != "application/json" || strings.Contains(response.Body.String(), "private-output") || !strings.Contains(response.Body.String(), "EVENT_REPLAY_RECORD_TOO_LARGE") {
		t.Fatalf("oversized event response leaked or skipped: %d %s", response.Code, response.Body.String())
	}
	events[0].Payload = map[string]any{"text": "valid"}
	events[1].Payload = map[string]any{"private-output": math.NaN()}
	response = httptest.NewRecorder()
	writeInvocationEventReplay(response, events[:2], 0, 2)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "id: 1") || strings.Contains(response.Body.String(), "private-output") {
		t.Fatal("invalid page released a partial success")
	}
	response = httptest.NewRecorder()
	writeInvocationEventReplay(response, nil, 99, 200)
	if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("X-DataGround-Has-More") != "false" {
		t.Fatal("empty page changed replay state")
	}
}

func TestEventReplayLimitValidation(t *testing.T) {
	for raw, want := range map[string]int{"": 0, "legacy=ignored": 0, "limit=1": 1, "limit=200": 200, "limit=500": 500} {
		got, err := parseEventReplayLimit(raw)
		if err != nil || got != want {
			t.Fatalf("limit %q: %d %v", raw, got, err)
		}
	}
	for _, raw := range []string{"limit=0", "limit=501", "limit=-1", "limit=+1", "limit=", "limit=1.5", "limit=1&limit=2", "limit=1&other=2", "limit=%ZZ"} {
		if _, err := parseEventReplayLimit(raw); err == nil {
			t.Fatalf("accepted malformed limit %q", raw)
		}
	}
}
