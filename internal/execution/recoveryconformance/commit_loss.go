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
	persisted, err := read(ctx, backend, fixture.Record.ObjectKey)
	if err != nil || !bytes.Equal(persisted, fixture.Content) {
		return fail(ctx, report, "commit-loss-preconditions")
	}
	if _, err := catalog.GetEnforcementBundleRecord(ctx, fixture.IsolationDomainID, fixture.Record.ID); !errors.Is(
		err,
		execution.ErrEnforcementBundleMissing,
	) {
		return fail(ctx, report, "commit-loss-preconditions")
	}
	audits, err := catalog.CountEnforcementBundleBindingAudits(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || audits != 0 {
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
	report, fixture, err := newReport(ctx, PhaseCommittedRecover, catalog, backend, config, 4)
	if err != nil {
		return report, err
	}
	existing, err := catalog.GetEnforcementBundleRecord(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || !execution.EqualEnforcementBundleRecords(existing, fixture.Record) {
		return fail(ctx, report, "catalog-commit-survived-process-loss")
	}
	audits, err := catalog.CountEnforcementBundleBindingAudits(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || audits != 1 {
		return fail(ctx, report, "catalog-commit-survived-process-loss")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "catalog-commit-survived-process-loss", Status: "passed"})

	persisted, err := read(ctx, backend, fixture.Record.ObjectKey)
	if err != nil || !bytes.Equal(persisted, fixture.Content) {
		return fail(ctx, report, "object-consistent-after-process-loss")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "object-consistent-after-process-loss", Status: "passed"})

	observed := &observedBackend{Backend: backend}
	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, observed, observed)
	if err != nil {
		return fail(ctx, report, "read-only-replay-after-ambiguous-commit")
	}
	replayed, err := finalizer.Finalize(ctx, execution.EnforcementBundleFinalization{
		Binding: fixture.Binding,
		Content: bytes.Clone(fixture.Content),
	})
	if err != nil || !execution.EqualEnforcementBundleRecords(replayed, fixture.Record) || observed.writes.Load() != 0 {
		return fail(ctx, report, "read-only-replay-after-ambiguous-commit")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "read-only-replay-after-ambiguous-commit", Status: "passed"})

	audits, err = catalog.CountEnforcementBundleBindingAudits(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || audits != 1 {
		return fail(ctx, report, "single-audit-after-ambiguous-commit")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "single-audit-after-ambiguous-commit", Status: "passed"})
	report.Status = "passed"
	return report, nil
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
