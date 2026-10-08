package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
)

const contentTestDomain = "iso_00000000000000000001"
const contentTestToken = "development-content-token-at-least-thirty-two-bytes"
const contentTestRoute = "/v1/isolation-domains/{isolationDomainId}/invocations/{invocationId}/artifacts/{artifactId}/content"
const contentTestPath = "/v1/isolation-domains/iso_00000000000000000001/invocations/inv_00000000000000000001/artifacts/art_00000000000000000001/content"

type contentTestStore struct {
	record artifact.Record
	data   []byte
	opens  int
	open   func(context.Context) error
}

func (s *contentTestStore) GetInvocationArtifactRecord(context.Context, string, string) (artifact.Record, error) {
	return s.record, nil
}
func (s *contentTestStore) OpenInvocationArtifactObject(ctx context.Context, key string) (io.ReadCloser, error) {
	if key != artifact.ObjectKey(s.record) {
		return nil, artifact.ErrInvocationArtifactUnavailable
	}
	s.opens++
	if s.open != nil {
		if err := s.open(ctx); err != nil {
			return nil, err
		}
	}
	return io.NopCloser(bytes.NewReader(s.data)), nil
}
func newContentTestStore() *contentTestStore {
	data := []byte("<script>sensitive artifact content</script>")
	return &contentTestStore{data: data, record: artifact.Record{
		SchemaVersion: artifact.InvocationArtifactSchemaV1, IsolationDomainID: contentTestDomain,
		ID: "art_00000000000000000001", InvocationID: "inv_00000000000000000001", OperationID: "op_00000000000000000001", EffectID: "eff_00000000000000000001",
		Name: "unsafe.html", Kind: "file", MediaType: "text/html", SizeBytes: int64(len(data)), Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(data)), Sensitive: true,
	}}
}

type contentTestAuthorizer struct {
	requests []authz.Request
	failAt   int
	fail     error
}

func (a *contentTestAuthorizer) Authorize(_ context.Context, r authz.Request) error {
	a.requests = append(a.requests, r)
	if len(a.requests) == a.failAt {
		return a.fail
	}
	return nil
}

func protectedContentTestHandler(t *testing.T, store *contentTestStore, authorizer authz.Authorizer) http.Handler {
	t.Helper()
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(contentTestToken), PrincipalID: "usr_00000000000000000001", IsolationDomainID: contentTestDomain})
	if err != nil {
		t.Fatal(err)
	}
	protected, err := newProtectedRoute(authenticator, authorizer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	content, err := newArtifactContentHandler(store, authorizer, DurableArtifactContentConfig{Objects: store, MaximumBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(contentTestRouteWithMethod(), protected(authz.ReadInvocationArtifactContent, authz.Artifact, "artifactId", content))
	return mux
}
func contentTestRouteWithMethod() string { return "GET " + contentTestRoute }
func contentTestRequest(path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer "+contentTestToken)
	return r
}

func TestArtifactContentChecksPolicyBeforeReadAndDisclosure(t *testing.T) {
	for _, failAt := range []int{0, 1, 2, 3} {
		for _, failure := range []error{authz.ErrDenied, authz.ErrUnavailable} {
			t.Run(fmt.Sprintf("%d/%v", failAt, failure), func(t *testing.T) {
				store := newContentTestStore()
				authorizer := &contentTestAuthorizer{failAt: failAt, fail: failure}
				handler := protectedContentTestHandler(t, store, authorizer)
				response := newContentResponse()
				handler.ServeHTTP(response, contentTestRequest(contentTestPath))
				if failAt == 0 {
					if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), store.data) {
						t.Fatalf("read=%d %s", response.Code, response.Body.String())
					}
					for key, want := range map[string]string{"Content-Type": "application/octet-stream", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Content-Disposition": "attachment; filename=\"art_00000000000000000001.bin\"", "ETag": "\"" + store.record.Digest + "\""} {
						if response.Header().Get(key) != want {
							t.Fatalf("%s=%q", key, response.Header().Get(key))
						}
					}
				} else {
					expected := 503
					if failure == authz.ErrDenied {
						expected = 403
					}
					if response.Code != expected || strings.Contains(response.Body.String(), "sensitive artifact") {
						t.Fatalf("denial disclosed content: %d", response.Code)
					}
				}
				if failAt == 1 || failAt == 2 {
					if store.opens != 0 {
						t.Fatal("denial reached storage")
					}
				}
				first := authorizer.requests[0]
				for _, r := range authorizer.requests {
					if r.Action != authz.ReadInvocationArtifactContent || r.ResourceType != authz.Artifact || r.ResourceID != store.record.ID || r.IsolationDomainID != contentTestDomain || r.Principal.ID() != first.Principal.ID() || r.CorrelationID == "" || r.CorrelationID != first.CorrelationID {
						t.Fatal("authorization scope or correlation changed")
					}
				}
			})
		}
	}
}

func TestArtifactContentRejectsScopeCorruptionAndMalformedRequests(t *testing.T) {
	for _, name := range []string{"unauthenticated", "foreign domain", "foreign invocation", "corrupt", "range", "query", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			store := newContentTestStore()
			authorizer := &contentTestAuthorizer{}
			handler := protectedContentTestHandler(t, store, authorizer)
			request := contentTestRequest(contentTestPath)
			want := 400
			switch name {
			case "unauthenticated":
				request.Header.Del("Authorization")
				want = 401
			case "foreign domain":
				request = contentTestRequest(strings.Replace(contentTestPath, contentTestDomain, "iso_11111111111111111111", 1))
				want = 403
			case "foreign invocation":
				request = contentTestRequest(strings.Replace(contentTestPath, store.record.InvocationID, "inv_11111111111111111111", 1))
				want = 404
			case "corrupt":
				store.data = []byte("corrupt")
				want = 503
			case "range":
				request.Header.Set("Range", "bytes=0-3")
			case "query":
				request = contentTestRequest(contentTestPath + "?download=true")
			case "cancelled":
				ctx, cancel := context.WithCancel(request.Context())
				request = request.WithContext(ctx)
				store.open = func(context.Context) error { cancel(); return nil }
				want = 503
			}
			response := newContentResponse()
			handler.ServeHTTP(response, request)
			if response.Code != want || strings.Contains(response.Body.String(), "sensitive artifact") || strings.Contains(response.Body.String(), "invocation-artifacts/") {
				t.Fatalf("unsafe response: %d %s", response.Code, response.Body.String())
			}
			if name != "corrupt" && name != "cancelled" && store.opens != 0 {
				t.Fatal("rejected request reached storage")
			}
		})
	}
}

// Keep the capacity assertion at the response write boundary: verified buffers
// remain held while clients receive bytes.
func TestArtifactContentBoundsConcurrentResponseBuffers(t *testing.T) {
	store := newContentTestStore()
	authorizer, err := authz.NewDevelopmentCedarAuthorizer("usr_00000000000000000001", contentTestDomain)
	if err != nil {
		t.Fatal(err)
	}
	// Store counters are not needed in this concurrency test.
	objects := contentStaticObjects{data: store.data}
	content, err := newArtifactContentHandler(store, authorizer, DurableArtifactContentConfig{Objects: objects, MaximumBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := authn.NewPrincipal(authn.PrincipalInput{ID: "usr_00000000000000000001", Kind: authn.PrincipalHuman, Issuer: "test", Subject: "test", Audience: authn.APIAudience, IsolationDomains: []string{contentTestDomain}})
	if err != nil {
		t.Fatal(err)
	}
	req := contentTestRequest(contentTestPath)
	req.SetPathValue("isolationDomainId", contentTestDomain)
	req.SetPathValue("invocationId", store.record.InvocationID)
	req.SetPathValue("artifactId", store.record.ID)
	ctx := context.WithValue(req.Context(), authenticatedPrincipalKey{}, principal)
	ctx = context.WithValue(ctx, authenticatedCorrelationKey{}, "cor_00000000000000000001")
	req = req.WithContext(ctx)
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			content(&contentBlockedResponse{contentResponse: newContentResponse(), started: started, release: release}, req.Clone(ctx))
		}()
	}
	for range 4 {
		<-started
	}
	response := newContentResponse()
	content(response, req.Clone(ctx))
	close(release)
	wg.Wait()
	if response.Code != 503 {
		t.Fatalf("capacity status=%d", response.Code)
	}
}

type contentStaticObjects struct{ data []byte }

func (s contentStaticObjects) OpenInvocationArtifactObject(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

type contentBlockedResponse struct {
	*contentResponse
	started chan struct{}
	release chan struct{}
}

func (w *contentBlockedResponse) Write(p []byte) (int, error) {
	w.started <- struct{}{}
	<-w.release
	return w.contentResponse.Write(p)
}

// A recorder has no socket; expose the native server deadline port explicitly.
type contentResponse struct{ *httptest.ResponseRecorder }

func newContentResponse() *contentResponse                { return &contentResponse{httptest.NewRecorder()} }
func (*contentResponse) SetWriteDeadline(time.Time) error { return nil }

func TestArtifactContentRequiresResponseDeadlineSupport(t *testing.T) {
	store := newContentTestStore()
	handler := protectedContentTestHandler(t, store, &contentTestAuthorizer{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, contentTestRequest(contentTestPath))
	if response.Code != 503 || bytes.Contains(response.Body.Bytes(), store.data) {
		t.Fatal("content disclosed without response deadline support")
	}
}

func TestArtifactContentNativeHTTPTransfer(t *testing.T) {
	store := newContentTestStore()
	server := httptest.NewServer(protectedContentTestHandler(t, store, &contentTestAuthorizer{}))
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+contentTestPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+contentTestToken)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || !bytes.Equal(content, store.data) {
		t.Fatalf("native transfer failed: %d %v", response.StatusCode, err)
	}
}
