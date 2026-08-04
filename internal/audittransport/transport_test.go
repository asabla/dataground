package audittransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"testing"
)

func TestExecuteRecoversAmbiguousWriteByExactReadBack(t *testing.T) {
	content := []byte("encrypted package")
	digest := sha256.Sum256(content)
	store := &memoryStore{writeErr: ErrObjectUnavailable}
	if err := Execute(context.Background(), store, "object", content, digest); err != nil {
		t.Fatal(err)
	}
	if store.puts != 1 || store.opens != 1 {
		t.Fatalf("puts = %d, opens = %d", store.puts, store.opens)
	}
}

func TestExecuteReplaysExactExistingObject(t *testing.T) {
	content := []byte("encrypted package")
	digest := sha256.Sum256(content)
	store := &memoryStore{content: bytes.Clone(content), writeErr: ErrObjectConflict}
	if err := Execute(context.Background(), store, "object", content, digest); err != nil {
		t.Fatal(err)
	}
	if store.puts != 1 || store.opens != 1 {
		t.Fatalf("puts = %d, opens = %d", store.puts, store.opens)
	}
}

func TestExecuteRejectsDifferentExistingObject(t *testing.T) {
	content := []byte("encrypted package")
	digest := sha256.Sum256(content)
	store := &memoryStore{content: []byte("different"), writeErr: ErrObjectConflict}
	if err := Execute(context.Background(), store, "object", content, digest); !errors.Is(err, ErrObjectConflict) {
		t.Fatalf("error = %v", err)
	}
	if !bytes.Equal(store.content, []byte("different")) {
		t.Fatal("conflicting object was replaced")
	}
}

func TestExecuteRejectsInvalidContentBeforeWrite(t *testing.T) {
	store := &memoryStore{}
	digest := sha256.Sum256([]byte("other"))
	if err := Execute(context.Background(), store, "object", []byte("package"), digest); !errors.Is(err, ErrObjectConflict) {
		t.Fatalf("error = %v", err)
	}
	if store.puts != 0 || store.opens != 0 {
		t.Fatal("invalid package reached the object store")
	}
}

type memoryStore struct {
	content  []byte
	writeErr error
	puts     int
	opens    int
}

func (store *memoryStore) PutAuditExportObjectIfAbsent(
	_ context.Context,
	_ string,
	content io.Reader,
	_ int64,
	_ [sha256.Size]byte,
) error {
	store.puts++
	if store.content == nil {
		store.content, _ = io.ReadAll(content)
	}
	return store.writeErr
}

func (store *memoryStore) OpenAuditExportObject(context.Context, string) (io.ReadCloser, error) {
	store.opens++
	if store.content == nil {
		return nil, ErrObjectMissing
	}
	return io.NopCloser(bytes.NewReader(store.content)), nil
}
