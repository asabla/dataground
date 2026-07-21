package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

type finalizerCatalogStub struct {
	record    EnforcementBundleRecord
	getRecord EnforcementBundleRecord
	getErr    error
	bindErr   error
	bindings  []EnforcementBundleBinding
}

func (catalog *finalizerCatalogStub) GetEnforcementBundleRecord(
	context.Context,
	string,
	string,
) (EnforcementBundleRecord, error) {
	if catalog.getErr != nil {
		return EnforcementBundleRecord{}, catalog.getErr
	}
	if catalog.getRecord.SchemaVersion != "" {
		return catalog.getRecord, nil
	}
	return EnforcementBundleRecord{}, ErrEnforcementBundleMissing
}

func (catalog *finalizerCatalogStub) BindEnforcementBundle(
	_ context.Context,
	binding EnforcementBundleBinding,
) (EnforcementBundleRecord, error) {
	catalog.bindings = append(catalog.bindings, binding)
	if catalog.bindErr != nil {
		return EnforcementBundleRecord{}, catalog.bindErr
	}
	if catalog.record.SchemaVersion != "" {
		catalog.getRecord = catalog.record
		return catalog.record, nil
	}
	catalog.getRecord = binding.Record
	return binding.Record, nil
}

type finalizerObjectStoreStub struct {
	content    []byte
	writeErr   error
	readErr    error
	closeErr   error
	writes     int
	writeKey   string
	writeSize  int64
	writeHash  string
	writeBytes []byte
}

func (store *finalizerObjectStoreStub) PutEnforcementObjectIfAbsent(
	_ context.Context,
	key string,
	content io.Reader,
	size int64,
	digest string,
) error {
	store.writes++
	store.writeKey = key
	store.writeSize = size
	store.writeHash = digest
	store.writeBytes, _ = io.ReadAll(content)
	if store.writeErr == nil && store.content == nil {
		store.content = bytes.Clone(store.writeBytes)
	}
	return store.writeErr
}

func (store *finalizerObjectStoreStub) OpenEnforcementObject(
	_ context.Context,
	_ string,
) (io.ReadCloser, error) {
	if store.readErr != nil {
		return nil, store.readErr
	}
	return &bundleReadCloser{Reader: bytes.NewReader(store.content), closeErr: store.closeErr}, nil
}

func TestEnforcementBundleFinalizerCreatesVerifiesAndBinds(t *testing.T) {
	content := []byte("version: 1\n")
	record := enforcementBundleFixture(content)
	catalog := &finalizerCatalogStub{}
	objects := &finalizerObjectStoreStub{}
	finalizer, err := NewEnforcementBundleFinalizer(catalog, objects, objects)
	if err != nil {
		t.Fatal(err)
	}

	bound, err := finalizer.Finalize(context.Background(), EnforcementBundleFinalization{
		Binding: EnforcementBundleBinding{Record: record, ActorID: "actor-1", CorrelationID: "correlation-1"},
		Content: content,
	})
	if err != nil {
		t.Fatalf("finalize bundle: %v", err)
	}
	if !EqualEnforcementBundleRecords(bound, record) || len(catalog.bindings) != 1 {
		t.Fatalf("unexpected binding: %#v, calls %d", bound, len(catalog.bindings))
	}
	if objects.writes != 1 || objects.writeKey != record.ObjectKey || objects.writeSize != record.SizeBytes ||
		objects.writeHash != record.Digest || !bytes.Equal(objects.writeBytes, content) {
		t.Fatalf("unexpected object write: %#v", objects)
	}
	content[0] = 'x'
	if objects.writeBytes[0] == 'x' || objects.content[0] == 'x' {
		t.Fatal("finalizer passed caller-owned content to object storage")
	}
}

func TestEnforcementBundleFinalizerRecoversAmbiguousWriteAndReplay(t *testing.T) {
	content := []byte("version: 1\n")
	record := enforcementBundleFixture(content)
	catalog := &finalizerCatalogStub{}
	objects := &finalizerObjectStoreStub{content: bytes.Clone(content), writeErr: errors.New("lost acknowledgement")}
	finalizer, err := NewEnforcementBundleFinalizer(catalog, objects, objects)
	if err != nil {
		t.Fatal(err)
	}
	request := EnforcementBundleFinalization{
		Binding: EnforcementBundleBinding{Record: record, ActorID: "actor-1", CorrelationID: "correlation-1"},
		Content: content,
	}
	for range 2 {
		if _, err := finalizer.Finalize(context.Background(), request); err != nil {
			t.Fatalf("recover finalization: %v", err)
		}
	}
	if objects.writes != 1 || len(catalog.bindings) != 1 {
		t.Fatalf("writes %d, bindings %d", objects.writes, len(catalog.bindings))
	}
}

func TestEnforcementBundleFinalizerPreflightsExistingMetadata(t *testing.T) {
	content := []byte("version: 1\n")
	record := enforcementBundleFixture(content)
	tests := map[string]struct {
		catalog *finalizerCatalogStub
		objects *finalizerObjectStoreStub
		want    error
	}{
		"exact replay": {
			catalog: &finalizerCatalogStub{getRecord: record},
			objects: &finalizerObjectStoreStub{content: bytes.Clone(content)},
		},
		"metadata conflict": {
			catalog: &finalizerCatalogStub{getRecord: func() EnforcementBundleRecord {
				other := record
				other.Provenance.CompilerVersion = "other"
				return other
			}()},
			objects: &finalizerObjectStoreStub{content: bytes.Clone(content)},
			want:    ErrEnforcementBundleConflict,
		},
		"catalog unavailable": {
			catalog: &finalizerCatalogStub{getErr: errors.New("database detail")},
			objects: &finalizerObjectStoreStub{},
			want:    ErrEnforcementBundleUnavailable,
		},
		"bound object unavailable": {
			catalog: &finalizerCatalogStub{getRecord: record},
			objects: &finalizerObjectStoreStub{readErr: ErrEnforcementObjectMissing},
			want:    ErrEnforcementBundleUnavailable,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			finalizer, err := NewEnforcementBundleFinalizer(test.catalog, test.objects, test.objects)
			if err != nil {
				t.Fatal(err)
			}
			bound, err := finalizer.Finalize(context.Background(), EnforcementBundleFinalization{
				Binding: EnforcementBundleBinding{Record: record, ActorID: "actor-1", CorrelationID: "correlation-1"},
				Content: content,
			})
			if test.want == nil {
				if err != nil || !EqualEnforcementBundleRecords(bound, record) {
					t.Fatalf("bound %#v, error %v", bound, err)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if test.objects.writes != 0 || len(test.catalog.bindings) != 0 {
				t.Fatalf("writes %d, bindings %d", test.objects.writes, len(test.catalog.bindings))
			}
		})
	}
}

func TestEnforcementBundleFinalizerRecoversCatalogFailureWithoutDeletingObject(t *testing.T) {
	content := []byte("version: 1\n")
	record := enforcementBundleFixture(content)
	catalog := &finalizerCatalogStub{bindErr: errors.New("database unavailable")}
	objects := &finalizerObjectStoreStub{}
	finalizer, err := NewEnforcementBundleFinalizer(catalog, objects, objects)
	if err != nil {
		t.Fatal(err)
	}
	request := EnforcementBundleFinalization{
		Binding: EnforcementBundleBinding{Record: record, ActorID: "actor-1", CorrelationID: "correlation-1"},
		Content: content,
	}
	if _, err := finalizer.Finalize(context.Background(), request); !errors.Is(err, ErrEnforcementBundleUnavailable) {
		t.Fatalf("first finalization error = %v", err)
	}
	if !bytes.Equal(objects.content, content) {
		t.Fatal("object was not retained after catalog failure")
	}
	catalog.bindErr = nil
	if _, err := finalizer.Finalize(context.Background(), request); err != nil {
		t.Fatalf("retry finalization: %v", err)
	}
	if objects.writes != 2 || len(catalog.bindings) != 2 {
		t.Fatalf("writes %d, bindings %d", objects.writes, len(catalog.bindings))
	}
}

func TestEnforcementBundleFinalizerFailsClosedBeforeBinding(t *testing.T) {
	content := []byte("version: 1\n")
	record := enforcementBundleFixture(content)
	tests := map[string]struct {
		mutate    func(*EnforcementBundleFinalization)
		objects   *finalizerObjectStoreStub
		want      error
		wantWrite bool
	}{
		"invalid metadata": {
			mutate:  func(request *EnforcementBundleFinalization) { request.Binding.Record.IsolationDomainID = "bad" },
			objects: &finalizerObjectStoreStub{},
		},
		"content digest mismatch": {
			mutate:  func(request *EnforcementBundleFinalization) { request.Content = []byte("changed") },
			objects: &finalizerObjectStoreStub{}, want: ErrEnforcementBundleMismatch,
		},
		"missing read back": {
			objects: &finalizerObjectStoreStub{readErr: ErrEnforcementObjectMissing},
			want:    ErrEnforcementBundleUnavailable, wantWrite: true,
		},
		"conflicting object": {
			objects: &finalizerObjectStoreStub{
				content: []byte("different"), writeErr: ErrEnforcementObjectConflict,
			},
			want: ErrEnforcementBundleConflict, wantWrite: true,
		},
		"corrupt read back": {
			objects: &finalizerObjectStoreStub{content: []byte("different")},
			want:    ErrEnforcementBundleConflict, wantWrite: true,
		},
		"read close failure": {
			objects: &finalizerObjectStoreStub{content: bytes.Clone(content), closeErr: errors.New("close")},
			want:    ErrEnforcementBundleUnavailable, wantWrite: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			catalog := &finalizerCatalogStub{}
			finalizer, err := NewEnforcementBundleFinalizer(catalog, test.objects, test.objects)
			if err != nil {
				t.Fatal(err)
			}
			request := EnforcementBundleFinalization{
				Binding: EnforcementBundleBinding{Record: record, ActorID: "actor-1", CorrelationID: "correlation-1"},
				Content: bytes.Clone(content),
			}
			if test.mutate != nil {
				test.mutate(&request)
			}
			_, err = finalizer.Finalize(context.Background(), request)
			if test.want == nil {
				if err == nil {
					t.Fatal("invalid request accepted")
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			wantWrites := 0
			if test.wantWrite {
				wantWrites = 1
			}
			if test.objects.writes != wantWrites || len(catalog.bindings) != 0 {
				t.Fatalf("writes %d, bindings %d", test.objects.writes, len(catalog.bindings))
			}
		})
	}
}

func TestEnforcementBundleFinalizerHandlesCatalogFailures(t *testing.T) {
	content := []byte("version: 1\n")
	record := enforcementBundleFixture(content)
	tests := map[string]struct {
		catalog *finalizerCatalogStub
		want    error
	}{
		"missing revision": {
			catalog: &finalizerCatalogStub{bindErr: ErrEnforcementBundleRevisionMissing},
			want:    ErrEnforcementBundleRevisionMissing,
		},
		"binding conflict": {
			catalog: &finalizerCatalogStub{bindErr: ErrEnforcementBundleConflict},
			want:    ErrEnforcementBundleConflict,
		},
		"catalog unavailable": {
			catalog: &finalizerCatalogStub{bindErr: errors.New("database detail")},
			want:    ErrEnforcementBundleUnavailable,
		},
		"catalog returned mismatch": {
			catalog: &finalizerCatalogStub{record: func() EnforcementBundleRecord {
				other := record
				other.Provenance.CompilerVersion = "other"
				return other
			}()},
			want: ErrEnforcementBundleConflict,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			objects := &finalizerObjectStoreStub{}
			finalizer, err := NewEnforcementBundleFinalizer(test.catalog, objects, objects)
			if err != nil {
				t.Fatal(err)
			}
			_, err = finalizer.Finalize(context.Background(), EnforcementBundleFinalization{
				Binding: EnforcementBundleBinding{Record: record, ActorID: "actor-1", CorrelationID: "correlation-1"},
				Content: content,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestEnforcementBundleFinalizerPreservesCancellation(t *testing.T) {
	content := []byte("version: 1\n")
	record := enforcementBundleFixture(content)
	catalog := &finalizerCatalogStub{}
	objects := &finalizerObjectStoreStub{content: bytes.Clone(content)}
	finalizer, err := NewEnforcementBundleFinalizer(catalog, objects, objects)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = finalizer.Finalize(ctx, EnforcementBundleFinalization{
		Binding: EnforcementBundleBinding{Record: record, ActorID: "actor-1", CorrelationID: "correlation-1"},
		Content: content,
	})
	if !errors.Is(err, context.Canceled) || len(catalog.bindings) != 0 {
		t.Fatalf("error %v, bindings %d", err, len(catalog.bindings))
	}
}

func TestEnforcementBundleFinalizationDoesNotSerializeContent(t *testing.T) {
	content := []byte("sensitive-policy-material")
	record := enforcementBundleFixture(content)
	encoded, err := json.Marshal(EnforcementBundleFinalization{
		Binding: EnforcementBundleBinding{Record: record, ActorID: "actor-1", CorrelationID: "correlation-1"},
		Content: content,
	})
	if err != nil {
		t.Fatalf("marshal finalization: %v", err)
	}
	if bytes.Contains(encoded, content) || bytes.Contains(encoded, []byte(record.ObjectKey)) {
		t.Fatalf("serialized finalization exposed protected content: %s", encoded)
	}
}

func TestNewEnforcementBundleFinalizerRequiresDependencies(t *testing.T) {
	catalog := &finalizerCatalogStub{}
	objects := &finalizerObjectStoreStub{}
	for name, construct := range map[string]func() error{
		"catalog": func() error {
			_, err := NewEnforcementBundleFinalizer(nil, objects, objects)
			return err
		},
		"reader": func() error {
			_, err := NewEnforcementBundleFinalizer(catalog, nil, objects)
			return err
		},
		"writer": func() error {
			_, err := NewEnforcementBundleFinalizer(catalog, objects, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := construct(); err == nil || !strings.Contains(err.Error(), "dependencies") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

var _ EnforcementBundleStore = (*finalizerCatalogStub)(nil)
var _ EnforcementObjectReader = (*finalizerObjectStoreStub)(nil)
var _ EnforcementObjectWriter = (*finalizerObjectStoreStub)(nil)
