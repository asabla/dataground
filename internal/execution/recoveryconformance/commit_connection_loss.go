package recoveryconformance

import (
	"bytes"
	"context"
	"errors"

	"github.com/asabla/dataground/internal/execution"
)

// RunCommitConnectionLoss requires the real catalog to return an ambiguous
// persistence result after its COMMIT response is lost. Outcome observation is
// deliberately deferred to a fresh recovery process.
func RunCommitConnectionLoss(
	ctx context.Context,
	catalog Catalog,
	backend Backend,
	waitForLoss func(context.Context) error,
	config Config,
) (Report, error) {
	report, fixture, err := newReport(ctx, PhaseCommitConnectionLoss, catalog, backend, config, 2)
	if err != nil {
		return report, err
	}
	if waitForLoss == nil {
		return report, errors.New("PostgreSQL commit connection-loss hook is required")
	}
	if requireRetainedUnbound(ctx, catalog, backend, fixture) != nil {
		return fail(ctx, report, "commit-connection-loss-preconditions")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "commit-connection-loss-preconditions", Status: "passed"})

	finalizer, err := execution.NewEnforcementBundleFinalizer(catalog, backend, backend)
	if err != nil {
		return fail(ctx, report, "commit-result-ambiguous")
	}
	_, err = finalizer.Finalize(ctx, execution.EnforcementBundleFinalization{
		Binding: fixture.Binding,
		Content: bytes.Clone(fixture.Content),
	})
	if !errors.Is(err, execution.ErrEnforcementBundleUnavailable) || waitForLoss(ctx) != nil {
		return fail(ctx, report, "commit-result-ambiguous")
	}
	report.Cases = append(report.Cases, CaseResult{Name: "commit-result-ambiguous", Status: "passed"})
	report.Status = "passed"
	return report, nil
}

// RunConnectionLossRecover observes the committed outcome from a fresh
// database connection and proves the finalizer can replay without new effects.
func RunConnectionLossRecover(ctx context.Context, catalog Catalog, backend Backend, config Config) (Report, error) {
	return runCommittedRecover(ctx, catalog, backend, config, PhaseConnectionLossRecover, committedRecoveryCases{
		catalog: "catalog-commit-observed-after-connection-loss",
		object:  "object-consistent-after-connection-loss",
		replay:  "read-only-replay-after-connection-loss",
		audit:   "single-audit-after-connection-loss",
	})
}
