package recoveryconformance

import (
	"bytes"
	"context"
	"errors"

	"github.com/asabla/dataground/internal/execution"
)

// RunCommitLoss terminates the caller after the real catalog transaction has
// committed but before the finalizer can observe its result. A live conformance
// run must provide a terminator that never returns. Returning from the hook
// makes the case fail so a test cannot accidentally claim process-loss evidence.
func RunCommitLoss(
	ctx context.Context,
	catalog Catalog,
	backend Backend,
	terminate func(),
	config Config,
) (Report, error) {
	report, fixture, err := newReport(ctx, PhaseCommitLoss, catalog, backend, config, 2)
	if err != nil {
		return report, err
	}
	if terminate == nil {
		return report, errors.New("enforcement recovery conformance termination hook is required")
	}
	if requireRetainedUnbound(ctx, catalog, backend, fixture) != nil {
		return fail(ctx, report, "commit-loss-preconditions")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "commit-loss-preconditions", Status: "passed"})

	terminating := &terminatingCatalog{Catalog: catalog, terminate: terminate}
	finalizer, err := execution.NewEnforcementBundleFinalizer(terminating, backend, backend)
	if err != nil {
		return fail(ctx, report, "process-terminated-after-catalog-commit")
	}
	_, _ = finalizer.Finalize(ctx, execution.EnforcementBundleFinalization{
		Binding: fixture.Binding,
		Content: bytes.Clone(fixture.Content),
	})
	return fail(ctx, report, "process-terminated-after-catalog-commit")
}

// RunCommittedRecover proves a fresh caller observes the committed relational
// and object effects and can replay finalization without another write or audit.
func RunCommittedRecover(ctx context.Context, catalog Catalog, backend Backend, config Config) (Report, error) {
	return runCommittedRecover(ctx, catalog, backend, config, PhaseCommittedRecover, committedRecoveryCases{
		catalog: "catalog-commit-survived-process-loss",
		object:  "object-consistent-after-process-loss",
		replay:  "read-only-replay-after-ambiguous-commit",
		audit:   "single-audit-after-ambiguous-commit",
	})
}

type committedRecoveryCases struct {
	catalog string
	object  string
	replay  string
	audit   string
}

func runCommittedRecover(
	ctx context.Context,
	catalog Catalog,
	backend Backend,
	config Config,
	phase Phase,
	cases committedRecoveryCases,
) (Report, error) {
	report, fixture, err := newReport(ctx, phase, catalog, backend, config, 4)
	if err != nil {
		return report, err
	}
	existing, err := catalog.GetEnforcementBundleRecord(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || !execution.EqualEnforcementBundleRecords(existing, fixture.Record) {
		return fail(ctx, report, cases.catalog)
	}
	audits, err := catalog.CountEnforcementBundleBindingAudits(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || audits != 1 {
		return fail(ctx, report, cases.catalog)
	}
	report.Cases = append(report.Cases, CaseResult{Name: cases.catalog, Status: "passed"})

	persisted, err := read(ctx, backend, fixture.Record.ObjectKey)
	if err != nil || !bytes.Equal(persisted, fixture.Content) {
		return fail(ctx, report, cases.object)
	}
	report.Cases = append(report.Cases, CaseResult{Name: cases.object, Status: "passed"})

	observed := &observedBackend{Backend: backend}
	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, observed, observed)
	if err != nil {
		return fail(ctx, report, cases.replay)
	}
	replayed, err := finalizer.Finalize(ctx, execution.EnforcementBundleFinalization{
		Binding: fixture.Binding,
		Content: bytes.Clone(fixture.Content),
	})
	if err != nil || !execution.EqualEnforcementBundleRecords(replayed, fixture.Record) || observed.writes.Load() != 0 {
		return fail(ctx, report, cases.replay)
	}
	report.Cases = append(report.Cases, CaseResult{Name: cases.replay, Status: "passed"})

	audits, err = catalog.CountEnforcementBundleBindingAudits(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || audits != 1 {
		return fail(ctx, report, cases.audit)
	}
	report.Cases = append(report.Cases, CaseResult{Name: cases.audit, Status: "passed"})
	report.Status = "passed"
	return report, nil
}

func requireRetainedUnbound(ctx context.Context, catalog Catalog, backend Backend, fixture Fixture) error {
	persisted, err := read(ctx, backend, fixture.Record.ObjectKey)
	if err != nil || !bytes.Equal(persisted, fixture.Content) {
		return errors.New("retained enforcement object is unavailable")
	}
	if _, err := catalog.GetEnforcementBundleRecord(ctx, fixture.IsolationDomainID, fixture.Record.ID); !errors.Is(
		err,
		execution.ErrEnforcementBundleMissing,
	) {
		return errors.New("enforcement bundle catalog is not fresh")
	}
	audits, err := catalog.CountEnforcementBundleBindingAudits(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || audits != 0 {
		return errors.New("enforcement bundle audit scope is not fresh")
	}
	return nil
}

type terminatingCatalog struct {
	Catalog
	terminate func()
}

func (catalog *terminatingCatalog) BindEnforcementBundle(
	ctx context.Context,
	binding execution.EnforcementBundleBinding,
) (execution.EnforcementBundleRecord, error) {
	record, err := catalog.Catalog.BindEnforcementBundle(ctx, binding)
	if err != nil {
		return execution.EnforcementBundleRecord{}, err
	}
	catalog.terminate()
	return record, errors.New("process termination hook returned")
}

var _ Catalog = (*terminatingCatalog)(nil)
