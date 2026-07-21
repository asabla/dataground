package recoveryconformance

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

func TestCommitConnectionLossRecoversReadOnly(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	catalog := &ambiguousCommitCatalog{Catalog: &memoryCatalog{available: true}}
	lossObserved := false
	report, err := RunCommitConnectionLoss(
		context.Background(),
		catalog,
		backend,
		func(context.Context) error { lossObserved = true; return nil },
		Config{RunID: testRunID},
	)
	if err != nil || report.Status != "passed" || !lossObserved {
		t.Fatalf("connection-loss report = %#v, error = %v, observed = %t", report, err, lossObserved)
	}
	if got, want := caseNames(report), []string{"commit-connection-loss-preconditions", "commit-result-ambiguous"}; !equalStrings(got, want) {
		t.Fatalf("connection-loss cases = %#v, want %#v", got, want)
	}

	recovered, err := RunConnectionLossRecover(
		context.Background(), catalog.Catalog, backend, Config{RunID: testRunID},
	)
	if err != nil || recovered.Status != "passed" || recovered.Phase != PhaseConnectionLossRecover {
		t.Fatalf("connection-loss recovery report = %#v, error = %v", recovered, err)
	}
	if got, want := caseNames(recovered), []string{
		"catalog-commit-observed-after-connection-loss",
		"object-consistent-after-connection-loss",
		"read-only-replay-after-connection-loss",
		"single-audit-after-connection-loss",
	}; !equalStrings(got, want) {
		t.Fatalf("connection-loss recovery cases = %#v, want %#v", got, want)
	}
}

func TestCommitConnectionLossRequiresUnavailableResultAndObservedLoss(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	for name, test := range map[string]struct {
		catalog Catalog
		wait    func(context.Context) error
	}{
		"acknowledged commit": {
			catalog: &memoryCatalog{available: true},
			wait:    func(context.Context) error { return nil },
		},
		"failed before commit": {
			catalog: &unavailableCatalog{Catalog: &memoryCatalog{available: true}},
			wait:    func(context.Context) error { return errors.New("connection remained available") },
		},
	} {
		t.Run(name, func(t *testing.T) {
			report, err := RunCommitConnectionLoss(
				context.Background(), test.catalog, backend, test.wait, Config{RunID: testRunID},
			)
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || suiteErr.Case != "commit-result-ambiguous" ||
				report.Cases[len(report.Cases)-1].Status != "failed" {
				t.Fatalf("report = %#v, error = %v", report, err)
			}
		})
	}
}

func TestConnectionLossRecoveryRejectsMissingCommitWithoutWriting(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	report, err := RunConnectionLossRecover(
		context.Background(), &memoryCatalog{available: true}, backend, Config{RunID: testRunID},
	)
	var suiteErr *SuiteError
	if !errors.As(err, &suiteErr) || suiteErr.Case != "catalog-commit-observed-after-connection-loss" ||
		backend.writes != 0 || len(report.Cases) != 1 || report.Cases[0].Status != "failed" {
		t.Fatalf("report = %#v, error = %v, writes = %d", report, err, backend.writes)
	}
}

type ambiguousCommitCatalog struct {
	Catalog
}

func (catalog *ambiguousCommitCatalog) BindEnforcementBundle(
	ctx context.Context,
	binding execution.EnforcementBundleBinding,
) (execution.EnforcementBundleRecord, error) {
	if _, err := catalog.Catalog.BindEnforcementBundle(ctx, binding); err != nil {
		return execution.EnforcementBundleRecord{}, err
	}
	return execution.EnforcementBundleRecord{}, errors.New("ambiguous commit result")
}

type unavailableCatalog struct {
	Catalog
}

func (catalog *unavailableCatalog) BindEnforcementBundle(
	context.Context,
	execution.EnforcementBundleBinding,
) (execution.EnforcementBundleRecord, error) {
	return execution.EnforcementBundleRecord{}, errors.New("catalog unavailable before commit")
}
