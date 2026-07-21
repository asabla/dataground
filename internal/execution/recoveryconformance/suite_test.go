package recoveryconformance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

const testRunID = "0123456789abcdef0123456789abcdef"

type memoryBackend struct {
	mutex   sync.Mutex
	objects map[string][]byte
	writes  int
}

type cancelingBackend struct {
	cancel context.CancelFunc
}

func (backend *cancelingBackend) OpenEnforcementObject(context.Context, string) (io.ReadCloser, error) {
	backend.cancel()
	return nil, context.Canceled
}

func (*cancelingBackend) PutEnforcementObjectIfAbsent(context.Context, string, io.Reader, int64, string) error {
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
		return errors.New("invalid object content")
	}
	sum := sha256.Sum256(owned)
	if digest != "sha256:"+hex.EncodeToString(sum[:]) {
		return errors.New("invalid object digest")
	}
	backend.mutex.Lock()
	defer backend.mutex.Unlock()
	backend.writes++
	if _, found := backend.objects[key]; found {
		return execution.ErrEnforcementObjectConflict
	}
	backend.objects[key] = bytes.Clone(owned)
	return nil
}

type memoryCatalog struct {
	available bool
	record    execution.EnforcementBundleRecord
	audits    int
}

func (catalog *memoryCatalog) GetEnforcementBundleRecord(
	ctx context.Context,
	isolationDomainID string,
	bundleID string,
) (execution.EnforcementBundleRecord, error) {
	if err := ctx.Err(); err != nil {
		return execution.EnforcementBundleRecord{}, err
	}
	if !catalog.available {
		return execution.EnforcementBundleRecord{}, errors.New("catalog unavailable")
	}
	if catalog.record.SchemaVersion == "" || catalog.record.IsolationDomainID != isolationDomainID ||
		catalog.record.ID != bundleID {
		return execution.EnforcementBundleRecord{}, execution.ErrEnforcementBundleMissing
	}
	return catalog.record, nil
}

func (catalog *memoryCatalog) BindEnforcementBundle(
	ctx context.Context,
	binding execution.EnforcementBundleBinding,
) (execution.EnforcementBundleRecord, error) {
	if err := ctx.Err(); err != nil {
		return execution.EnforcementBundleRecord{}, err
	}
	if !catalog.available {
		return execution.EnforcementBundleRecord{}, errors.New("catalog unavailable")
	}
	normalized, err := execution.NormalizeEnforcementBundleBinding(binding)
	if err != nil {
		return execution.EnforcementBundleRecord{}, err
	}
	if catalog.record.SchemaVersion != "" && !execution.EqualEnforcementBundleRecords(catalog.record, normalized.Record) {
		return execution.EnforcementBundleRecord{}, execution.ErrEnforcementBundleConflict
	}
	if catalog.record.SchemaVersion == "" {
		catalog.record = normalized.Record
		catalog.audits++
	}
	return catalog.record, nil
}

func (catalog *memoryCatalog) CountEnforcementBundleBindingAudits(
	ctx context.Context,
	isolationDomainID string,
	bundleID string,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if !catalog.available || (catalog.record.SchemaVersion != "" &&
		(catalog.record.IsolationDomainID != isolationDomainID || catalog.record.ID != bundleID)) {
		return 0, errors.New("catalog unavailable")
	}
	return catalog.audits, nil
}

func TestPrepareAndRecoverAcrossCatalogRestart(t *testing.T) {
	backend := newMemoryBackend()
	catalog := &memoryCatalog{available: true}
	prepare, err := RunPrepare(
		context.Background(),
		catalog,
		backend,
		func(context.Context, Fixture) error { return nil },
		func() { catalog.available = false },
		Config{RunID: testRunID},
	)
	if err != nil || prepare.Status != "passed" || prepare.Phase != PhasePrepare {
		t.Fatalf("prepare report = %#v, error = %v", prepare, err)
	}
	if got, want := caseNames(prepare), []string{
		"fresh-scope",
		"fixture-provisioned",
		"object-retained-after-database-loss",
	}; !equalStrings(got, want) {
		t.Fatalf("prepare cases = %#v, want %#v", got, want)
	}
	if catalog.record.SchemaVersion != "" || catalog.audits != 0 || backend.writes != 1 {
		t.Fatalf("prepare effects = record %#v, audits %d, writes %d", catalog.record, catalog.audits, backend.writes)
	}

	catalog.available = true
	recover, err := RunRecover(context.Background(), catalog, backend, Config{RunID: testRunID})
	if err != nil || recover.Status != "passed" || recover.Phase != PhaseRecover {
		t.Fatalf("recover report = %#v, error = %v", recover, err)
	}
	if got, want := caseNames(recover), []string{
		"retained-object-present",
		"catalog-adoption-after-restart",
		"read-only-replay",
		"single-audit-commit",
	}; !equalStrings(got, want) {
		t.Fatalf("recover cases = %#v, want %#v", got, want)
	}
	if catalog.record.SchemaVersion == "" || catalog.audits != 1 || backend.writes != 2 {
		t.Fatalf("recover effects = record %#v, audits %d, writes %d", catalog.record, catalog.audits, backend.writes)
	}
}

func TestPrepareRejectsReusedScopeWithoutChangingIt(t *testing.T) {
	backend := newMemoryBackend()
	catalog := &memoryCatalog{available: true}
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	backend.objects[fixture.Record.ObjectKey] = bytes.Clone(fixture.Content)
	disconnected := false
	provisioned := false
	report, err := RunPrepare(
		context.Background(),
		catalog,
		backend,
		func(context.Context, Fixture) error { provisioned = true; return nil },
		func() { disconnected = true },
		Config{RunID: testRunID},
	)
	var suiteErr *SuiteError
	if !errors.As(err, &suiteErr) || suiteErr.Case != "fresh-scope" || disconnected || backend.writes != 0 ||
		provisioned || len(report.Cases) != 1 || report.Cases[0].Status != "failed" {
		t.Fatalf("report = %#v, error = %v, disconnected = %t, writes = %d", report, err, disconnected, backend.writes)
	}
}

func TestPrepareStopsBeforeObjectWriteWhenFixtureProvisioningFails(t *testing.T) {
	backend := newMemoryBackend()
	disconnected := false
	report, err := RunPrepare(
		context.Background(),
		&memoryCatalog{available: true},
		backend,
		func(context.Context, Fixture) error { return errors.New("database detail") },
		func() { disconnected = true },
		Config{RunID: testRunID},
	)
	var suiteErr *SuiteError
	if !errors.As(err, &suiteErr) || suiteErr.Case != "fixture-provisioned" || disconnected || backend.writes != 0 ||
		len(report.Cases) != 2 || report.Cases[0].Status != "passed" || report.Cases[1].Status != "failed" {
		t.Fatalf("report = %#v, error = %v, disconnected = %t, writes = %d", report, err, disconnected, backend.writes)
	}
}

func TestRecoverRejectsMissingOrAlreadyBoundEffects(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		backend *memoryBackend
		catalog *memoryCatalog
	}{
		"missing object": {backend: newMemoryBackend(), catalog: &memoryCatalog{available: true}},
		"already bound": {
			backend: &memoryBackend{objects: map[string][]byte{fixture.Record.ObjectKey: bytes.Clone(fixture.Content)}},
			catalog: &memoryCatalog{available: true, record: fixture.Record, audits: 1},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			report, err := RunRecover(context.Background(), test.catalog, test.backend, Config{RunID: testRunID})
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || suiteErr.Case != "retained-object-present" || len(report.Cases) != 1 ||
				report.Cases[0].Status != "failed" {
				t.Fatalf("report = %#v, error = %v", report, err)
			}
		})
	}
}

func TestInvalidConfigurationFailsBeforeEffectsAndIsNotReflected(t *testing.T) {
	backend := newMemoryBackend()
	catalog := &memoryCatalog{available: true}
	report, err := RunPrepare(
		context.Background(), catalog, backend, func(context.Context, Fixture) error { return nil }, func() {},
		Config{RunID: "unsafe-value"},
	)
	if err == nil || report.RunID != "" || backend.writes != 0 || len(backend.objects) != 0 {
		t.Fatalf("report = %#v, error = %v, writes = %d", report, err, backend.writes)
	}
}

func TestReportsDoNotSerializeSensitiveRoutingOrContent(t *testing.T) {
	backend := newMemoryBackend()
	catalog := &memoryCatalog{available: true}
	report, err := RunPrepare(
		context.Background(), catalog, backend, func(context.Context, Fixture) error { return nil },
		func() { catalog.available = false }, Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"endpoint",
		"bucket",
		"objectKey",
		"postgres://",
		"127.0.0.1",
		"version: 1",
		"correlation",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report serialized forbidden detail %q: %s", forbidden, encoded)
		}
	}
}

func TestCancellationBeforeEffectsIsPreserved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	backend := newMemoryBackend()
	report, err := RunPrepare(
		ctx, &memoryCatalog{available: true}, backend, func(context.Context, Fixture) error { return nil }, func() {},
		Config{RunID: testRunID},
	)
	if !errors.Is(err, context.Canceled) || report.RunID != "" || backend.writes != 0 {
		t.Fatalf("report = %#v, error = %v, writes = %d", report, err, backend.writes)
	}
}

func TestCancellationDuringFreshScopeCheckIsPreserved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	backend := &cancelingBackend{cancel: cancel}
	report, err := RunPrepare(
		ctx, &memoryCatalog{available: true}, backend, func(context.Context, Fixture) error { return nil }, func() {},
		Config{RunID: testRunID},
	)
	if !errors.Is(err, context.Canceled) || len(report.Cases) != 1 ||
		report.Cases[0] != (CaseResult{Name: "fresh-scope", Status: "failed"}) {
		t.Fatalf("report = %#v, error = %v", report, err)
	}
}

func caseNames(report Report) []string {
	names := make([]string, 0, len(report.Cases))
	for _, result := range report.Cases {
		if result.Status != "passed" {
			return nil
		}
		names = append(names, result.Name)
	}
	return names
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

var _ Backend = (*memoryBackend)(nil)
var _ Backend = (*cancelingBackend)(nil)
var _ Catalog = (*memoryCatalog)(nil)
