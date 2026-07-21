package recoveryconformance

import (
	"bytes"
	"context"
	"errors"

	"github.com/asabla/dataground/internal/execution"
)

// RunPreCommitConnectionLoss requires the catalog transaction's COMMIT request
// to be lost before PostgreSQL receives it. Outcome observation is deliberately
// deferred to a fresh process with a direct database connection.
func RunPreCommitConnectionLoss(
	ctx context.Context,
	catalog Catalog,
	backend Backend,
	waitForLoss func(context.Context) error,
	config Config,
) (Report, error) {
	report, fixture, err := newReport(ctx, PhasePreCommitConnectionLoss, catalog, backend, config, 2)
	if err != nil {
		return report, err
	}
	if waitForLoss == nil {
		return report, errors.New("PostgreSQL pre-commit connection-loss hook is required")
	}
	if requireRetainedUnbound(ctx, catalog, backend, fixture) != nil {
		return fail(ctx, report, "pre-commit-connection-loss-preconditions")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "pre-commit-connection-loss-preconditions", Status: "passed"})

	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, backend, backend)
	if err != nil {
		return fail(ctx, report, "pre-commit-result-ambiguous")
	}
	_, err = finalizer.Finalize(ctx, execution.EnforcementBundleFinalization{
		Binding: fixture.Binding,
		Content: bytes.Clone(fixture.Content),
	})
	if !errors.Is(err, execution.ErrEnforcementBundleUnavailable) || waitForLoss(ctx) != nil {
		return fail(ctx, report, "pre-commit-result-ambiguous")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "pre-commit-result-ambiguous", Status: "passed"})
	report.Status = "passed"
	return report, nil
}

// RunRolledBackRecover proves the lost COMMIT request left no catalog effect,
// then adopts the retained immutable object exactly once through a direct
// database connection and verifies replay remains read-only.
func RunRolledBackRecover(ctx context.Context, catalog Catalog, backend Backend, config Config) (Report, error) {
	report, fixture, err := newReport(ctx, PhaseRolledBackRecover, catalog, backend, config, 4)
	if err != nil {
		return report, err
	}
	if requireRetainedUnbound(ctx, catalog, backend, fixture) != nil {
		return fail(ctx, report, "rollback-observed-after-pre-commit-loss")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "rollback-observed-after-pre-commit-loss", Status: "passed"})

	observed := &observedBackend{Backend: backend}
	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, observed, observed)
	if err != nil {
		return fail(ctx, report, "retained-object-adopted-after-rollback")
	}
	request := execution.EnforcementBundleFinalization{
		Binding: fixture.Binding,
		Content: bytes.Clone(fixture.Content),
	}
	bound, err := finalizer.Finalize(ctx, request)
	if err != nil || !execution.EqualEnforcementBundleRecords(bound, fixture.Record) || observed.writes.Load() != 1 {
		return fail(ctx, report, "retained-object-adopted-after-rollback")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "retained-object-adopted-after-rollback", Status: "passed"})

	replayed, err := finalizer.Finalize(ctx, request)
	if err != nil || !execution.EqualEnforcementBundleRecords(replayed, fixture.Record) || observed.writes.Load() != 1 {
		return fail(ctx, report, "read-only-replay-after-rollback")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "read-only-replay-after-rollback", Status: "passed"})

	audits, err := catalog.CountEnforcementBundleBindingAudits(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || audits != 1 {
		return fail(ctx, report, "single-audit-after-rollback-adoption")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "single-audit-after-rollback-adoption", Status: "passed"})
	report.Status = "passed"
	return report, nil
}
