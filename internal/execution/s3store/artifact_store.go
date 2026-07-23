package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/asabla/dataground/internal/artifact"
)

const invocationArtifactKeyPrefix = "invocation-artifacts/v1/"

var invocationArtifactObjectKeyPattern = regexp.MustCompile(
	"^invocation-artifacts/v1/iso_[0-9a-z]{20,32}/inv_[0-9a-z]{20,32}/art_[0-9a-z]{20,32}/[0-9a-f]{64}$",
)

func (store *Store) OpenInvocationArtifactObject(
	ctx context.Context,
	key string,
) (io.ReadCloser, error) {
	objectURL, err := store.objectURL(key, invocationArtifactKeyPrefix)
	if err != nil || !invocationArtifactObjectKeyPattern.MatchString(key) {
		return nil, artifact.ErrInvocationArtifactObjectConflict
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, objectURL, nil)
	if err != nil {
		return nil, artifact.ErrInvocationArtifactUnavailable
	}
	request.Header.Set("Accept-Encoding", "identity")

	response, err := store.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, artifact.ErrInvocationArtifactUnavailable
	}
	if response.StatusCode == http.StatusOK {
		if response.Header.Get("Content-Encoding") != "" ||
			response.ContentLength > artifact.MaximumInvocationArtifactBytes {
			closeResponse(response)
			return nil, artifact.ErrInvocationArtifactUnavailable
		}
		return response.Body, nil
	}
	closeResponse(response)
	if response.StatusCode == http.StatusNotFound {
		return nil, artifact.ErrInvocationArtifactObjectMissing
	}
	return nil, artifact.ErrInvocationArtifactUnavailable
}

func (store *Store) PutInvocationArtifactObjectIfAbsent(
	ctx context.Context,
	key string,
	content io.Reader,
	size int64,
	digest string,
	mediaType string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	objectURL, err := store.objectURL(key, invocationArtifactKeyPrefix)
	if err != nil || !invocationArtifactObjectKeyPattern.MatchString(key) ||
		content == nil || size < 0 ||
		size > artifact.MaximumInvocationArtifactBytes ||
		!validArtifactMediaType(mediaType) {
		return artifact.ErrInvocationArtifactObjectConflict
	}
	owned, err := io.ReadAll(io.LimitReader(content, size+1))
	if err != nil || int64(len(owned)) != size || artifactDigest(owned) != digest {
		return artifact.ErrInvocationArtifactObjectConflict
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPut, objectURL, bytes.NewReader(owned))
	if err != nil {
		return artifact.ErrInvocationArtifactUnavailable
	}
	request.ContentLength = size
	request.Header.Set("Content-Type", mediaType)
	request.Header.Set("If-None-Match", "*")
	checksum := sha256.Sum256(owned)
	request.Header.Set("x-amz-checksum-sha256", base64.StdEncoding.EncodeToString(checksum[:]))

	response, err := store.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return artifact.ErrInvocationArtifactUnavailable
	}
	closeResponse(response)
	if response.StatusCode == http.StatusOK {
		return nil
	}
	if response.StatusCode == http.StatusPreconditionFailed {
		return artifact.ErrInvocationArtifactObjectConflict
	}
	return artifact.ErrInvocationArtifactUnavailable
}

func validArtifactMediaType(value string) bool {
	if value == "" || len(value) > 255 || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value ||
		strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	_, _, err := mime.ParseMediaType(value)
	return err == nil
}

func artifactDigest(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}

var _ artifact.ObjectReader = (*Store)(nil)
var _ artifact.ObjectWriter = (*Store)(nil)
