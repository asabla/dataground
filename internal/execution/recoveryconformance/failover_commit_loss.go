package recoveryconformance

import (
	"bytes"
	"context"
	"errors"

	"github.com/asabla/dataground/internal/execution"
)

// RunFailoverCommitLoss forwards the real catalog COMMIT, prevents the caller
// from observing its response, and asks external orchestration to remove the
// primary. Outcome observation is deferred to a fresh process on the promoted
// standby because either atomic transaction outcome is valid at this boundary.
func RunFailoverCommitLoss(
	ctx context.Context,
	catalog Catalog,
	backend Backend,
	waitForCommitForwarded func(context.Context) error,
	signalPrimaryLoss func() error,
	config Config,
) (Report, error) {
	report, fixture, err := newReport(ctx, PhaseFailoverCommitLoss, catalog, backend, config, 2)
	if err != nil {
		return report, err
	}
	if waitForCommitForwarded == nil || signalPrimaryLoss == nil {
		return report, errors.New("PostgreSQL in-flight failover hooks are required")
	}
	if requireRetainedUnbound(ctx, catalog, backend, fixture) != nil {
		return fail(ctx, report, "in-flight-failover-preconditions")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "in-flight-failover-preconditions", Status: "passed"})

	finalizationContext, cancelFinalization := context.WithCancel(ctx)
	defer cancelFinalization()
	result := make(chan error, 1)
	go func() {
		finalizer, finalizerErr := execution.NewEnforcementBundleFinalizer(catalog, backend, backend)
		if finalizerErr != nil {
			result <- finalizerErr
			return
		}
		_, finalizerErr = finalizer.Finalize(finalizationContext, execution.EnforcementBundleFinalization{
			Binding: fixture.Binding,
			Content: bytes.Clone(fixture.Content),
		})
		result <- finalizerErr
	}()
	if waitForCommitForwarded(ctx) != nil || signalPrimaryLoss() != nil {
		return fail(ctx, report, "primary-loss-during-commit-is-ambiguous")
	}
	select {
	case finalizerErr := <-result:
		if !errors.Is(finalizerErr, execution.ErrEnforcementBundleUnavailable) {
			return fail(ctx, report, "primary-loss-during-commit-is-ambiguous")
		}
	case <-ctx.Done():
		return report, ctx.Err()
	}
	report.Cases = append(report.Cases, CaseResult{
		Name: "primary-loss-during-commit-is-ambiguous", Status: "passed",
	})
	report.Status = "passed"
	return report, nil
}

// RunFailoverCommitRecover accepts only the two atomic outcomes visible after
// promotion: one exact binding with one audit, or neither effect. It converges
// either outcome to one binding and audit, then proves replay is read-only.
func RunFailoverCommitRecover(
	ctx context.Context,
	catalog Catalog,
	backend Backend,
	verifyPromoted func(context.Context, Fixture) error,
	config Config,
) (Report, error) {
	report, fixture, err := newReport(ctx, PhaseFailoverCommitRecover, catalog, backend, config, 5)
	if err != nil {
		return report, err
	}
	if verifyPromoted == nil {
		return report, errors.New("PostgreSQL failover verification hook is required")
	}
	if err := verifyPromoted(ctx, fixture); err != nil {
		return fail(ctx, report, "promoted-standby-ready-after-in-flight-loss")
	}
	report.Cases = append(report.Cases, CaseResult{
		Name: "promoted-standby-ready-after-in-flight-loss", Status: "passed",
	})

	persisted, err := read(ctx, backend, fixture.Record.ObjectKey)
	if err != nil || !bytes.Equal(persisted, fixture.Content) {
		return fail(ctx, report, "atomic-catalog-outcome-observed-after-failover")
	}
	existing, recordErr := catalog.GetEnforcementBundleRecord(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	audits, auditErr := catalog.CountEnforcementBundleBindingAudits(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	committed := recordErr == nil && execution.EqualEnforcementBundleRecords(existing, fixture.Record) &&
		auditErr == nil && audits == 1
	rolledBack := errors.Is(recordErr, execution.ErrEnforcementBundleMissing) && auditErr == nil && audits == 0
	if !committed && !rolledBack {
		return fail(ctx, report, "atomic-catalog-outcome-observed-after-failover")
	}
	report.Cases = append(report.Cases, CaseResult{
		Name: "atomic-catalog-outcome-observed-after-failover", Status: "passed",
	})

	observed := &observedBackend{Backend: backend}
	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, observed, observed)
	if err != nil {
		return fail(ctx, report, "catalog-converged-after-in-flight-failover")
	}
	request := execution.EnforcementBundleFinalization{Binding: fixture.Binding, Content: bytes.Clone(fixture.Content)}
	bound, err := finalizer.Finalize(ctx, request)
	expectedWrites := int64(0)
	if rolledBack {
		expectedWrites = 1
	}
	if err != nil || !execution.EqualEnforcementBundleRecords(bound, fixture.Record) ||
		observed.writes.Load() != expectedWrites {
		return fail(ctx, report, "catalog-converged-after-in-flight-failover")
	}
	report.Cases = append(report.Cases, CaseResult{
		Name: "catalog-converged-after-in-flight-failover", Status: "passed",
	})

	replayed, err := finalizer.Finalize(ctx, request)
	if err != nil || !execution.EqualEnforcementBundleRecords(replayed, fixture.Record) ||
		observed.writes.Load() != expectedWrites {
		return fail(ctx, report, "read-only-replay-after-in-flight-failover")
	}
	report.Cases = append(report.Cases, CaseResult{
		Name: "read-only-replay-after-in-flight-failover", Status: "passed",
	})

	audits, err = catalog.CountEnforcementBundleBindingAudits(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	restored, recordErr := catalog.GetEnforcementBundleRecord(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || audits != 1 || recordErr != nil || !execution.EqualEnforcementBundleRecords(restored, fixture.Record) {
		return fail(ctx, report, "single-audit-after-in-flight-failover")
	}
	report.Cases = append(report.Cases, CaseResult{
		Name: "single-audit-after-in-flight-failover", Status: "passed",
	})
	report.Status = "passed"
	return report, nil
}
