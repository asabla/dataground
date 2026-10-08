package artifact

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestReaderRequiresExactBindingAndVerifiedObject(t *testing.T) {
	for _, name := range []string{"valid", "empty", "missing", "domain", "invocation", "artifact", "key", "oversize", "digest", "truncated", "appended", "object missing", "deleted", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			data := []byte("sensitive result")
			if name == "empty" {
				data = nil
			}
			value := artifactFinalization(data)
			record := value.Binding.Record
			store := newMemoryStore()
			store.objects[record.ObjectKey] = data
			switch name {
			case "domain":
				record.IsolationDomainID = "iso_11111111111111111111"
			case "invocation":
				record.InvocationID = "inv_11111111111111111111"
			case "artifact":
				record.ID = "art_11111111111111111111"
			case "key":
				record.ObjectKey = "foreign/path"
			case "oversize":
				record.SizeBytes = 101
			case "digest":
				store.objects[record.ObjectKey] = []byte("different result")
			case "truncated":
				store.objects[record.ObjectKey] = data[:3]
			case "appended":
				store.objects[record.ObjectKey] = append(bytes.Clone(data), 'x')
			case "object missing":
				delete(store.objects, record.ObjectKey)
			}
			original := value.Binding.Record
			if name != "missing" {
				store.records[original.ID] = record
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			objects := readerObjectFunc(func(ctx context.Context, key string) (io.ReadCloser, error) {
				if name == "deleted" {
					delete(store.records, original.ID)
				}
				if name == "cancelled" {
					cancel()
				}
				return store.OpenInvocationArtifactObject(ctx, key)
			})
			reader, err := NewReader(store, objects, 100)
			if err != nil {
				t.Fatal(err)
			}
			result, err := reader.Read(ctx, original.IsolationDomainID, original.InvocationID, original.ID)
			if name == "valid" || name == "empty" {
				if err != nil || !bytes.Equal(result.Bytes, data) || result.Record != original {
					t.Fatalf("read failed: %v", err)
				}
			} else if err == nil || len(result.Bytes) != 0 {
				t.Fatal("invalid artifact disclosed content")
			}
			if name == "domain" || name == "invocation" || name == "artifact" || name == "missing" || name == "key" || name == "oversize" {
				if len(store.actions) != 0 {
					t.Fatal("invalid binding reached object storage")
				}
			}
			if store.writes != 0 || store.binds != 0 {
				t.Fatal("read changed storage")
			}
		})
	}
}

type readerObjectFunc func(context.Context, string) (io.ReadCloser, error)

func (f readerObjectFunc) OpenInvocationArtifactObject(ctx context.Context, key string) (io.ReadCloser, error) {
	return f(ctx, key)
}

func TestReaderRejectsMalformedReferencesAndMissingDependencies(t *testing.T) {
	store := newMemoryStore()
	var missing *memoryStore
	for _, catalog := range []ReadCatalog{nil, missing} {
		if _, err := NewReader(catalog, store, 1); err == nil {
			t.Fatal("missing catalog accepted")
		}
	}
	if _, err := NewReader(store, missing, 1); err == nil {
		t.Fatal("missing objects accepted")
	}
	for _, size := range []int64{0, -1, int64(^uint64(0) >> 1)} {
		if _, err := NewReader(store, store, size); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	reader, _ := NewReader(store, store, 100)
	if _, err := reader.Read(context.Background(), "bad", "bad", "bad"); !errors.Is(err, ErrInvocationArtifactInvalid) {
		t.Fatalf("invalid path: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.Read(ctx, "bad", "bad", "bad"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestReaderWithholdsContentAfterReadOrCloseFailure(t *testing.T) {
	for _, failRead := range []bool{false, true} {
		store := newMemoryStore()
		value := artifactFinalization([]byte("sensitive"))
		record := value.Binding.Record
		store.records[record.ID] = record
		body := &readerFailedBody{Reader: bytes.NewReader(value.Content), failRead: failRead}
		objects := readerObjectFunc(func(context.Context, string) (io.ReadCloser, error) { return body, nil })
		reader, err := NewReader(store, objects, 100)
		if err != nil {
			t.Fatal(err)
		}
		content, err := reader.Read(context.Background(), record.IsolationDomainID, record.InvocationID, record.ID)
		if err == nil || len(content.Bytes) != 0 || !body.closed {
			t.Fatal("failed object read disclosed content or leaked its body")
		}
	}
}

type readerFailedBody struct {
	*bytes.Reader
	failRead bool
	closed   bool
}

func (body *readerFailedBody) Read(p []byte) (int, error) {
	n, err := body.Reader.Read(p)
	if body.failRead {
		return n, errors.New("private upstream error")
	}
	return n, err
}
func (body *readerFailedBody) Close() error {
	body.closed = true
	return errors.New("private close error")
}
