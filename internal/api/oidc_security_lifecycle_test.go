package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asabla/dataground/internal/api"
)

func TestDurableOIDCDPoPAssemblyRejectsProtectedRequestsOutsideRefreshLifecycle(t *testing.T) {
	t.Parallel()

	source := &apiAssemblyKeysetSource{snapshot: apiAssemblyKeysetSnapshot(1)}
	assembly, err := api.NewDurableOIDCDPoPAssembly(
		context.Background(),
		apiAssemblyConfig(t, source),
	)
	if err != nil {
		t.Fatalf("assemble durable OIDC DPoP API: %v", err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/isolation-domains/iso_00000000000000000001/agent-services",
		nil,
	)
	request.Header.Set("Authorization", "DPoP credential")
	request.Header.Set("DPoP", "proof")
	response := httptest.NewRecorder()
	assembly.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stopped lifecycle status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if request.Header.Get("Authorization") != "" || request.Header.Get("DPoP") != "" {
		t.Fatal("stopped lifecycle retained credential headers")
	}

	livenessRequest := httptest.NewRequest(http.MethodGet, "/livez", nil)
	livenessRequest.Header.Set("Authorization", "DPoP credential")
	livenessRequest.Header.Set("DPoP", "proof")
	liveness := httptest.NewRecorder()
	assembly.Handler().ServeHTTP(liveness, livenessRequest)
	if liveness.Code != http.StatusOK {
		t.Fatalf("liveness status = %d, want %d", liveness.Code, http.StatusOK)
	}
	if livenessRequest.Header.Get("Authorization") != "" || livenessRequest.Header.Get("DPoP") != "" {
		t.Fatal("liveness retained credential headers")
	}
}
