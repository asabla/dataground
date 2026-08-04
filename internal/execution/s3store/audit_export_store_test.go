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
	"testing"

	"github.com/asabla/dataground/internal/audittransport"
)

func TestAuditExportStoreUsesConditionalChecksummedRequests(t *testing.T) {
	content := []byte("encrypted package")
	digest := sha256.Sum256(content)
	auditExportObjectKey := auditExportKeyForDigest(digest)
	requests := make(chan *http.Request, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		clone := request.Clone(request.Context())
		clone.Body = io.NopCloser(bytes.NewReader(mustRead(t, request.Body)))
		requests <- clone
		if request.Method == http.MethodPut {
			response.WriteHeader(http.StatusOK)
			return
		}
		response.Header().Set("Content-Length", "17")
		_, _ = response.Write(content)
	}))
	defer server.Close()
	store, err := NewAuditExportStore(newTestStore(t, server, PathStyle))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutAuditExportObjectIfAbsent(
		context.Background(), auditExportObjectKey, bytes.NewReader(content), int64(len(content)), digest,
	); err != nil {
		t.Fatal(err)
	}
	put := <-requests
	if put.URL.Path != "/platform-objects/"+auditExportObjectKey ||
		put.Header.Get("If-None-Match") != "*" ||
		put.Header.Get("Content-Type") != audittransport.PackageMediaType ||
		put.Header.Get("x-amz-checksum-sha256") != base64.StdEncoding.EncodeToString(digest[:]) {
		t.Fatalf("unexpected PUT request: %#v", put)
	}
	reader, err := store.OpenAuditExportObject(context.Background(), auditExportObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if got := mustRead(t, reader); !bytes.Equal(got, content) {
		t.Fatalf("content = %q", got)
	}
	get := <-requests
	if get.Method != http.MethodGet || get.Header.Get("Accept-Encoding") != "identity" {
		t.Fatalf("unexpected GET request: %#v", get)
	}
}

func TestAuditExportStoreSeparatesKeyspaceAndStableOutcomes(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPut {
			response.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		response.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	store, err := NewAuditExportStore(newTestStore(t, server, PathStyle))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("encrypted package")
	digest := sha256.Sum256(content)
	auditExportObjectKey := auditExportKeyForDigest(digest)
	if err := store.PutAuditExportObjectIfAbsent(
		context.Background(), auditExportObjectKey, bytes.NewReader(content), int64(len(content)), digest,
	); !errors.Is(err, audittransport.ErrObjectConflict) {
		t.Fatalf("write error = %v", err)
	}
	if _, err := store.OpenAuditExportObject(
		context.Background(), auditExportObjectKey,
	); !errors.Is(err, audittransport.ErrObjectMissing) {
		t.Fatalf("read error = %v", err)
	}
	if _, err := store.OpenAuditExportObject(
		context.Background(), invocationArtifactObjectKey,
	); !errors.Is(err, audittransport.ErrObjectConflict) {
		t.Fatalf("cross-keyspace error = %v", err)
	}
	if err := store.PutAuditExportObjectIfAbsent(
		context.Background(), auditExportObjectKey, bytes.NewReader(content), int64(len(content)),
		sha256.Sum256([]byte("different package")),
	); !errors.Is(err, audittransport.ErrObjectConflict) {
		t.Fatalf("object-key digest mismatch error = %v", err)
	}
}

func auditExportKeyForDigest(digest [sha256.Size]byte) string {
	return "audit-export-deliveries/v1/iso_aaaaaaaaaaaaaaaaaaaa/" +
		"adl_bbbbbbbbbbbbbbbbbbbb/" + hex.EncodeToString(digest[:]) + ".json"
}
