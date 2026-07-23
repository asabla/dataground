package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
)

func TestFinalizerPersistsBeforeBindingAndReplaysReadOnly(t *testing.T) {
	store := newMemoryStore()
	finalizer, err := newTestFinalizer(store)
	if err != nil {
		t.Fatal(err)
	}
	value := artifactFinalization([]byte("durable result"))

	first, err := finalizer.Finalize(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	second, err := finalizer.Finalize(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	if !EqualRecords(first, second) || store.writes != 1 || store.binds != 1 {
		t.Fatalf("artifact replay = (%#v, writes %d, binds %d)", second, store.writes, store.binds)
	}
	if len(store.actions) != 4 ||
		store.actions[0] != "write" ||
		store.actions[1] != "read" ||
		store.actions[2] != "bind" ||
		store.actions[3] != "read" {
		t.Fatalf("artifact finalization order = %v", store.actions)
	}
}

func TestFinalizerRecoversLostWriteAcknowledgement(t *testing.T) {
	store := newMemoryStore()
	store.writeErr = ErrInvocationArtifactUnavailable
	finalizer, err := newTestFinalizer(store)
	if err != nil {
		t.Fatal(err)
	}
	value := artifactFinalization([]byte("lost acknowledgement"))
	record, err := finalizer.Finalize(context.Background(), value)
	if err != nil || !EqualRecords(record, value.Binding.Record) || store.binds != 1 {
		t.Fatalf("lost acknowledgement = (%#v, %v, binds %d)", record, err, store.binds)
	}
}

func TestFinalizerRejectsConflictingContent(t *testing.T) {
	store := newMemoryStore()
	value := artifactFinalization([]byte("expected"))
	store.objects[value.Binding.Record.ObjectKey] = []byte("different")
	store.records[value.Binding.Record.ID] = value.Binding.Record
	finalizer, err := newTestFinalizer(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finalizer.Finalize(context.Background(), value); !errors.Is(
		err,
		ErrInvocationArtifactConflict,
	) {
		t.Fatalf("conflicting artifact = %v", err)
	}
	if store.writes != 0 || store.binds != 0 {
		t.Fatalf("conflicting artifact performed writes = (%d, %d)", store.writes, store.binds)
	}
}

func TestFinalizerRejectsInvalidInputsBeforeStorage(t *testing.T) {
	tests := map[string]func(*Finalization){
		"digest mismatch": func(value *Finalization) {
			value.Content = []byte("changed")
		},
		"cross-domain key": func(value *Finalization) {
			value.Binding.Record.ObjectKey = "invocation-artifacts/v1/iso_other/value"
		},
		"missing lease": func(value *Finalization) {
			value.Binding.LeaseOwner = ""
		},
		"oversized metadata": func(value *Finalization) {
			value.Binding.Record.SizeBytes = testMaximumArtifactBytes + 1
		},
		"wrong state machine": func(value *Finalization) {
			value.Binding.StateMachineVersion = 1
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			store := newMemoryStore()
			finalizer, err := newTestFinalizer(store)
			if err != nil {
				t.Fatal(err)
			}
			value := artifactFinalization([]byte("content"))
			mutate(&value)
			if _, err := finalizer.Finalize(context.Background(), value); !errors.Is(
				err,
				ErrInvocationArtifactInvalid,
			) {
				t.Fatalf("invalid artifact = %v", err)
			}
			if store.writes != 0 || store.binds != 0 || len(store.actions) != 0 {
				t.Fatalf("invalid artifact reached storage = %#v", store)
			}
		})
	}
}

func TestFinalizerRejectsTypedNilDependencies(t *testing.T) {
	var store *memoryStore
	if finalizer, err := NewFinalizer(
		store,
		store,
		store,
		FinalizerConfig{MaximumBytes: testMaximumArtifactBytes},
	); finalizer != nil || err == nil {
		t.Fatalf("typed nil dependencies = (%#v, %v)", finalizer, err)
	}
}

func TestFinalizerRequiresBoundedConfiguration(t *testing.T) {
	store := newMemoryStore()
	if finalizer, err := NewFinalizer(
		store,
		store,
		store,
		FinalizerConfig{},
	); finalizer != nil || err == nil {
		t.Fatalf("unbounded finalizer = (%#v, %v)", finalizer, err)
	}
}

const testMaximumArtifactBytes = 1 << 20

func newTestFinalizer(store *memoryStore) (*Finalizer, error) {
	return NewFinalizer(
		store,
		store,
		store,
		FinalizerConfig{MaximumBytes: testMaximumArtifactBytes},
	)
}

func artifactFinalization(content []byte) Finalization {
	digest := sha256.Sum256(content)
	record := Record{
		SchemaVersion:     InvocationArtifactSchemaV1,
		IsolationDomainID: "iso_0123456789abcdefghij",
		ID:                "art_0123456789abcdefghij",
		InvocationID:      "inv_0123456789abcdefghij",
		OperationID:       "op_0123456789abcdefghij",
		EffectID:          "eff_0123456789abcdefghij",
		Name:              "result.json",
		Kind:              "file",
		MediaType:         "application/json",
		SizeBytes:         int64(len(content)),
		Digest:            "sha256:" + hex.EncodeToString(digest[:]),
		Sensitive:         true,
	}
	record.ObjectKey = ObjectKey(record)
	return Finalization{
		Binding: Binding{
			Record:              record,
			ActorID:             "runtime-worker",
			CorrelationID:       "correlation-1",
			LeaseOwner:          "worker-1",
			FencingToken:        4,
			StateMachineVersion: 2,
		},
		Content: content,
	}
}

type memoryStore struct {
	records  map[string]Record
	objects  map[string][]byte
	writes   int
	binds    int
	actions  []string
	writeErr error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		records: make(map[string]Record),
		objects: make(map[string][]byte),
	}
}

func (store *memoryStore) GetInvocationArtifactRecord(
	_ context.Context,
	_ string,
	artifactID string,
) (Record, error) {
	record, found := store.records[artifactID]
	if !found {
		return Record{}, ErrInvocationArtifactMissing
	}
	return record, nil
}

func (store *memoryStore) BindInvocationArtifact(
	_ context.Context,
	binding Binding,
) (Record, error) {
	if existing, found := store.records[binding.Record.ID]; found {
		if !EqualRecords(existing, binding.Record) {
			return Record{}, ErrInvocationArtifactConflict
		}
		return existing, nil
	}
	store.actions = append(store.actions, "bind")
	store.binds++
	store.records[binding.Record.ID] = binding.Record
	return binding.Record, nil
}

func (store *memoryStore) OpenInvocationArtifactObject(
	_ context.Context,
	key string,
) (io.ReadCloser, error) {
	content, found := store.objects[key]
	if !found {
		return nil, ErrInvocationArtifactObjectMissing
	}
	store.actions = append(store.actions, "read")
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (store *memoryStore) PutInvocationArtifactObjectIfAbsent(
	_ context.Context,
	key string,
	content io.Reader,
	_ int64,
	_ string,
	_ string,
) error {
	store.actions = append(store.actions, "write")
	store.writes++
	value, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	if existing, found := store.objects[key]; found && !bytes.Equal(existing, value) {
		return ErrInvocationArtifactObjectConflict
	}
	store.objects[key] = bytes.Clone(value)
	return store.writeErr
}
