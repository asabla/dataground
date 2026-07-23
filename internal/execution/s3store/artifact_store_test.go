package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asabla/dataground/internal/artifact"
)

const artifactTestMaximumBytes int64 = 32 << 20

const invocationArtifactObjectKey = "invocation-artifacts/v1/iso_aaaaaaaaaaaaaaaaaaaa/" +
	"inv_bbbbbbbbbbbbbbbbbbbb/art_cccccccccccccccccccc/" +
	"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

func TestStoreUsesBoundedConditionalInvocationArtifactRequests(t *testing.T) {
	content := []byte("artifact content")
	digest := artifactDigest(content)
	checksum := sha256.Sum256(content)
	requests := make(chan *http.Request, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		clone := request.Clone(request.Context())
		clone.Body = io.NopCloser(bytes.NewReader(mustRead(t, request.Body)))
		requests <- clone
		switch request.Method {
		case http.MethodPut:
			response.WriteHeader(http.StatusOK)
		case http.MethodGet:
			response.Header().Set("Content-Length", "16")
			_, _ = response.Write(content)
		default:
			t.Fatalf("unexpected method %q", request.Method)
		}
	}))
	defer server.Close()

	store := newTestArtifactStore(t, server)
	if err := store.PutInvocationArtifactObjectIfAbsent(
		context.Background(),
		invocationArtifactObjectKey,
		bytes.NewReader(content),
		int64(len(content)),
		digest,
		"text/plain; charset=utf-8",
	); err != nil {
		t.Fatalf("put invocation artifact: %v", err)
	}
	put := <-requests
	if put.Method != http.MethodPut ||
		put.URL.Path != "/platform-objects/"+invocationArtifactObjectKey ||
		put.Header.Get("If-None-Match") != "*" ||
		put.Header.Get("Content-Type") != "text/plain; charset=utf-8" ||
		put.Header.Get("x-amz-checksum-sha256") != base64.StdEncoding.EncodeToString(checksum[:]) ||
		put.ContentLength != int64(len(content)) ||
		!bytes.Equal(mustRead(t, put.Body), content) {
		t.Fatalf("unexpected PUT request: %#v", put)
	}

	object, err := store.OpenInvocationArtifactObject(
		context.Background(),
		invocationArtifactObjectKey,
	)
	if err != nil {
		t.Fatalf("open invocation artifact: %v", err)
	}
	defer object.Close()
	if returned := mustRead(t, object); !bytes.Equal(returned, content) {
		t.Fatalf("GET content = %q", returned)
	}
	get := <-requests
	if get.Method != http.MethodGet ||
		get.URL.Path != "/platform-objects/"+invocationArtifactObjectKey ||
		get.Header.Get("Accept-Encoding") != "identity" {
		t.Fatalf("unexpected GET request: %#v", get)
	}
}

func TestStoreMapsOnlyStableInvocationArtifactOutcomes(t *testing.T) {
	content := []byte("artifact")
	digest := artifactDigest(content)
	tests := map[string]struct {
		method string
		status int
		want   error
	}{
		"missing object":       {method: http.MethodGet, status: http.StatusNotFound, want: artifact.ErrInvocationArtifactObjectMissing},
		"unauthorized read":    {method: http.MethodGet, status: http.StatusForbidden, want: artifact.ErrInvocationArtifactUnavailable},
		"existing object":      {method: http.MethodPut, status: http.StatusPreconditionFailed, want: artifact.ErrInvocationArtifactObjectConflict},
		"concurrent operation": {method: http.MethodPut, status: http.StatusConflict, want: artifact.ErrInvocationArtifactUnavailable},
		"upstream failure":     {method: http.MethodPut, status: http.StatusInternalServerError, want: artifact.ErrInvocationArtifactUnavailable},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Method != test.method {
					t.Fatalf("method = %q", request.Method)
				}
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte("sensitive upstream detail"))
			}))
			defer server.Close()
			store := newTestArtifactStore(t, server)
			var err error
			if test.method == http.MethodGet {
				_, err = store.OpenInvocationArtifactObject(
					context.Background(),
					invocationArtifactObjectKey,
				)
			} else {
				err = store.PutInvocationArtifactObjectIfAbsent(
					context.Background(),
					invocationArtifactObjectKey,
					bytes.NewReader(content),
					int64(len(content)),
					digest,
					"application/octet-stream",
				)
			}
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestStoreSeparatesPlatformObjectKeyspaces(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	store := newTestArtifactStore(t, server)
	content := []byte("artifact")
	digest := artifactDigest(content)

	if _, err := store.store.OpenEnforcementObject(
		context.Background(),
		invocationArtifactObjectKey,
	); err == nil {
		t.Fatal("enforcement reader accepted invocation-artifact key")
	}
	if err := store.store.PutEnforcementObjectIfAbsent(
		context.Background(),
		invocationArtifactObjectKey,
		bytes.NewReader(content),
		int64(len(content)),
		digest,
	); err == nil {
		t.Fatal("enforcement writer accepted invocation-artifact key")
	}
	if _, err := store.OpenInvocationArtifactObject(
		context.Background(),
		objectKey,
	); !errors.Is(err, artifact.ErrInvocationArtifactObjectConflict) {
		t.Fatalf("artifact reader error = %v", err)
	}
	if err := store.PutInvocationArtifactObjectIfAbsent(
		context.Background(),
		objectKey,
		bytes.NewReader(content),
		int64(len(content)),
		digest,
		"application/octet-stream",
	); !errors.Is(err, artifact.ErrInvocationArtifactObjectConflict) {
		t.Fatalf("artifact writer error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("cross-keyspace calls made %d requests", requests.Load())
	}
}

func TestStoreRejectsInvalidInvocationArtifactWriteBeforeRequest(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	store := newTestArtifactStore(t, server)
	content := []byte("artifact")
	digest := artifactDigest(content)
	tests := map[string]struct {
		key       string
		content   io.Reader
		size      int64
		digest    string
		mediaType string
	}{
		"path traversal": {key: "invocation-artifacts/v1/../secret", content: bytes.NewReader(content), size: int64(len(content)), digest: digest, mediaType: "text/plain"},
		"malformed scoped key": {key: "invocation-artifacts/v1/not-a-domain/object", content: bytes.NewReader(content), size: int64(len(content)), digest: digest, mediaType: "text/plain"},
		"oversized key": {key: "invocation-artifacts/v1/" + strings.Repeat("a", 1024), content: bytes.NewReader(content), size: int64(len(content)), digest: digest, mediaType: "text/plain"},
		"nil content": {key: invocationArtifactObjectKey, size: int64(len(content)), digest: digest, mediaType: "text/plain"},
		"short content": {key: invocationArtifactObjectKey, content: bytes.NewReader(content[:2]), size: int64(len(content)), digest: digest, mediaType: "text/plain"},
		"extra content": {key: invocationArtifactObjectKey, content: bytes.NewReader(append(bytes.Clone(content), 'x')), size: int64(len(content)), digest: digest, mediaType: "text/plain"},
		"wrong digest": {key: invocationArtifactObjectKey, content: bytes.NewReader(content), size: int64(len(content)), digest: "sha256:" + strings.Repeat("0", 64), mediaType: "text/plain"},
		"oversized": {key: invocationArtifactObjectKey, content: bytes.NewReader(content), size: artifactTestMaximumBytes + 1, digest: digest, mediaType: "text/plain"},
		"invalid media type": {key: invocationArtifactObjectKey, content: bytes.NewReader(content), size: int64(len(content)), digest: digest, mediaType: "text/plain\r\nx-secret: value"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := store.PutInvocationArtifactObjectIfAbsent(
				context.Background(),
				test.key,
				test.content,
				test.size,
				test.digest,
				test.mediaType,
			)
			if !errors.Is(err, artifact.ErrInvocationArtifactObjectConflict) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid writes made %d requests", requests.Load())
	}
}

func TestStoreFailsClosedOnEncodedOrDeclaredOversizedArtifactReads(t *testing.T) {
	tests := map[string]func(http.ResponseWriter){
		"encoded": func(response http.ResponseWriter) {
			response.Header().Set("Content-Encoding", "gzip")
			response.WriteHeader(http.StatusOK)
		},
		"oversized": func(response http.ResponseWriter) {
			response.Header().Set(
				"Content-Length",
				"33554433",
			)
			response.WriteHeader(http.StatusOK)
		},
	}
	for name, respond := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				respond(response)
			}))
			defer server.Close()
			store := newTestArtifactStore(t, server)
			_, err := store.OpenInvocationArtifactObject(
				context.Background(),
				invocationArtifactObjectKey,
			)
			if !errors.Is(err, artifact.ErrInvocationArtifactUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestStorePreservesInvocationArtifactCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
		response.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()
	store := newTestArtifactStore(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.OpenInvocationArtifactObject(
		ctx,
		invocationArtifactObjectKey,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("read error = %v", err)
	}
	if err := store.PutInvocationArtifactObjectIfAbsent(
		ctx,
		invocationArtifactObjectKey,
		panicReader{},
		1,
		"sha256:"+strings.Repeat("0", 64),
		"application/octet-stream",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("write error = %v", err)
	}
}

func TestNewArtifactStoreRequiresBoundedConfiguration(t *testing.T) {
	baseStore := &Store{}
	if _, err := NewArtifactStore(nil, artifactTestMaximumBytes); err == nil {
		t.Fatal("nil S3 store accepted")
	}
	if _, err := NewArtifactStore(baseStore, 0); err == nil {
		t.Fatal("unbounded artifact store accepted")
	}
	if _, err := NewArtifactStore(baseStore, int64(^uint64(0)>>1)); err == nil {
		t.Fatal("overflow-prone artifact bound accepted")
	}
}

func newTestArtifactStore(t *testing.T, server *httptest.Server) *ArtifactStore {
	t.Helper()
	store, err := NewArtifactStore(
		newTestStore(t, server, PathStyle),
		artifactTestMaximumBytes,
	)
	if err != nil {
		t.Fatalf("new invocation-artifact store: %v", err)
	}
	return store
}

