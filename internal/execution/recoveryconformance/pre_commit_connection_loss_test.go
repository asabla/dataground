package recoveryconformance

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestPreCommitConnectionLossRollsBackAndAdoptsRetainedObject(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	rolledBack := &unavailableCatalog{Catalog: &memoryCatalog{available: true}}
	lossObserved := false
	report, err := RunPreCommitConnectionLoss(
		context.Background(),
		rolledBack,
		backend,
		func(context.Context) error { lossObserved = true; return nil },
		Config{RunID: testRunID},
	)
	if err != nil || report.Status != "passed" || !lossObserved {
		t.Fatalf("pre-commit report = %#v, error = %v, observed = %t", report, err, lossObserved)
	}
	if got, want := caseNames(report), []string{
		"pre-commit-connection-loss-preconditions",
		"pre-commit-result-ambiguous",
	}; !equalStrings(got, want) {
		t.Fatalf("pre-commit cases = %#v, want %#v", got, want)
	}

	recovered, err := RunRolledBackRecover(
		context.Background(), rolledBack.Catalog, backend, Config{RunID: testRunID},
	)
	if err != nil || recovered.Status != "passed" || recovered.Phase != PhaseRolledBackRecover {
		t.Fatalf("rolled-back recovery report = %#v, error = %v", recovered, err)
	}
	if got, want := caseNames(recovered), []string{
		"rollback-observed-after-pre-commit-loss",
		"retained-object-adopted-after-rollback",
		"read-only-replay-after-rollback",
		"single-audit-after-rollback-adoption",
	}; !equalStrings(got, want) {
		t.Fatalf("rolled-back recovery cases = %#v, want %#v", got, want)
	}
}

func TestPreCommitConnectionLossRequiresUnavailableResultAndObservedLoss(t *testing.T) {
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
		"unobserved connection loss": {
			catalog: &unavailableCatalog{Catalog: &memoryCatalog{available: true}},
			wait:    func(context.Context) error { return errors.New("connection loss was not observed") },
		},
	} {
		t.Run(name, func(t *testing.T) {
			report, err := RunPreCommitConnectionLoss(
				context.Background(), test.catalog, backend, test.wait, Config{RunID: testRunID},
			)
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || suiteErr.Case != "pre-commit-result-ambiguous" ||
				report.Cases[len(report.Cases)-1].Status != "failed" {
				t.Fatalf("report = %#v, error = %v", report, err)
			}
		})
	}
}

func TestRolledBackRecoveryRejectsContaminatedPartialEffectsWithoutWriting(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		catalog *memoryCatalog
		objects map[string][]byte
	}{
		"missing retained object": {
			catalog: &memoryCatalog{available: true},
			objects: map[string][]byte{},
		},
		"unexpected committed record": {
			catalog: &memoryCatalog{available: true, record: fixture.Record, audits: 1},
			objects: map[string][]byte{fixture.Record.ObjectKey: bytes.Clone(fixture.Content)},
		},
		"stray binding audit": {
			catalog: &memoryCatalog{available: true, audits: 1},
			objects: map[string][]byte{fixture.Record.ObjectKey: bytes.Clone(fixture.Content)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			backend := &memoryBackend{objects: test.objects}
			report, err := RunRolledBackRecover(
				context.Background(), test.catalog, backend, Config{RunID: testRunID},
			)
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || suiteErr.Case != "rollback-observed-after-pre-commit-loss" ||
				backend.writes != 0 || len(report.Cases) != 1 || report.Cases[0].Status != "failed" {
				t.Fatalf("report = %#v, error = %v, writes = %d", report, err, backend.writes)
			}
		})
	}
}

var _ Catalog = (*unavailableCatalog)(nil)
