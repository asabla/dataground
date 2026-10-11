package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/asabla/dataground/internal/workspace"
)

var workspaceSourceKeyPattern = regexp.MustCompile(`^workspace-sources/v1/iso_[0-9a-z]{20,32}/wsp_[0-9a-z]{20,32}/src_[0-9a-z]{20,32}/[0-9a-f]{64}$`)

// WorkspaceSourceStore has no list, overwrite or delete authority. The storage
// profile digest prevents an installation retry from selecting a new bucket.
type WorkspaceSourceStore struct {
	store         *Store
	profileDigest string
}

func NewWorkspaceSourceStore(store *Store) (*WorkspaceSourceStore, error) {
	if store == nil {
		return nil, workspace.ErrSourceInvalid
	}
	profile := struct {
		SchemaVersion   string          `json:"schemaVersion"`
		Endpoint        string          `json:"endpoint"`
		Bucket          string          `json:"bucket"`
		AddressingStyle AddressingStyle `json:"addressingStyle"`
	}{"dataground.workspace-source-storage/v1", store.endpoint.String(), store.bucket, store.style}
	data, err := json.Marshal(profile)
	if err != nil {
		return nil, workspace.ErrSourceInvalid
	}
	sum := sha256.Sum256(data)
	return &WorkspaceSourceStore{store: store, profileDigest: "sha256:" + hex.EncodeToString(sum[:])}, nil
}
func (s *WorkspaceSourceStore) WorkspaceSourceStorageDigest() string { return s.profileDigest }
func (s *WorkspaceSourceStore) OpenWorkspaceSource(ctx context.Context, key string) (io.ReadCloser, error) {
	if !workspaceSourceKeyPattern.MatchString(key) {
		return nil, workspace.ErrSourceInvalid
	}
	address, err := s.store.objectURL(key, "workspace-sources/v1/")
	if err != nil {
		return nil, workspace.ErrSourceInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, workspace.ErrSourceUnavailable
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := s.store.client.Do(request)
	if err != nil {
		return nil, workspace.ErrSourceUnavailable
	}
	if response.StatusCode == http.StatusOK && response.Header.Get("Content-Encoding") == "" && response.ContentLength <= workspace.MaximumArchiveBytes {
		return response.Body, nil
	}
	closeResponse(response)
	if response.StatusCode == http.StatusNotFound {
		return nil, workspace.ErrSourceObjectMissing
	}
	return nil, workspace.ErrSourceUnavailable
}
func (s *WorkspaceSourceStore) PutWorkspaceSourceIfAbsent(ctx context.Context, key string, content []byte, digest string) error {
	if !workspaceSourceKeyPattern.MatchString(key) || !digestPattern.MatchString(digest) || !strings.HasSuffix(key, "/"+strings.TrimPrefix(digest, "sha256:")) || len(content) < 1024 || len(content) > workspace.MaximumArchiveBytes {
		return workspace.ErrSourceInvalid
	}
	owned := bytes.Clone(content)
	defer clear(owned)
	sum := sha256.Sum256(owned)
	if "sha256:"+hex.EncodeToString(sum[:]) != digest {
		return workspace.ErrSourceInvalid
	}
	address, err := s.store.objectURL(key, "workspace-sources/v1/")
	if err != nil {
		return workspace.ErrSourceInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, address, bytes.NewReader(owned))
	if err != nil {
		return workspace.ErrSourceUnavailable
	}
	request.ContentLength = int64(len(owned))
	request.Header.Set("Content-Type", "application/x-tar")
	request.Header.Set("If-None-Match", "*")
	request.Header.Set("x-amz-checksum-sha256", base64.StdEncoding.EncodeToString(sum[:]))
	response, err := s.store.client.Do(request)
	if err != nil {
		return workspace.ErrSourceUnavailable
	}
	closeResponse(response)
	if response.StatusCode == http.StatusOK {
		return nil
	}
	if response.StatusCode == http.StatusPreconditionFailed {
		return workspace.ErrSourceConflict
	}
	return workspace.ErrSourceUnavailable
}
