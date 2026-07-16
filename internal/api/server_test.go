package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asabla/dataground/internal/api"
)

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/livez", "/readyz"} {
		path := path
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()

			api.NewHandler().ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("expected no-store cache policy")
			}
			if response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("expected JSON content type")
			}
			if response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("expected nosniff content policy")
			}

			var body struct {
				Status string `json:"status"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Status != "ok" {
				t.Fatalf("expected ok status, got %q", body.Status)
			}
		})
	}
}

func TestHealthEndpointsRejectOtherMethods(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodPost, "/livez", nil)
	response := httptest.NewRecorder()

	api.NewHandler().ServeHTTP(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d, got %d", http.StatusMethodNotAllowed, response.Code)
	}
}
