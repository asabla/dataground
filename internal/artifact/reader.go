package artifact

import (
	"context"
	"errors"
)

// ReadCatalog exposes only the immutable invocation binding. The caller must
// authorize content access independently of metadata access before calling Read.
type ReadCatalog interface {
	GetInvocationArtifactRecord(context.Context, string, string) (Record, error)
}

type Reader struct {
	catalog      ReadCatalog
	objects      ObjectReader
	maximumBytes int64
}

// Content must not be serialized into diagnostics or event payloads.
type Content struct {
	Record Record `json:"-"`
	Bytes  []byte `json:"-"`
}

func NewReader(catalog ReadCatalog, objects ObjectReader, maximumBytes int64) (*Reader, error) {
	if dependencyMissing(catalog) || dependencyMissing(objects) || maximumBytes <= 0 || maximumBytes == int64(^uint64(0)>>1) {
		return nil, errors.New("artifact read dependencies and bounded maximum are required")
	}
	return &Reader{catalog: catalog, objects: objects, maximumBytes: maximumBytes}, nil
}

// Read verifies the complete bounded object before returning any content.
// A catalog row for another invocation is indistinguishable from a missing row.
func (reader *Reader) Read(ctx context.Context, domainID, invocationID, artifactID string) (Content, error) {
	if err := ctx.Err(); err != nil {
		return Content{}, err
	}
	if reader == nil {
		return Content{}, ErrInvocationArtifactUnavailable
	}
	if !isolationDomainPattern.MatchString(domainID) || !invocationPattern.MatchString(invocationID) || !artifactPattern.MatchString(artifactID) {
		return Content{}, ErrInvocationArtifactInvalid
	}
	record, err := reader.catalog.GetInvocationArtifactRecord(ctx, domainID, artifactID)
	if err != nil {
		if ctx.Err() != nil {
			return Content{}, ctx.Err()
		}
		if errors.Is(err, ErrInvocationArtifactMissing) {
			return Content{}, ErrInvocationArtifactMissing
		}
		return Content{}, ErrInvocationArtifactUnavailable
	}
	if record.IsolationDomainID != domainID || record.InvocationID != invocationID || record.ID != artifactID {
		return Content{}, ErrInvocationArtifactMissing
	}
	record, err = NormalizeRecord(record)
	if err != nil || record.SizeBytes > reader.maximumBytes {
		return Content{}, ErrInvocationArtifactUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Content{}, err
	}
	content, err := readObject(ctx, reader.objects, record, record.SizeBytes)
	if err != nil {
		return Content{}, err
	}
	if err := ctx.Err(); err != nil {
		clear(content)
		return Content{}, err
	}
	current, err := reader.catalog.GetInvocationArtifactRecord(ctx, domainID, artifactID)
	if err == nil {
		current, err = NormalizeRecord(current)
	}
	if err != nil || !EqualRecords(record, current) || ctx.Err() != nil {
		clear(content)
		if ctx.Err() != nil {
			return Content{}, ctx.Err()
		}
		return Content{}, ErrInvocationArtifactUnavailable
	}
	return Content{Record: record, Bytes: content}, nil
}
