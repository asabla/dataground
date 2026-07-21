package recoveryconformance

import (
	"bytes"
	"context"
	"errors"

	"github.com/asabla/dataground/internal/execution"
)

// RunFailoverRecover proves a fresh process can observe a promoted physical
// standby, adopt an object retained before primary loss, and replay without
// another object write or audit. Promotion and WAL-position synchronization
// are external orchestration responsibilities verified by the supplied hook.
func RunFailoverRecover(
	ctx context.Context,
	catalog Catalog,
	backend Backend,
	verifyPromoted func(context.Context, Fixture) error,
	config Config,
) (Report, error) {
	report, fixture, err := newReport(ctx, PhaseFailoverRecover, catalog, backend, config, 5)
	if err != nil {
		return report, err
	}
	if verifyPromoted == nil {
		return report, errors.New("PostgreSQL failover verification hook is required")
	}
	if err := verifyPromoted(ctx, fixture); err != nil {
		return fail(ctx, report, "promoted-standby-has-replicated-fixture")
	}
	report.Cases = append(report.Cases, CaseResult{
		Name: "promoted-standby-has-replicated-fixture", Status: "passed",
	})

	if requireRetainedUnbound(ctx, catalog, backend, fixture) != nil {
		return fail(ctx, report, "retained-object-unbound-after-primary-loss")
	}
	report.Cases = append(report.Cases, CaseResult{
		Name: "retained-object-unbound-after-primary-loss", Status: "passed",
	})

	observed := &observedBackend{Backend: backend}
	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, observed, observed)
	if err != nil {
		return fail(ctx, report, "catalog-adopted-on-promoted-standby")
	}
	request := execution.EnforcementBundleFinalization{
		Binding: fixture.Binding,
		Content: bytes.Clone(fixture.Content),
	}
	bound, err := finalizer.Finalize(ctx, request)
	if err != nil || !execution.EqualEnforcementBundleRecords(bound, fixture.Record) || observed.writes.Load() != 1 {
		return fail(ctx, report, "catalog-adopted-on-promoted-standby")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "catalog-adopted-on-promoted-standby", Status: "passed"})

	replayed, err := finalizer.Finalize(ctx, request)
	if err != nil || !execution.EqualEnforcementBundleRecords(replayed, fixture.Record) || observed.writes.Load() != 1 {
		return fail(ctx, report, "read-only-replay-after-failover")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "read-only-replay-after-failover", Status: "passed"})

	audits, err := catalog.CountEnforcementBundleBindingAudits(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || audits != 1 {
		return fail(ctx, report, "single-audit-after-failover")
	}
	restored, err := catalog.GetEnforcementBundleRecord(ctx, fixture.IsolationDomainID, fixture.Record.ID)
	if err != nil || !execution.EqualEnforcementBundleRecords(restored, fixture.Record) {
		return fail(ctx, report, "single-audit-after-failover")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "single-audit-after-failover", Status: "passed"})
	report.Status = "passed"
	return report, nil
}
