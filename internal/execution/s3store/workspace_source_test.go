package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/workspace"
)

func TestWorkspaceSourceStorePinsProfileAndClosedKeyNamespace(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(404) }))
	defer server.Close()
	makeStore := func(bucket string) *WorkspaceSourceStore {
		base, err := New(Config{Endpoint: server.URL, Bucket: bucket, AddressingStyle: PathStyle, AllowHTTPForLoopback: true, HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		store, err := NewWorkspaceSourceStore(base)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	s := makeStore("sources")
	if s.WorkspaceSourceStorageDigest() != makeStore("sources").WorkspaceSourceStorageDigest() || s.WorkspaceSourceStorageDigest() == makeStore("other-sources").WorkspaceSourceStorageDigest() {
		t.Fatal("profile binding unstable or omitted bucket")
	}
	data := make([]byte, 1024)
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	key := "workspace-sources/v1/iso_" + strings.Repeat("1", 20) + "/wsp_" + strings.Repeat("2", 20) + "/src_" + strings.Repeat("3", 20) + "/" + hex.EncodeToString(sum[:])
	for _, bad := range []string{"../outside", "invocation-artifacts/v1/" + key, key + "/extra", strings.Replace(key, "/wsp_", "/../wsp_", 1)} {
		if _, err := s.OpenWorkspaceSource(context.Background(), bad); err == nil {
			t.Fatal("bad key read")
		}
		if err := s.PutWorkspaceSourceIfAbsent(context.Background(), bad, data, digest); err == nil {
			t.Fatal("bad key written")
		}
	}
	if requests != 0 {
		t.Fatal("bad key reached transport")
	}
	if err := s.PutWorkspaceSourceIfAbsent(context.Background(), key, bytes.Repeat([]byte("x"), 1024), digest); err == nil || requests != 0 {
		t.Fatal("digest mismatch reached transport")
	}
	if _, err := s.OpenWorkspaceSource(context.Background(), key); !errors.Is(err, workspace.ErrSourceObjectMissing) {
		t.Fatal("missing object misclassified", err)
	}
}
func TestWorkspaceSourceStoreRejectsRedirectsAndOversizedResponses(t *testing.T) {
	for _, mode := range []string{"redirect", "oversized", "encoding"} {
		t.Run(mode, func(t *testing.T) {
			targetRequests := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetRequests++; w.Write(make([]byte, 1024)) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, r, target.URL, 302)
				case "oversized":
					w.Header().Set("Content-Length", "33554433")
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				}
			}))
			defer server.Close()
			base, err := New(Config{Endpoint: server.URL, Bucket: "sources", AddressingStyle: PathStyle, AllowHTTPForLoopback: true, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			s, err := NewWorkspaceSourceStore(base)
			if err != nil {
				t.Fatal(err)
			}
			key := "workspace-sources/v1/iso_" + strings.Repeat("1", 20) + "/wsp_" + strings.Repeat("2", 20) + "/src_" + strings.Repeat("3", 20) + "/" + strings.Repeat("a", 64)
			if _, err := s.OpenWorkspaceSource(context.Background(), key); !errors.Is(err, workspace.ErrSourceUnavailable) || targetRequests != 0 {
				t.Fatal("unsafe response accepted", err)
			}
		})
	}
}
