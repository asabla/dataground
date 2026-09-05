package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
)

func TestInvocationDiscoveryRecoversBoundedHistoryWithoutContent(t *testing.T) {
	handler := newHandler(t)
	service := createService(t, handler, testDomain, "history-service-1")
	revision := createPublishedRevision(t, handler, testDomain, service.Metadata.ID)
	assignAlias(t, handler, testDomain, service.Metadata.ID, revision.Metadata.ID, "history-alias-1")
	first := invoke(t, handler, testDomain, service.Metadata.ID, "success", "history-invoke-1")
	second := invoke(t, handler, testDomain, service.Metadata.ID, "question", "history-invoke-2")
	path := serviceCollectionPath(testDomain) + "/" + service.Metadata.ID + "/invocations"
	type page struct {
		Items      []domain.InvocationSummary `json:"items"`
		NextCursor string                     `json:"nextCursor"`
	}
	read := func(path string) page {
		t.Helper()
		response := perform(t, handler, http.MethodGet, path, "", nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("history status = %d", response.Code)
		}
		var fields struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		for _, item := range fields.Items {
			for _, key := range []string{"input", "result", "error", "usage", "artifactIds"} {
				if _, found := item[key]; found {
					t.Fatalf("history disclosed %s", key)
				}
			}
		}
		var result page
		decodeResponse(t, response, &result)
		return result
	}
	newest := read(path + "?limit=1")
	if len(newest.Items) != 1 || newest.Items[0].Metadata.ID != second.Metadata.ID || newest.NextCursor == "" {
		t.Fatal("newest invocation was not discovered")
	}
	// A concurrent new invocation must not shift the continuation boundary.
	invoke(t, handler, testDomain, service.Metadata.ID, "success", "history-invoke-3")
	older := read(path + "?limit=1&cursor=" + newest.NextCursor)
	if len(older.Items) != 1 || older.Items[0].Metadata.ID != first.Metadata.ID || older.NextCursor != "" {
		t.Fatal("history continuation skipped or duplicated an invocation")
	}
	otherService := createService(t, handler, testDomain, "history-service-2")
	otherPath := serviceCollectionPath(testDomain) + "/" + otherService.Metadata.ID + "/invocations"
	if empty := read(otherPath); empty.Items == nil || len(empty.Items) != 0 {
		t.Fatal("empty service history was not an empty array")
	}
	for path, status := range map[string]int{
		otherPath + "?cursor=" + newest.NextCursor:                                  http.StatusBadRequest,
		serviceCollectionPath(testDomain) + "/svc_00000000000000000009/invocations": http.StatusNotFound,
		strings.Replace(path, testDomain, "iso_00000000000000000002", 1):            http.StatusForbidden,
	} {
		response := perform(t, handler, http.MethodGet, path, "", nil, nil)
		if response.Code != status {
			t.Fatalf("history denial = %d, want %d", response.Code, status)
		}
	}
}

func TestInvocationDiscoveryAuthorizesServiceBeforeQueryOrDisclosure(t *testing.T) {
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
	if err != nil {
		t.Fatal(err)
	}
	for _, denial := range []error{authz.ErrDenied, authz.ErrUnavailable} {
		called := false
		handler, err := api.NewHandler(authenticator, authorizerFunc(func(_ context.Context, request authz.Request) error {
			called = true
			if request.Action != authz.ListInvocations || request.ResourceType != authz.AgentService || request.ResourceID != "svc_00000000000000000001" || request.IsolationDomainID != testDomain {
				t.Fatal("history authorized the wrong resource")
			}
			return denial
		}))
		if err != nil {
			t.Fatal(err)
		}
		response := perform(t, handler, http.MethodGet, serviceCollectionPath(testDomain)+"/svc_00000000000000000001/invocations?limit=invalid", "", nil, nil)
		expected := http.StatusForbidden
		if denial == authz.ErrUnavailable {
			expected = http.StatusServiceUnavailable
		}
		if !called || response.Code != expected {
			t.Fatalf("query or resource lookup ran before authorization: %d", response.Code)
		}
	}
}

func TestInvocationDiscoveryErrorsRetainAuthorizationCorrelation(t *testing.T) {
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(testToken), PrincipalID: testActor, IsolationDomainID: testDomain})
	if err != nil {
		t.Fatal(err)
	}
	var correlationID string
	handler, err := api.NewHandler(authenticator, authorizerFunc(func(_ context.Context, request authz.Request) error {
		correlationID = request.CorrelationID
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	path := serviceCollectionPath(testDomain) + "/svc_00000000000000000001/invocations"
	for query, status := range map[string]int{"?limit=invalid": http.StatusBadRequest, "": http.StatusNotFound} {
		response := perform(t, handler, http.MethodGet, path+query, "", nil, nil)
		var envelope api.ErrorEnvelope
		decodeResponse(t, response, &envelope)
		if response.Code != status || correlationID == "" || envelope.Error.CorrelationID != correlationID {
			t.Fatal("history error lost the authorized request correlation")
		}
	}
}
