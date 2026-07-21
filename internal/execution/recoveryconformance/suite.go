// Package recoveryconformance verifies that immutable enforcement-object and
// PostgreSQL catalog effects recover across outages, restarts and contention.
package recoveryconformance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"sync"
	"sync/atomic"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/identity"
)

const ReportSchemaV1 = "dataground.enforcement-recovery-conformance/v1"

const ConcurrentRecoveryWorkers = 8

type Phase string

const (
	PhasePrepare                 Phase = "prepare"
	PhaseOutage                  Phase = "outage"
	PhaseRecover                 Phase = "recover"
	PhaseCommitLoss              Phase = "commit-loss"
	PhaseCommittedRecover        Phase = "committed-recover"
	PhaseCommitConnectionLoss    Phase = "commit-connection-loss"
	PhaseConnectionLossRecover   Phase = "connection-loss-recover"
	PhasePreCommitConnectionLoss Phase = "pre-commit-connection-loss"
	PhaseRolledBackRecover       Phase = "rolled-back-recover"
)

var runIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type Backend interface {
	execution.EnforcementObjectReader
	execution.EnforcementObjectWriter
}

type Catalog interface {
	execution.EnforcementBundleStore
	CountEnforcementBundleBindingAudits(context.Context, string, string) (int, error)
}

type Config struct {
	RunID string
}

type CaseResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Report is safe release evidence. It excludes database addresses, object
// routing, policy bytes, credentials, audit metadata, and upstream errors.
type Report struct {
	SchemaVersion string       `json:"schemaVersion"`
	RunID         string       `json:"runId"`
	Phase         Phase        `json:"phase"`
	Status        string       `json:"status"`
	Cases         []CaseResult `json:"cases"`
}

type SuiteError struct {
	Phase Phase
	Case  string
}

func (err *SuiteError) Error() string {
	return "enforcement recovery conformance case failed: " + string(err.Phase) + "/" + err.Case
}

type Fixture struct {
	IsolationDomainID string
	ServiceID         string
	RevisionID        string
	Record            execution.EnforcementBundleRecord
	Binding           execution.EnforcementBundleBinding
	Content           []byte `json:"-"`
}

func FixtureFor(config Config) (Fixture, error) {
	if !runIDPattern.MatchString(config.RunID) {
		return Fixture{}, errors.New("invalid enforcement recovery conformance configuration")
	}
	content := []byte("version: 1\nmetadata:\n  purpose: dataground-enforcement-recovery-conformance\n")
	digest := sha256.Sum256(content)
	input := sha256.Sum256([]byte("input:" + config.RunID))
	binding := sha256.Sum256([]byte("binding:" + config.RunID))
	fixture := Fixture{
		IsolationDomainID: identity.Derived("iso", "enforcement-recovery:"+config.RunID),
		ServiceID:         identity.Derived("svc", "enforcement-recovery:"+config.RunID),
		RevisionID:        identity.Derived("rev", "enforcement-recovery:"+config.RunID),
		Content:           bytes.Clone(content),
	}
	fixture.Record = execution.EnforcementBundleRecord{
		SchemaVersion:     execution.EnforcementBundleSchemaV1,
		IsolationDomainID: fixture.IsolationDomainID,
		ID:                "conformance-" + hex.EncodeToString(digest[:16]),
		RevisionID:        fixture.RevisionID,
		Digest:            "sha256:" + hex.EncodeToString(digest[:]),
		MediaType:         execution.EnforcementBundleMediaType,
		SizeBytes:         int64(len(content)),
		Provenance: execution.EnforcementBundleProvenance{
			Producer:              "rosetta",
			SourceRevision:        hex.EncodeToString(input[:20]),
			CompilerVersion:       "1.0.0-conformance",
			CatalogVersion:        "rosetta/v1",
			TargetContractVersion: "rosetta/openshell-policy-v1",
			Mode:                  "strict",
			InputDigest:           "sha256:" + hex.EncodeToString(input[:]),
			BindingDigest:         "sha256:" + hex.EncodeToString(binding[:]),
		},
	}
	fixture.Record.ObjectKey = execution.EnforcementBundleObjectKey(fixture.Record)
	fixture.Binding = execution.EnforcementBundleBinding{
		Record:        fixture.Record,
		ActorID:       "conformance:enforcement-recovery",
		CorrelationID: "enforcement-recovery-" + config.RunID,
	}
	return fixture, nil
}

func RunPrepare(
	ctx context.Context,
	catalog Catalog,
	backend Backend,
	provision func(context.Context, Fixture) error,
	disconnect func(),
	config Config,
) (Report, error) {
	report, fixture, err := newReport(ctx, PhasePrepare, catalog, backend, config, 3)
	if err != nil {
		return report, err
	}
	if provision == nil || disconnect == nil {
		return report, errors.New("enforcement recovery conformance hooks are required")
	}
	if err := requireFreshScope(ctx, catalog, backend, fixture); err != nil {
		return fail(ctx, report, "fresh-scope")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "fresh-scope", Status: "passed"})
	if err := provision(ctx, fixture); err != nil {
		return fail(ctx, report, "fixture-provisioned")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "fixture-provisioned", Status: "passed"})

	disconnecting := &disconnectingBackend{Backend: backend, disconnect: disconnect}
	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, disconnecting, disconnecting)
	if err != nil {
		return fail(ctx, report, "object-retained-after-database-loss")
	}
	_, err = finalizer.Finalize(ctx, execution.EnforcementBundleFinalization{
		Binding: fixture.Binding,
		Content: bytes.Clone(fixture.Content),
	})
	if !errors.Is(err, execution.ErrEnforcementBundleUnavailable) || disconnecting.writes != 1 ||
		disconnecting.disconnects != 1 {
		return fail(ctx, report, "object-retained-after-database-loss")
	}
	persisted, err := read(ctx, backend, fixture.Record.ObjectKey)
	if err != nil || !bytes.Equal(persisted, fixture.Content) {
		return fail(ctx, report, "object-retained-after-database-loss")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "object-retained-after-database-loss", Status: "passed"})
	report.Status = "passed"
	return report, nil
}

func RunRecover(ctx context.Context, catalog Catalog, backend Backend, config Config) (Report, error) {
	report, fixture, err := newReport(ctx, PhaseRecover, catalog, backend, config, 4)
	if err != nil {
		return report, err
	}
	persisted, err := read(ctx, backend, fixture.Record.ObjectKey)
	if err != nil || !bytes.Equal(persisted, fixture.Content) {
		return fail(ctx, report, "retained-object-present")
	}
	if _, err := catalog.GetEnforcementBundleRecord(ctx, fixture.IsolationDomainID, fixture.Record.ID); !errors.Is(
		err,
		execution.ErrEnforcementBundleMissing,
	) {
		return fail(ctx, report, "retained-object-present")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "retained-object-present", Status: "passed"})

	observed := &observedBackend{Backend: backend}
	request := execution.EnforcementBundleFinalization{
		Binding: fixture.Binding,
		Content: bytes.Clone(fixture.Content),
	}
	barrier := newArrivalBarrier(ConcurrentRecoveryWorkers)
	results := make(chan recoveryResult, ConcurrentRecoveryWorkers)
	var workers sync.WaitGroup
	for range ConcurrentRecoveryWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			synchronized := &barrierCatalog{Catalog: catalog, barrier: barrier}
			finalizer, finalizerErr := execution.NewEnforcementBundleFinalizer(synchronized, observed, observed)
			if finalizerErr != nil {
				results <- recoveryResult{err: finalizerErr}
				return
			}
			bound, finalizationErr := finalizer.Finalize(ctx, request)
			results <- recoveryResult{record: bound, err: finalizationErr}
		}()
	}
	workers.Wait()
	close(results)
	for result := range results {
		if result.err != nil || !execution.EqualEnforcementBundleRecords(result.record, fixture.Record) {
			return fail(ctx, report, "concurrent-catalog-adoption-after-restarts")
		}
	}
	if observed.writes.Load() != ConcurrentRecoveryWorkers {
		return fail(ctx, report, "concurrent-catalog-adoption-after-restarts")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "concurrent-catalog-adoption-after-restarts", Status: "passed"})

	restartedFinalizer, err := execution.NewEnforcementBundleFinalizer(catalog, observed, observed)
	if err != nil {
		return fail(ctx, report, "read-only-replay")
	}
	replayed, err := restartedFinalizer.Finalize(ctx, request)
	if err != nil || !execution.EqualEnforcementBundleRecords(replayed, fixture.Record) ||
		observed.writes.Load() != ConcurrentRecoveryWorkers {
		return fail(ctx, report, "read-only-replay")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "read-only-replay", Status: "passed"})

	audits, err := catalog.CountEnforcementBundleBindingAudits(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || audits != 1 {
		return fail(ctx, report, "single-audit-commit")
	}
	restored, err := catalog.GetEnforcementBundleRecord(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || !execution.EqualEnforcementBundleRecords(restored, fixture.Record) {
		return fail(ctx, report, "single-audit-commit")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "single-audit-commit", Status: "passed"})
	report.Status = "passed"
	return report, nil
}

func RunOutage(ctx context.Context, catalog Catalog, backend Backend, config Config) (Report, error) {
	report, fixture, err := newReport(ctx, PhaseOutage, catalog, backend, config, 2)
	if err != nil {
		return report, err
	}
	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, backend, backend)
	if err != nil {
		return fail(ctx, report, "finalization-fails-closed-during-object-outage")
	}
	_, err = finalizer.Finalize(ctx, execution.EnforcementBundleFinalization{
		Binding: fixture.Binding,
		Content: bytes.Clone(fixture.Content),
	})
	if !errors.Is(err, execution.ErrEnforcementBundleUnavailable) {
		return fail(ctx, report, "finalization-fails-closed-during-object-outage")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "finalization-fails-closed-during-object-outage", Status: "passed"})

	if _, err := catalog.GetEnforcementBundleRecord(ctx, fixture.IsolationDomainID, fixture.Record.ID); !errors.Is(
		err,
		execution.ErrEnforcementBundleMissing,
	) {
		return fail(ctx, report, "catalog-remains-unbound")
	}
	audits, err := catalog.CountEnforcementBundleBindingAudits(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || audits != 0 {
		return fail(ctx, report, "catalog-remains-unbound")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "catalog-remains-unbound", Status: "passed"})
	report.Status = "passed"
	return report, nil
}

func newReport(
	ctx context.Context,
	phase Phase,
	catalog Catalog,
	backend Backend,
	config Config,
	caseCapacity int,
) (Report, Fixture, error) {
	report := Report{SchemaVersion: ReportSchemaV1, Phase: phase, Status: "failed", Cases: make([]CaseResult, 0, caseCapacity)}
	if err := ctx.Err(); err != nil {
		return report, Fixture{}, err
	}
	fixture, err := FixtureFor(config)
	if err != nil || catalog == nil || backend == nil {
		return report, Fixture{}, errors.New("invalid enforcement recovery conformance configuration")
	}
	report.RunID = config.RunID
	return report, fixture, nil
}

func requireFreshScope(ctx context.Context, catalog Catalog, backend Backend, fixture Fixture) error {
	if _, err := catalog.GetEnforcementBundleRecord(ctx, fixture.IsolationDomainID, fixture.Record.ID); !errors.Is(
		err,
		execution.ErrEnforcementBundleMissing,
	) {
		return errors.New("conformance catalog scope is not fresh")
	}
	object, err := backend.OpenEnforcementObject(ctx, fixture.Record.ObjectKey)
	if err == nil {
		if object != nil {
			_ = object.Close()
		}
		return errors.New("conformance object scope is not fresh")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !errors.Is(err, execution.ErrEnforcementObjectMissing) {
		return errors.New("conformance object scope is unavailable")
	}
	return nil
}

func fail(ctx context.Context, report Report, caseName string) (Report, error) {
	report.Cases = append(report.Cases, CaseResult{Name: caseName, Status: "failed"})
	if err := ctx.Err(); err != nil {
		return report, err
	}
	return report, &SuiteError{Phase: report.Phase, Case: caseName}
}

func read(ctx context.Context, backend Backend, key string) ([]byte, error) {
	object, err := backend.OpenEnforcementObject(ctx, key)
	if err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("conformance backend returned an empty stream")
	}
	content, readErr := io.ReadAll(io.LimitReader(object, execution.MaximumEnforcementPolicyBytes+1))
	closeErr := object.Close()
	if readErr != nil || closeErr != nil || len(content) > execution.MaximumEnforcementPolicyBytes {
		return nil, errors.New("conformance object read failed")
	}
	return content, nil
}

type disconnectingBackend struct {
	Backend
	disconnect  func()
	writes      int
	disconnects int
}

func (backend *disconnectingBackend) PutEnforcementObjectIfAbsent(
	ctx context.Context,
	key string,
	content io.Reader,
	size int64,
	digest string,
) error {
	backend.writes++
	if err := backend.Backend.PutEnforcementObjectIfAbsent(ctx, key, content, size, digest); err != nil {
		return err
	}
	backend.disconnects++
	backend.disconnect()
	return nil
}

type observedBackend struct {
	Backend
	writes atomic.Int64
}

func (backend *observedBackend) PutEnforcementObjectIfAbsent(
	ctx context.Context,
	key string,
	content io.Reader,
	size int64,
	digest string,
) error {
	backend.writes.Add(1)
	return backend.Backend.PutEnforcementObjectIfAbsent(ctx, key, content, size, digest)
}

type recoveryResult struct {
	record execution.EnforcementBundleRecord
	err    error
}

type arrivalBarrier struct {
	mutex     sync.Mutex
	remaining int
	ready     chan struct{}
}

func newArrivalBarrier(participants int) *arrivalBarrier {
	return &arrivalBarrier{remaining: participants, ready: make(chan struct{})}
}

func (barrier *arrivalBarrier) arrive(ctx context.Context) error {
	barrier.mutex.Lock()
	barrier.remaining--
	if barrier.remaining == 0 {
		close(barrier.ready)
	}
	barrier.mutex.Unlock()
	select {
	case <-barrier.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type barrierCatalog struct {
	Catalog
	barrier *arrivalBarrier
	once    sync.Once
	err     error
}

func (catalog *barrierCatalog) GetEnforcementBundleRecord(
	ctx context.Context,
	isolationDomainID string,
	bundleID string,
) (execution.EnforcementBundleRecord, error) {
	record, err := catalog.Catalog.GetEnforcementBundleRecord(ctx, isolationDomainID, bundleID)
	catalog.once.Do(func() { catalog.err = catalog.barrier.arrive(ctx) })
	if catalog.err != nil {
		return execution.EnforcementBundleRecord{}, catalog.err
	}
	return record, err
}

var _ Backend = (*disconnectingBackend)(nil)
var _ Backend = (*observedBackend)(nil)
