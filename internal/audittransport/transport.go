package audittransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
)

const (
	MaximumPackageBytes = 100 << 20
	PackageMediaType    = "application/vnd.dataground.audit-export-encrypted-package+json;version=1"
)

var (
	ErrObjectMissing     = errors.New("audit export transport object is missing")
	ErrObjectConflict    = errors.New("audit export transport object conflicts with the delivery")
	ErrObjectUnavailable = errors.New("audit export transport object is unavailable")
)

type ObjectStore interface {
	OpenAuditExportObject(context.Context, string) (io.ReadCloser, error)
	PutAuditExportObjectIfAbsent(context.Context, string, io.Reader, int64, [sha256.Size]byte) error
}

// Execute installs one immutable encrypted package and verifies the exact
// read-back. A matching object resolves a lost or conflicting write response;
// a different object fails closed and is never replaced.
func Execute(
	ctx context.Context,
	store ObjectStore,
	objectKey string,
	content []byte,
	expected [sha256.Size]byte,
) error {
	if ctx == nil || store == nil || objectKey == "" || len(content) == 0 ||
		len(content) > MaximumPackageBytes || sha256.Sum256(content) != expected {
		return ErrObjectConflict
	}
	owned := bytes.Clone(content)
	defer clear(owned)
	writeErr := store.PutAuditExportObjectIfAbsent(
		ctx, objectKey, bytes.NewReader(owned), int64(len(owned)), expected,
	)
	if err := ctx.Err(); err != nil {
		return err
	}
	reader, readErr := store.OpenAuditExportObject(ctx, objectKey)
	if readErr != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		if errors.Is(readErr, ErrObjectConflict) || errors.Is(writeErr, ErrObjectConflict) {
			return ErrObjectConflict
		}
		return ErrObjectUnavailable
	}
	if reader == nil {
		return ErrObjectUnavailable
	}
	observed, bodyErr := io.ReadAll(io.LimitReader(reader, MaximumPackageBytes+1))
	closeErr := reader.Close()
	defer clear(observed)
	if bodyErr != nil || closeErr != nil || len(observed) == 0 ||
		len(observed) > MaximumPackageBytes {
		return ErrObjectUnavailable
	}
	if sha256.Sum256(observed) != expected || !bytes.Equal(observed, owned) {
		return ErrObjectConflict
	}
	return nil
}
