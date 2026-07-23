package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

const objectKey = "enforcement-bundles/v1/iso_aaaaaaaaaaaaaaaaaaaa/rev_bbbbbbbbbbbbbbbbbbbb/bundle-1/" +
	"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc.yaml"

func TestStoreUsesBoundedConditionalS3Requests(t *testing.T) {
	content := []byte("version: 1\n")
	digest := policyDigest(content)
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
			response.Header().Set("Content-Length", "11")
			_, _ = response.Write(content)
		default:
			t.Fatalf("unexpected method %q", request.Method)
		}
	}))
	defer server.Close()

	store := newTestStore(t, server, PathStyle)
	if err := store.PutEnforcementObjectIfAbsent(
		context.Background(), objectKey, bytes.NewReader(content), int64(len(content)), digest,
	); err != nil {
		t.Fatalf("put enforcement object: %v", err)
	}
	put := <-requests
	if put.Method != http.MethodPut || put.URL.Path != "/platform-objects/"+objectKey ||
		put.Header.Get("If-None-Match") != "*" ||
		put.Header.Get("Content-Type") != execution.EnforcementBundleMediaType ||
		put.Header.Get("x-amz-checksum-sha256") != base64.StdEncoding.EncodeToString(checksum[:]) ||
		put.ContentLength != int64(len(content)) || !bytes.Equal(mustRead(t, put.Body), content) {
		t.Fatalf("unexpected PUT request: %#v", put)
	}

	object, err := store.OpenEnforcementObject(context.Background(), objectKey)
	if err != nil {
		t.Fatalf("open enforcement object: %v", err)
	}
	defer object.Close()
	if returned := mustRead(t, object); !bytes.Equal(returned, content) {
		t.Fatalf("GET content = %q", returned)
	}
	get := <-requests
	if get.Method != http.MethodGet || get.URL.Path != "/platform-objects/"+objectKey ||
		get.Header.Get("Accept-Encoding") != "identity" {
		t.Fatalf("unexpected GET request: %#v", get)
	}
}

func TestStoreMapsOnlyStableS3Outcomes(t *testing.T) {
	content := []byte("version: 1\n")
	digest := policyDigest(content)
	tests := map[string]struct {
		method string
		status int
		want   error
	}{
		"missing object":       {method: http.MethodGet, status: http.StatusNotFound, want: execution.ErrEnforcementObjectMissing},
		"unauthorized read":    {method: http.MethodGet, status: http.StatusForbidden, want: errUnavailable},
		"existing object":      {method: http.MethodPut, status: http.StatusPreconditionFailed, want: execution.ErrEnforcementObjectConflict},
		"concurrent operation": {method: http.MethodPut, status: http.StatusConflict, want: errUnavailable},
		"upstream failure":     {method: http.MethodPut, status: http.StatusInternalServerError, want: errUnavailable},
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
			store := newTestStore(t, server, PathStyle)
			var err error
			if test.method == http.MethodGet {
				_, err = store.OpenEnforcementObject(context.Background(), objectKey)
			} else {
				err = store.PutEnforcementObjectIfAbsent(
					context.Background(), objectKey, bytes.NewReader(content), int64(len(content)), digest,
				)
			}
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestStoreRejectsInvalidWriteBeforeRequest(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	store := newTestStore(t, server, PathStyle)
	content := []byte("version: 1\n")
	digest := policyDigest(content)
	tests := map[string]struct {
		key     string
		content io.Reader
		size    int64
		digest  string
	}{
		"path traversal": {key: "enforcement-bundles/v1/../secret", content: bytes.NewReader(content), size: int64(len(content)), digest: digest},
		"wrong prefix":   {key: "lakehouse/object", content: bytes.NewReader(content), size: int64(len(content)), digest: digest},
		"oversized key":  {key: "enforcement-bundles/v1/" + strings.Repeat("a", 1024), content: bytes.NewReader(content), size: int64(len(content)), digest: digest},
		"nil content":    {key: objectKey, size: int64(len(content)), digest: digest},
		"short content":  {key: objectKey, content: bytes.NewReader(content[:2]), size: int64(len(content)), digest: digest},
		"extra content":  {key: objectKey, content: bytes.NewReader(append(content, 'x')), size: int64(len(content)), digest: digest},
		"wrong digest":   {key: objectKey, content: bytes.NewReader(content), size: int64(len(content)), digest: "sha256:" + strings.Repeat("0", 64)},
		"oversized":      {key: objectKey, content: bytes.NewReader(content), size: execution.MaximumEnforcementPolicyBytes + 1, digest: digest},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := store.PutEnforcementObjectIfAbsent(
				context.Background(), test.key, test.content, test.size, test.digest,
			); err == nil {
				t.Fatal("invalid write accepted")
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid writes made %d requests", requests.Load())
	}
}

func TestStoreFailsClosedOnRedirectAndEncodedOrOversizedReads(t *testing.T) {
	tests := map[string]func(http.ResponseWriter){
		"redirect": func(response http.ResponseWriter) {
			response.Header().Set("Location", "https://attacker.invalid/object")
			response.WriteHeader(http.StatusTemporaryRedirect)
		},
		"encoded": func(response http.ResponseWriter) {
			response.Header().Set("Content-Encoding", "gzip")
			response.WriteHeader(http.StatusOK)
		},
		"oversized": func(response http.ResponseWriter) {
			response.Header().Set("Content-Length", "4194305")
			response.WriteHeader(http.StatusOK)
		},
	}
	for name, respond := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				respond(response)
			}))
			defer server.Close()
			store := newTestStore(t, server, PathStyle)
			if _, err := store.OpenEnforcementObject(context.Background(), objectKey); !errors.Is(err, errUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestStoreValidatesEndpointAndAddressing(t *testing.T) {
	tests := map[string]Config{
		"missing endpoint":      {Bucket: "platform-objects", AddressingStyle: PathStyle},
		"endpoint credentials":  {Endpoint: "https://user:pass@s3.example.test", Bucket: "platform-objects", AddressingStyle: PathStyle},
		"endpoint path":         {Endpoint: "https://s3.example.test/base", Bucket: "platform-objects", AddressingStyle: PathStyle},
		"remote HTTP":           {Endpoint: "http://s3.example.test", Bucket: "platform-objects", AddressingStyle: PathStyle, AllowHTTPForLoopback: true},
		"unapproved HTTP":       {Endpoint: "http://127.0.0.1:9000", Bucket: "platform-objects", AddressingStyle: PathStyle},
		"invalid bucket":        {Endpoint: "https://s3.example.test", Bucket: "../other", AddressingStyle: PathStyle},
		"missing style":         {Endpoint: "https://s3.example.test", Bucket: "platform-objects"},
		"virtual IP endpoint":   {Endpoint: "https://127.0.0.1:9000", Bucket: "platform-objects", AddressingStyle: VirtualHostedStyle},
		"endpoint query":        {Endpoint: "https://s3.example.test?secret=value", Bucket: "platform-objects", AddressingStyle: PathStyle},
		"empty endpoint query":  {Endpoint: "https://s3.example.test?", Bucket: "platform-objects", AddressingStyle: PathStyle},
		"endpoint invalid port": {Endpoint: "https://s3.example.test:0", Bucket: "platform-objects", AddressingStyle: PathStyle},
		"missing transport": {
			Endpoint: "https://s3.example.test", Bucket: "platform-objects", AddressingStyle: PathStyle,
			HTTPClient: &http.Client{},
		},
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := New(config); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}

	store, err := New(Config{
		Endpoint: "https://s3.example.test:9443", Bucket: "platform-objects",
		AddressingStyle: VirtualHostedStyle, HTTPClient: &http.Client{Transport: http.DefaultTransport},
	})
	if err != nil {
		t.Fatalf("new virtual-hosted store: %v", err)
	}
	objectURL, err := store.objectURL(objectKey, "enforcement-bundles/v1/")
	if err != nil {
		t.Fatalf("object URL: %v", err)
	}
	want := "https://platform-objects.s3.example.test:9443/" + objectKey
	if objectURL != want {
		t.Fatalf("object URL = %q, want %q", objectURL, want)
	}
	if _, err := New(Config{
		Endpoint: "http://127.0.0.1:9000", Bucket: "platform-objects", AddressingStyle: PathStyle,
		AllowHTTPForLoopback: true, HTTPClient: &http.Client{Transport: http.DefaultTransport},
	}); err != nil {
		t.Fatalf("explicit loopback HTTP: %v", err)
	}
}

func TestStorePreservesCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
		response.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()
	store := newTestStore(t, server, PathStyle)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.OpenEnforcementObject(ctx, objectKey); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if err := store.PutEnforcementObjectIfAbsent(
		ctx, objectKey, panicReader{}, 1, "sha256:"+strings.Repeat("0", 64),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("write error = %v", err)
	}
}

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) {
	panic("cancelled write read its body")
}

func newTestStore(t *testing.T, server *httptest.Server, style AddressingStyle) *Store {
	t.Helper()
	store, err := New(Config{
		Endpoint: server.URL, Bucket: "platform-objects", AddressingStyle: style, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func policyDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func mustRead(t *testing.T, reader io.Reader) []byte {
	t.Helper()
	content, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return content
}
