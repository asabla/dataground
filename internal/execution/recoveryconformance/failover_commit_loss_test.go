package recoveryconformance

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestFailoverCommitLossRequiresForwardedCommitAndPrimaryLoss(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	catalog := &ambiguousCommitCatalog{Catalog: &memoryCatalog{available: true}}
	forwarded := false
	signaled := false
	report, err := RunFailoverCommitLoss(
		context.Background(),
		catalog,
		backend,
		func(context.Context) error { forwarded = true; return nil },
		func() error { signaled = true; return nil },
		Config{RunID: testRunID},
	)
	if err != nil || report.Status != "passed" || !forwarded || !signaled {
		t.Fatalf("report = %#v, error = %v, forwarded = %t, signaled = %t", report, err, forwarded, signaled)
	}
	if got, want := caseNames(report), []string{
		"in-flight-failover-preconditions",
		"primary-loss-during-commit-is-ambiguous",
	}; !equalStrings(got, want) {
		t.Fatalf("cases = %#v, want %#v", got, want)
	}
}

func TestFailoverCommitLossCancelsFinalizationWhenCoordinationFails(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	for name, hooks := range map[string]struct {
		wait   func(context.Context) error
		signal func() error
	}{
		"forwarding wait": {
			wait:   func(context.Context) error { return errors.New("wait failed") },
			signal: func() error { return nil },
		},
		"primary loss signal": {
			wait:   func(context.Context) error { return nil },
			signal: func() error { return errors.New("signal failed") },
		},
	} {
		t.Run(name, func(t *testing.T) {
			report, err := RunFailoverCommitLoss(
				context.Background(),
				&unavailableCatalog{Catalog: &memoryCatalog{available: true}},
				backend,
				hooks.wait,
				hooks.signal,
				Config{RunID: testRunID},
			)
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || suiteErr.Case != "primary-loss-during-commit-is-ambiguous" ||
				len(report.Cases) != 2 || report.Cases[1].Status != "failed" {
				t.Fatalf("report = %#v, error = %v", report, err)
			}
		})
	}
}

func TestFailoverCommitRecoveryConvergesBothAtomicOutcomes(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	for name, catalog := range map[string]*memoryCatalog{
		"committed":   {available: true, record: fixture.Record, audits: 1},
		"rolled back": {available: true},
	} {
		t.Run(name, func(t *testing.T) {
			backend := &memoryBackend{objects: map[string][]byte{
				fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
			}}
			report, err := RunFailoverCommitRecover(
				context.Background(), catalog, backend,
				func(context.Context, Fixture) error { return nil },
				Config{RunID: testRunID},
			)
			if err != nil || report.Status != "passed" || catalog.audits != 1 {
				t.Fatalf("report = %#v, error = %v, audits = %d", report, err, catalog.audits)
			}
			expectedWrites := 0
			if name == "rolled back" {
				expectedWrites = 1
			}
			if backend.writes != expectedWrites {
				t.Fatalf("writes = %d, want %d", backend.writes, expectedWrites)
			}
			if got, want := caseNames(report), []string{
				"promoted-standby-ready-after-in-flight-loss",
				"atomic-catalog-outcome-observed-after-failover",
				"catalog-converged-after-in-flight-failover",
				"read-only-replay-after-in-flight-failover",
				"single-audit-after-in-flight-failover",
			}; !equalStrings(got, want) {
				t.Fatalf("cases = %#v, want %#v", got, want)
			}
		})
	}
}

func TestFailoverCommitRecoveryRejectsNonAtomicOrConflictingState(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	conflicting := fixture.Record
	conflicting.Digest = "sha256:" + string(make([]byte, 64))
	for name, catalog := range map[string]*memoryCatalog{
		"record without audit": {available: true, record: fixture.Record},
		"audit without record": {available: true, audits: 1},
		"duplicate audit":      {available: true, record: fixture.Record, audits: 2},
		"conflicting record":   {available: true, record: conflicting, audits: 1},
	} {
		t.Run(name, func(t *testing.T) {
			backend := &memoryBackend{objects: map[string][]byte{
				fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
			}}
			report, err := RunFailoverCommitRecover(
				context.Background(), catalog, backend,
				func(context.Context, Fixture) error { return nil },
				Config{RunID: testRunID},
			)
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || suiteErr.Case != "atomic-catalog-outcome-observed-after-failover" ||
				backend.writes != 0 || len(report.Cases) != 2 || report.Cases[1].Status != "failed" {
				t.Fatalf("report = %#v, error = %v, writes = %d", report, err, backend.writes)
			}
		})
	}
}
