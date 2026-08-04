package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/asabla/dataground/internal/audittransport"
)

const auditExportKeyPrefix = "audit-export-deliveries/v1/"

var auditExportObjectKeyPattern = regexp.MustCompile(
	`^audit-export-deliveries/v1/iso_[0-9a-z]{20,32}/adl_[0-9a-z]{20,32}/[0-9a-f]{64}\.json$`,
)

type AuditExportStore struct {
	store *Store
}

func NewAuditExportStore(store *Store) (*AuditExportStore, error) {
	if store == nil {
		return nil, audittransport.ErrObjectConflict
	}
	return &AuditExportStore{store: store}, nil
}

func (store *AuditExportStore) OpenAuditExportObject(
	ctx context.Context,
	key string,
) (io.ReadCloser, error) {
	objectURL, err := store.store.objectURL(key, auditExportKeyPrefix)
	if err != nil || !auditExportObjectKeyPattern.MatchString(key) {
		return nil, audittransport.ErrObjectConflict
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, objectURL, nil)
	if err != nil {
		return nil, audittransport.ErrObjectUnavailable
	}
	request.Header.Set("Accept-Encoding", "identity")
	response, err := store.store.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, audittransport.ErrObjectUnavailable
	}
	if response.StatusCode == http.StatusOK {
		if response.Header.Get("Content-Encoding") != "" ||
			response.ContentLength > audittransport.MaximumPackageBytes {
			closeResponse(response)
			return nil, audittransport.ErrObjectUnavailable
		}
		return response.Body, nil
	}
	closeResponse(response)
	if response.StatusCode == http.StatusNotFound {
		return nil, audittransport.ErrObjectMissing
	}
	return nil, audittransport.ErrObjectUnavailable
}

func (store *AuditExportStore) PutAuditExportObjectIfAbsent(
	ctx context.Context,
	key string,
	content io.Reader,
	size int64,
	digest [sha256.Size]byte,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	objectURL, err := store.store.objectURL(key, auditExportKeyPrefix)
	if err != nil || !auditExportObjectKeyPattern.MatchString(key) || content == nil ||
		size <= 0 || size > audittransport.MaximumPackageBytes ||
		!strings.HasSuffix(key, "/"+hex.EncodeToString(digest[:])+".json") {
		return audittransport.ErrObjectConflict
	}
	owned, err := io.ReadAll(io.LimitReader(content, size+1))
	if err != nil || int64(len(owned)) != size || sha256.Sum256(owned) != digest {
		clear(owned)
		return audittransport.ErrObjectConflict
	}
	defer clear(owned)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, objectURL, bytes.NewReader(owned))
	if err != nil {
		return audittransport.ErrObjectUnavailable
	}
	request.ContentLength = size
	request.Header.Set("Content-Type", audittransport.PackageMediaType)
	request.Header.Set("If-None-Match", "*")
	request.Header.Set("x-amz-checksum-sha256", base64.StdEncoding.EncodeToString(digest[:]))
	response, err := store.store.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return audittransport.ErrObjectUnavailable
	}
	closeResponse(response)
	if response.StatusCode == http.StatusOK {
		return nil
	}
	if response.StatusCode == http.StatusPreconditionFailed {
		return audittransport.ErrObjectConflict
	}
	return audittransport.ErrObjectUnavailable
}

var _ audittransport.ObjectStore = (*AuditExportStore)(nil)
