package recoveryconformance

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/execution"
)

func TestCatalogCommitLossStateRecoversReadOnly(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	catalog := &memoryCatalog{available: true}
	terminated := false
	report, err := RunCommitLoss(
		context.Background(),
		catalog,
		backend,
		func() { terminated = true },
		Config{RunID: testRunID},
	)
	var suiteErr *SuiteError
	if !errors.As(err, &suiteErr) || suiteErr.Case != "process-terminated-after-catalog-commit" || !terminated ||
		len(report.Cases) != 2 || report.Cases[0].Status != "passed" || report.Cases[1].Status != "failed" {
		t.Fatalf("commit-loss report = %#v, error = %v, terminated = %t", report, err, terminated)
	}
	if !execution.EqualEnforcementBundleRecords(catalog.record, fixture.Record) || catalog.audits != 1 ||
		backend.writes != 1 {
		t.Fatalf("commit-loss effects = record %#v, audits %d, writes %d", catalog.record, catalog.audits, backend.writes)
	}

	recovered, err := RunCommittedRecover(context.Background(), catalog, backend, Config{RunID: testRunID})
	if err != nil || recovered.Status != "passed" || recovered.Phase != PhaseCommittedRecover {
		t.Fatalf("committed recovery report = %#v, error = %v", recovered, err)
	}
	if got, want := caseNames(recovered), []string{
		"catalog-commit-survived-process-loss",
		"object-consistent-after-process-loss",
		"read-only-replay-after-ambiguous-commit",
		"single-audit-after-ambiguous-commit",
	}; !equalStrings(got, want) {
		t.Fatalf("committed recovery cases = %#v, want %#v", got, want)
	}
	if catalog.audits != 1 || backend.writes != 1 {
		t.Fatalf("committed recovery effects = audits %d, writes %d", catalog.audits, backend.writes)
	}
}

func TestCommitLossRejectsMissingOrAlreadyBoundEffectsWithoutTermination(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		backend *memoryBackend
		catalog *memoryCatalog
	}{
		"missing object": {backend: newMemoryBackend(), catalog: &memoryCatalog{available: true}},
		"already bound": {
			backend: &memoryBackend{objects: map[string][]byte{fixture.Record.ObjectKey: bytes.Clone(fixture.Content)}},
			catalog: &memoryCatalog{available: true, record: fixture.Record, audits: 1},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			terminated := false
			report, err := RunCommitLoss(
				context.Background(), test.catalog, test.backend, func() { terminated = true }, Config{RunID: testRunID},
			)
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || suiteErr.Case != "commit-loss-preconditions" || terminated ||
				len(report.Cases) != 1 || report.Cases[0].Status != "failed" {
				t.Fatalf("report = %#v, error = %v, terminated = %t", report, err, terminated)
			}
		})
	}
}

func TestCommittedRecoveryRejectsIncompleteEffectsWithoutWriting(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]struct {
		backend  *memoryBackend
		catalog  *memoryCatalog
		wantCase string
	}{
		"missing catalog": {
			backend: &memoryBackend{objects: map[string][]byte{
				fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
			}},
			catalog:  &memoryCatalog{available: true},
			wantCase: "catalog-commit-survived-process-loss",
		},
		"duplicate audit": {
			backend: &memoryBackend{objects: map[string][]byte{
				fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
			}},
			catalog:  &memoryCatalog{available: true, record: fixture.Record, audits: 2},
			wantCase: "catalog-commit-survived-process-loss",
		},
		"missing object": {
			backend:  newMemoryBackend(),
			catalog:  &memoryCatalog{available: true, record: fixture.Record, audits: 1},
			wantCase: "object-consistent-after-process-loss",
		},
		"conflicting object": {
			backend:  &memoryBackend{objects: map[string][]byte{fixture.Record.ObjectKey: []byte("conflicting")}},
			catalog:  &memoryCatalog{available: true, record: fixture.Record, audits: 1},
			wantCase: "object-consistent-after-process-loss",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			report, err := RunCommittedRecover(context.Background(), test.catalog, test.backend, Config{RunID: testRunID})
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || suiteErr.Case != test.wantCase || test.backend.writes != 0 ||
				len(report.Cases) == 0 || report.Cases[len(report.Cases)-1].Status != "failed" {
				t.Fatalf("report = %#v, error = %v, writes = %d", report, err, test.backend.writes)
			}
		})
	}
}
