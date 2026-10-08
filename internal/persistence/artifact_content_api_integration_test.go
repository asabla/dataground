package persistence_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/persistence"
)

// Reuse the finalized, claim-bound catalog fixture to exercise the public
// route with real PostgreSQL authorization audit and an HTTP object endpoint.
func verifyArtifactContentAPI(t *testing.T, ctx context.Context, repository *persistence.Repository, record artifact.Record, content []byte) {
	t.Helper()
	reads := 0
	objects := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		if r.Method != "GET" || r.URL.Path != "/artifacts/"+record.ObjectKey || r.Header.Get("Authorization") != "" {
			t.Error("object routing or credential leak")
		}
		_, _ = w.Write(content)
	}))
	defer objects.Close()
	store, err := s3store.New(s3store.Config{Endpoint: objects.URL, Bucket: "artifacts", AddressingStyle: s3store.PathStyle, AllowHTTPForLoopback: true, HTTPClient: &http.Client{Transport: objects.Client().Transport, Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	objectReader, err := s3store.NewArtifactStore(store, 1024)
	if err != nil {
		t.Fatal(err)
	}
	const principalID = "usr_00000000000000000001"
	const token = "artifact-read-integration-token-with-thirty-two-bytes"
	authentication, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: principalID, IsolationDomainID: record.IsolationDomainID})
	if err != nil {
		t.Fatal(err)
	}
	auditedAuthentication, err := authn.NewAuditedAuthenticator(authentication, repository)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authz.NewDevelopmentCedarAuthorizer(principalID, record.IsolationDomainID)
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := authz.NewAuditedAuthorizer(policy, repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(repository, auditedAuthentication, authorizer, api.DurableArtifactContentConfig{Objects: objectReader, MaximumBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/isolation-domains/" + record.IsolationDomainID + "/invocations/" + record.InvocationID + "/artifacts/" + record.ID + "/content"
	request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+token)
	response := &artifactContentResponse{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(response, request)
	if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), content) || reads != 1 {
		t.Fatalf("durable content response: %d %s", response.Code, response.Body.String())
	}
	// Repeating a read has no storage mutation or idempotency requirement.
	request = httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+token)
	response = &artifactContentResponse{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(response, request)
	if response.Code != 200 || reads != 2 {
		t.Fatal("repeated content read failed")
	}
	unconfigured, err := api.NewDurableHandler(repository, auditedAuthentication, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+token)
	response = &artifactContentResponse{ResponseRecorder: httptest.NewRecorder()}
	unconfigured.ServeHTTP(response, request)
	if response.Code != 503 || reads != 2 {
		t.Fatal("unconfigured handler reached storage")
	}
	// The concrete catalog remains independently consumable by the verified reader.
	reader, err := artifact.NewReader(repository, objectReader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := reader.Read(ctx, record.IsolationDomainID, "inv_11111111111111111111", record.ID)
	if err == nil || len(wrong.Bytes) != 0 || reads != 2 {
		t.Fatal("foreign invocation content was read")
	}
}

type artifactContentResponse struct{ *httptest.ResponseRecorder }

func (*artifactContentResponse) SetWriteDeadline(time.Time) error { return nil }
