package recoveryconformance

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestFailoverRecoveryAdoptsRetainedObjectOnPromotedStandby(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	catalog := &memoryCatalog{available: true}
	verified := false
	report, err := RunFailoverRecover(
		context.Background(),
		catalog,
		backend,
		func(_ context.Context, observed Fixture) error {
			verified = observed.RevisionID == fixture.RevisionID
			return nil
		},
		Config{RunID: testRunID},
	)
	if err != nil || report.Status != "passed" || report.Phase != PhaseFailoverRecover || !verified {
		t.Fatalf("failover report = %#v, error = %v, verified = %t", report, err, verified)
	}
	if got, want := caseNames(report), []string{
		"promoted-standby-has-replicated-fixture",
		"retained-object-unbound-after-primary-loss",
		"catalog-adopted-on-promoted-standby",
		"read-only-replay-after-failover",
		"single-audit-after-failover",
	}; !equalStrings(got, want) {
		t.Fatalf("failover cases = %#v, want %#v", got, want)
	}
	if catalog.record.SchemaVersion == "" || catalog.audits != 1 || backend.writes != 1 {
		t.Fatalf("failover effects = record %#v, audits %d, writes %d", catalog.record, catalog.audits, backend.writes)
	}
}

func TestFailoverRecoveryRequiresPromotedReplicatedFixtureBeforeEffects(t *testing.T) {
	fixture, err := FixtureFor(Config{RunID: testRunID})
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{objects: map[string][]byte{
		fixture.Record.ObjectKey: bytes.Clone(fixture.Content),
	}}
	catalog := &memoryCatalog{available: true}
	for name, verify := range map[string]func(context.Context, Fixture) error{
		"missing hook":      nil,
		"standby not ready": func(context.Context, Fixture) error { return errors.New("database detail") },
	} {
		t.Run(name, func(t *testing.T) {
			report, err := RunFailoverRecover(
				context.Background(), catalog, backend, verify, Config{RunID: testRunID},
			)
			if verify == nil {
				if err == nil || len(report.Cases) != 0 || backend.writes != 0 {
					t.Fatalf("report = %#v, error = %v, writes = %d", report, err, backend.writes)
				}
				return
			}
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || suiteErr.Case != "promoted-standby-has-replicated-fixture" ||
				len(report.Cases) != 1 || report.Cases[0].Status != "failed" || backend.writes != 0 {
				t.Fatalf("report = %#v, error = %v, writes = %d", report, err, backend.writes)
			}
		})
	}
}

func TestFailoverRecoveryRejectsContaminatedEffectsBeforeWriting(t *testing.T) {
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
		"unexpected binding": {
			catalog: &memoryCatalog{available: true, record: fixture.Record, audits: 1},
			objects: map[string][]byte{fixture.Record.ObjectKey: bytes.Clone(fixture.Content)},
		},
		"stray audit": {
			catalog: &memoryCatalog{available: true, audits: 1},
			objects: map[string][]byte{fixture.Record.ObjectKey: bytes.Clone(fixture.Content)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			backend := &memoryBackend{objects: test.objects}
			report, err := RunFailoverRecover(
				context.Background(),
				test.catalog,
				backend,
				func(context.Context, Fixture) error { return nil },
				Config{RunID: testRunID},
			)
			var suiteErr *SuiteError
			if !errors.As(err, &suiteErr) || suiteErr.Case != "retained-object-unbound-after-primary-loss" ||
				backend.writes != 0 || len(report.Cases) != 2 || report.Cases[1].Status != "failed" {
				t.Fatalf("report = %#v, error = %v, writes = %d", report, err, backend.writes)
			}
		})
	}
}
