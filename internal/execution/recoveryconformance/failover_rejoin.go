package recoveryconformance

import (
	"bytes"
	"context"
	"errors"

	"github.com/asabla/dataground/internal/execution"
)

// RunFailoverRejoinObserve proves a fenced former primary was rewound and
// rejoined as a read-only physical standby. Rewind, fence removal and WAL
// synchronization are external orchestration responsibilities verified by the
// supplied hook. The suite performs no catalog or object mutation.
func RunFailoverRejoinObserve(
	ctx context.Context,
	catalog Catalog,
	backend Backend,
	verifyRejoined func(context.Context, Fixture) error,
	config Config,
) (Report, error) {
	report, fixture, err := newReport(ctx, PhaseFailoverRejoinObserve, catalog, backend, config, 4)
	if err != nil {
		return report, err
	}
	if verifyRejoined == nil {
		return report, errors.New("PostgreSQL rejoin verification hook is required")
	}
	if err := verifyRejoined(ctx, fixture); err != nil {
		return fail(ctx, report, "rewound-primary-rejoined-read-only")
	}
	report.Cases = append(report.Cases, CaseResult{
		Name: "rewound-primary-rejoined-read-only", Status: "passed",
	})

	persisted, err := read(ctx, backend, fixture.Record.ObjectKey)
	restored, recordErr := catalog.GetEnforcementBundleRecord(
		ctx, fixture.IsolationDomainID, fixture.Record.ID,
	)
	if err != nil || !bytes.Equal(persisted, fixture.Content) || recordErr != nil ||
		!execution.EqualEnforcementBundleRecords(restored, fixture.Record) {
		return fail(ctx, report, "rejoined-standby-has-converged-catalog")
	}
	report.Cases = append(report.Cases, CaseResult{
		Name: "rejoined-standby-has-converged-catalog", Status: "passed",
	})

	observed := &observedBackend{Backend: backend}
	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, observed, observed)
	if err != nil {
		return fail(ctx, report, "read-only-replay-on-rejoined-standby")
	}
	replayed, err := finalizer.Finalize(ctx, execution.EnforcementBundleFinalization{
		Binding: fixture.Binding,
		Content: bytes.Clone(fixture.Content),
	})
	if err != nil || !execution.EqualEnforcementBundleRecords(replayed, fixture.Record) ||
		observed.writes.Load() != 0 {
		return fail(ctx, report, "read-only-replay-on-rejoined-standby")
	}
	report.Cases = append(report.Cases, CaseResult{
		Name: "read-only-replay-on-rejoined-standby", Status: "passed",
	})

	audits, err := catalog.CountEnforcementBundleBindingAudits(
		ctx, fixture.IsolationDomainID, fixture.Record.ID,
	)
	if err != nil || audits != 1 {
		return fail(ctx, report, "single-audit-on-rejoined-standby")
	}
	report.Cases = append(report.Cases, CaseResult{
		Name: "single-audit-on-rejoined-standby", Status: "passed",
	})
	report.Status = "passed"
	return report, nil
}
