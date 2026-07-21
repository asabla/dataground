package recoveryconformance

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestFailoverRejoinObservesConvergedReadOnlyState(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	catalog := &memoryCatalog{available: true, record: fixture.Record, audits: 1}
	backend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	verified := false
	report, err := RunFailoverRejoinObserve(
		context.Background(),
		catalog,
		backend,
		func(_ context.Context, observed Fixture) error {
			verified = observed.RevisionID == fixture.RevisionID
			return nil
		},
		Config{RunID: testRunID},
	)
	if err != nil || report.Status != "passed" || report.Phase != PhaseFailoverRejoinObserve || !verified {
		t.Fatalf("rejoin report = %#v, error = %v, verified = %t", report, err, verified)
	}
	if got, want := caseNames(report), []string{
		"rewound-primary-rejoined-read-only",
		"rejoined-standby-has-converged-catalog",
		"read-only-replay-on-rejoined-standby",
		"single-audit-on-rejoined-standby",
	}; !equalStrings(got, want) {
		t.Fatalf("rejoin cases = %#v, want %#v", got, want)
	}
	if backend.writes != 0 || catalog.audits != 1 {
		t.Fatalf("rejoin effects = writes %d, audits %d", backend.writes, catalog.audits)
	}
}

func TestFailoverRejoinRequiresVerificationBeforeObservation(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	for name, verify := range map[string]func(context.Context, Fixture) error{
		"missing hook": nil,
		"not ready":    func(context.Context, Fixture) error { return errors.New("database detail") },
	} {
		t.Run(name, func(t *testing.T) {
			catalog := &memoryCatalog{available: true, record: fixture.Record, audits: 1}
			backend := &memoryBackend{objects: map[string][]byte{
				fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
			}}
			report, err := RunFailoverRejoinObserve(
				context.Background(), catalog, backend, verify, Config{RunID: testRunID},
			)
			if verify == nil {
				if err == nil || len(report.Cases) != 0 || backend.writes != 0 {
					t.Fatalf("report = %#v, error = %v, writes = %d", report, err, backend.writes)
				}
				return
			}
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || suiteErr.Case != "rewound-primary-rejoined-read-only" ||
				len(report.Cases) != 1 || report.Cases[0].Status != "failed" || backend.writes != 0 {
				t.Fatalf("report = %#v, error = %v, writes = %d", report, err, backend.writes)
			}
		})
	}
}

func TestFailoverRejoinRejectsIncompleteReplicatedEffects(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		catalog *memoryCatalog
		objects map[string][]byte
	}{
		"missing object": {
			catalog: &memoryCatalog{available: true, record: fixture.Record, audits: 1},
			objects: map[string][]byte{},
		},
		"missing record": {
			catalog: &memoryCatalog{available: true, audits: 1},
			objects: map[string][]byte{fixture.Record.ObjectKey: bytes.Clone(fixture.Content)},
		},
		"missing audit": {
			catalog: &memoryCatalog{available: true, record: fixture.Record},
			objects: map[string][]byte{fixture.Record.ObjectKey: bytes.Clone(fixture.Content)},
		},
		"duplicate audit": {
			catalog: &memoryCatalog{available: true, record: fixture.Record, audits: 2},
			objects: map[string][]byte{fixture.Record.ObjectKey: bytes.Clone(fixture.Content)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			backend := &memoryBackend{objects: test.objects}
			report, err := RunFailoverRejoinObserve(
				context.Background(),
				test.catalog,
				backend,
				func(context.Context, Fixture) error { return nil },
				Config{RunID: testRunID},
			)
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || backend.writes != 0 {
				t.Fatalf("report = %#v, error = %v, writes = %d", report, err, backend.writes)
			}
			if name == "missing audit" || name == "duplicate audit" {
				if suiteErr.Case != "single-audit-on-rejoined-standby" {
					t.Fatalf("case = %q", suiteErr.Case)
				}
			} else if suiteErr.Case != "rejoined-standby-has-converged-catalog" {
				t.Fatalf("case = %q", suiteErr.Case)
			}
		})
	}
}
