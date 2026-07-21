package s3conformance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

const testRunID = "0123456789abcdef0123456789abcdef"

type memoryBackend struct {
	mutex     sync.Mutex
	objects   map[string][]byte
	overwrite bool
	writes    int
}

type cancelingBackend struct {
	cancel context.CancelFunc
}

type nilStreamBackend struct{}

func (backend *cancelingBackend) OpenEnforcementObject(context.Context, string) (io.ReadCloser, error) {
	backend.cancel()
	return nil, context.Canceled
}

func (*cancelingBackend) PutEnforcementObjectIfAbsent(context.Context, string, io.Reader, int64, string) error {
	return errors.New("unexpected write")
}

func (*nilStreamBackend) OpenEnforcementObject(context.Context, string) (io.ReadCloser, error) {
	return nil, nil
}

func (*nilStreamBackend) PutEnforcementObjectIfAbsent(context.Context, string, io.Reader, int64, string) error {
	return errors.New("unexpected write")
}

func newMemoryBackend() *memoryBackend {
	return &memoryBackend{objects: make(map[string][]byte)}
}

func (backend *memoryBackend) OpenEnforcementObject(_ context.Context, key string) (io.ReadCloser, error) {
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	content, found := backend.objects[key]
	if !found {
		return nil, execution.ErrEnforcementObjectMissing
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(content))), nil
}

func (backend *memoryBackend) PutEnforcementObjectIfAbsent(
	_ context.Context,
	key string,
	content io.Reader,
	size int64,
	digest string,
) error {
	owned, err := io.ReadAll(content)
	if err != nil || int64(len(owned)) != size {
		return errors.New("invalid content")
	}
	sum := sha256.Sum256(owned)
	if digest != "sha256:"+hex.EncodeToString(sum[:]) {
		return errors.New("invalid digest")
	}
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	backend.writes++
	if _, found := backend.objects[key]; found && !backend.overwrite {
		return execution.ErrEnforcementObjectConflict
	}
	backend.objects[key] = bytes.Clone(owned)
	return nil
}

func TestRunAcceptsAnAtomicImmutableBackend(t *testing.T) {
	backend := newMemoryBackend()
	report, err := Run(context.Background(), backend, Config{RunID: testRunID})
	if err != nil {
		t.Fatalf("run conformance: %v", err)
	}
	if report.SchemaVersion != ReportSchemaV1 || report.RunID != testRunID || report.Status != "passed" ||
		len(report.Cases) != 7 {
		t.Fatalf("unexpected report: %#v", report)
	}
	expectedCases := []string{
		"missing-read",
		"create-read",
		"immutable-rewrite",
		"concurrent-create",
		"finalizer-lost-ack",
		"finalizer-catalog-retry",
		"finalizer-conflict",
	}
	for index, result := range report.Cases {
		if result.Name != expectedCases[index] || result.Status != "passed" {
			t.Fatalf("case did not pass: %#v", result)
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"endpoint", "bucket", "objectKey", "version: 1"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report serialized forbidden detail %q: %s", forbidden, encoded)
		}
	}
}

func TestRunRejectsAReplacingBackend(t *testing.T) {
	backend := newMemoryBackend()
	backend.overwrite = true
	report, err := Run(context.Background(), backend, Config{RunID: testRunID})
	var suiteErr *SuiteError
	if !errors.As(err, &suiteErr) || suiteErr.Case != "immutable-rewrite" || report.Status != "failed" {
		t.Fatalf("report = %#v, error = %v", report, err)
	}
	if len(report.Cases) != 3 || report.Cases[2].Status != "failed" {
		t.Fatalf("unexpected failed cases: %#v", report.Cases)
	}
}

func TestRunValidatesInputsBeforeStorageEffects(t *testing.T) {
	tests := map[string]Config{
		"invalid run identifier": {RunID: "../shared"},
		"one writer":             {RunID: testRunID, ConcurrentWriters: 1},
		"too many writers":       {RunID: testRunID, ConcurrentWriters: 33},
	}
	for name, config := range tests {
		t.Run(name, func(t *testing.T) {
			backend := newMemoryBackend()
			if _, err := Run(context.Background(), backend, config); err == nil {
				t.Fatal("invalid conformance input accepted")
			}
			if backend.writes != 0 || len(backend.objects) != 0 {
				t.Fatal("invalid conformance input reached storage")
			}
		})
	}
}

func TestRunPreservesCancellationBeforeStorageEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backend := newMemoryBackend()
	if _, err := Run(ctx, backend, Config{RunID: testRunID}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if backend.writes != 0 || len(backend.objects) != 0 {
		t.Fatal("cancelled run reached storage")
	}
}

func TestRunPreservesCancellationDuringACase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	backend := &cancelingBackend{cancel: cancel}
	report, err := Run(ctx, backend, Config{RunID: testRunID})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if len(report.Cases) != 1 || report.Cases[0] != (CaseResult{Name: "missing-read", Status: "failed"}) {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestInvalidRunIdentifierIsNotReflectedInEvidence(t *testing.T) {
	report, err := Run(context.Background(), newMemoryBackend(), Config{RunID: "secret-or-unsafe-value"})
	if err == nil || report.RunID != "" {
		t.Fatalf("report = %#v, error = %v", report, err)
	}
}

func TestRunRejectsANilStreamWithoutPanicking(t *testing.T) {
	report, err := Run(context.Background(), &nilStreamBackend{}, Config{RunID: testRunID})
	var suiteErr *SuiteError
	if !errors.As(err, &suiteErr) || suiteErr.Case != "missing-read" || len(report.Cases) != 1 ||
		report.Cases[0].Status != "failed" {
		t.Fatalf("report = %#v, error = %v", report, err)
	}
}

func TestObjectKeysAreIsolatedAndStable(t *testing.T) {
	keys := []string{
		objectKey(testRunID, "missing"),
		objectKey(testRunID, "create-read"),
		objectKey(testRunID, "immutable-rewrite"),
		objectKey(testRunID, "concurrent-create"),
		finalizerRecord(testRunID, "finalizer-lost-ack", []byte("version: 1\n")).ObjectKey,
		finalizerRecord(testRunID, "finalizer-catalog-retry", []byte("version: 1\n")).ObjectKey,
		finalizerRecord(testRunID, "finalizer-conflict", []byte("version: 1\n")).ObjectKey,
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for index, key := range sorted {
		if index > 0 && key == sorted[index-1] {
			t.Fatalf("duplicate key %q", key)
		}
		if !strings.HasPrefix(key, "enforcement-bundles/v1/iso_") || len(key) > 1024 {
			t.Fatalf("invalid key %q", key)
		}
	}
	if objectKey(testRunID, "create-read") == objectKey("fedcba9876543210fedcba9876543210", "create-read") {
		t.Fatal("run identifiers share object scope")
	}
	if finalizerRecord(testRunID, "finalizer-lost-ack", []byte("version: 1\n")).ObjectKey ==
		finalizerRecord("fedcba9876543210fedcba9876543210", "finalizer-lost-ack", []byte("version: 1\n")).ObjectKey {
		t.Fatal("finalizer run identifiers share object scope")
	}
}

var _ Backend = (*memoryBackend)(nil)
var _ Backend = (*cancelingBackend)(nil)
var _ Backend = (*nilStreamBackend)(nil)
