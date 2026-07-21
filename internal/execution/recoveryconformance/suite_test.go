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
	"sync/atomic"
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
	mutex     sync.Mutex
	available bool
	record    execution.EnforcementBundleRecord
	audits    int
}

func (catalog *memoryCatalog) GetEnforcementBundleRecord(
	ctx context.Context,
	isolationDomainID string,
	bundleID string,
) (execution.EnforcementBundleRecord, error) {
	catalog.mutex.Lock()
	defer catalog.mutex.Unlock()
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
	catalog.mutex.Lock()
	defer catalog.mutex.Unlock()
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
	catalog.mutex.Lock()
	defer catalog.mutex.Unlock()
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

	catalog.mutex.Lock()
	catalog.available = true
	catalog.mutex.Unlock()
	recover, err := RunRecover(context.Background(), catalog, backend, Config{RunID: testRunID})
	if err != nil || recover.Status != "passed" || recover.Phase != PhaseRecover {
		t.Fatalf("recover report = %#v, error = %v", recover, err)
	}
	if got, want := caseNames(recover), []string{
		"retained-object-present",
		"concurrent-catalog-adoption-after-restarts",
		"read-only-replay",
		"single-audit-commit",
	}; !equalStrings(got, want) {
		t.Fatalf("recover cases = %#v, want %#v", got, want)
	}
	if catalog.record.SchemaVersion == "" || catalog.audits != 1 || backend.writes != 1+ConcurrentRecoveryWorkers {
		t.Fatalf("recover effects = record %#v, audits %d, writes %d", catalog.record, catalog.audits, backend.writes)
	}
}

func TestOutageRequiresUnavailableObjectBackendAndNoCatalogEffect(t *testing.T) {
	backend := newMemoryBackend()
	catalog := &memoryCatalog{available: true}
	report, err := RunOutage(context.Background(), catalog, unavailableBackend{Backend: backend}, Config{RunID: testRunID})
	if err != nil || report.Status != "passed" || report.Phase != PhaseOutage {
		t.Fatalf("outage report = %#v, error = %v", report, err)
	}
	if got, want := caseNames(report), []string{
		"finalization-fails-closed-during-object-outage",
		"catalog-remains-unbound",
	}; !equalStrings(got, want) {
		t.Fatalf("outage cases = %#v, want %#v", got, want)
	}
	if catalog.record.SchemaVersion != "" || catalog.audits != 0 || backend.writes != 0 {
		t.Fatalf("outage effects = record %#v, audits %d, writes %d", catalog.record, catalog.audits, backend.writes)
	}
}

func TestOutageRejectsAvailableMissingOrAlreadyBoundScope(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		backend  Backend
		catalog  *memoryCatalog
		wantCase string
	}{
		"available object": {
			backend:  &memoryBackend{objects: map[string][]byte{fixture.Record.ObjectKey: bytes.Clone(fixture.Content)}},
			catalog:  &memoryCatalog{available: true},
			wantCase: "finalization-fails-closed-during-object-outage",
		},
		"missing object": {
			backend:  newMemoryBackend(),
			catalog:  &memoryCatalog{available: true},
			wantCase: "finalization-fails-closed-during-object-outage",
		},
		"bound catalog": {
			backend:  unavailableBackend{Backend: newMemoryBackend()},
			catalog:  &memoryCatalog{available: true, record: fixture.Record, audits: 1},
			wantCase: "catalog-remains-unbound",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			report, err := RunOutage(context.Background(), test.catalog, test.backend, Config{RunID: testRunID})
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || suiteErr.Case != test.wantCase || report.Status != "failed" {
				t.Fatalf("report = %#v, error = %v", report, err)
			}
		})
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
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	prepareCatalog := &memoryCatalog{available: true}
	prepare, err := RunPrepare(
		context.Background(), prepareCatalog, newMemoryBackend(), func(context.Context, Fixture) error { return nil },
		func() { prepareCatalog.available = false }, Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	outage, err := RunOutage(
		context.Background(),
		&memoryCatalog{available: true},
		unavailableBackend{Backend: newMemoryBackend()},
		Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	recoverBackend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	recover, err := RunRecover(
		context.Background(), &memoryCatalog{available: true}, recoverBackend, Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	committedCatalog := &memoryCatalog{available: true, record: fixture.Record, audits: 1}
	committed, err := RunCommittedRecover(
		context.Background(), committedCatalog, recoverBackend, Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	connectionLossCatalog := &ambiguousCommitCatalog{Catalog: &memoryCatalog{available: true}}
	connectionLoss, err := RunCommitConnectionLoss(
		context.Background(),
		connectionLossCatalog,
		recoverBackend,
		func(context.Context) error { return nil },
		Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	connectionRecovered, err := RunConnectionLossRecover(
		context.Background(), connectionLossCatalog.Catalog, recoverBackend, Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	rolledBackCatalog := &unavailableCatalog{Catalog: &memoryCatalog{available: true}}
	preCommitLoss, err := RunPreCommitConnectionLoss(
		context.Background(),
		rolledBackCatalog,
		recoverBackend,
		func(context.Context) error { return nil },
		Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	rolledBack, err := RunRolledBackRecover(
		context.Background(), rolledBackCatalog.Catalog, recoverBackend, Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	failoverCatalog := &memoryCatalog{available: true}
	failoverBackend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	failover, err := RunFailoverRecover(
		context.Background(),
		failoverCatalog,
		failoverBackend,
		func(context.Context, Fixture) error { return nil },
		Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	inFlightCatalog := &ambiguousCommitCatalog{Catalog: &memoryCatalog{available: true}}
	inFlight, err := RunFailoverCommitLoss(
		context.Background(), inFlightCatalog, failoverBackend,
		func(context.Context) error { return nil }, func() error { return nil },
		Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	inFlightRecovered, err := RunFailoverCommitRecover(
		context.Background(), inFlightCatalog.Catalog, failoverBackend,
		func(context.Context, Fixture) error { return nil }, Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	rejoined, err := RunFailoverRejoinObserve(
		context.Background(), inFlightCatalog.Catalog, failoverBackend,
		func(context.Context, Fixture) error { return nil }, Config{RunID: testRunID},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range []Report{
		prepare,
		outage,
		recover,
		committed,
		connectionLoss,
		connectionRecovered,
		preCommitLoss,
		rolledBack,
		failover,
		inFlight,
		inFlightRecovered,
		rejoined,
	} {
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{
			"endpoint",
			"bucket",
			"objectKey",
			"enforcement/",
			"sha256:",
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

func TestCancellationDuringConcurrentRecoveryIsPreserved(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	catalog := &cancelAfterInitialReadCatalog{
		Catalog: &memoryCatalog{available: true},
		cancel:  cancel,
	}
	backend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	report, err := RunRecover(ctx, catalog, backend, Config{RunID: testRunID})
	if !errors.Is(err, context.Canceled) || len(report.Cases) != 2 ||
		report.Cases[1] != (CaseResult{Name: "concurrent-catalog-adoption-after-restarts", Status: "failed"}) {
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

type unavailableBackend struct {
	Backend
}

func (unavailableBackend) OpenEnforcementObject(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("backend unavailable")
}

func (unavailableBackend) PutEnforcementObjectIfAbsent(context.Context, string, io.Reader, int64, string) error {
	return errors.New("backend unavailable")
}

var _ Backend = unavailableBackend{}

type cancelAfterInitialReadCatalog struct {
	Catalog
	cancel context.CancelFunc
	reads  atomic.Int64
}

func (catalog *cancelAfterInitialReadCatalog) GetEnforcementBundleRecord(
	ctx context.Context,
	isolationDomainID string,
	bundleID string,
) (execution.EnforcementBundleRecord, error) {
	record, err := catalog.Catalog.GetEnforcementBundleRecord(ctx, isolationDomainID, bundleID)
	if catalog.reads.Add(1) == 2 {
		catalog.cancel()
	}
	return record, err
}

var _ Catalog = (*cancelAfterInitialReadCatalog)(nil)
