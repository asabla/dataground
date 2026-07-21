package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution/recoveryconformance"
	"github.com/asabla/dataground/internal/execution/recoveryconformance/pgcommitproxy"
)

func TestRunRejectsUnsafeOrIncompleteConfigurationBeforeConnections(t *testing.T) {
	t.Setenv("DATAGROUND_TEST_DATABASE_URL", "postgres://unused.invalid/test")
	tests := map[string][]string{
		"remote endpoint": {
			"--endpoint", "https://s3.example.test", "--bucket", "disposable", "--phase", "prepare",
			"--run-id", "0123456789abcdef0123456789abcdef", "--allow-loopback-http",
		},
		"unknown phase": {
			"--endpoint", "http://127.0.0.1:8333", "--bucket", "disposable", "--phase", "delete",
			"--run-id", "0123456789abcdef0123456789abcdef", "--allow-loopback-http",
		},
		"implicit plaintext": {
			"--endpoint", "http://127.0.0.1:8333", "--bucket", "disposable", "--phase", "recover",
			"--run-id", "0123456789abcdef0123456789abcdef",
		},
		"unsafe run identifier": {
			"--endpoint", "http://127.0.0.1:8333", "--bucket", "disposable", "--phase", "prepare",
			"--run-id", "../shared", "--allow-loopback-http",
		},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := run(context.Background(), args, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d", code)
			}
			if stdout.Len() != 0 ||
				!strings.Contains(stderr.String(), "invalid enforcement recovery conformance command configuration") ||
				strings.Contains(stderr.String(), "example.test") || strings.Contains(stderr.String(), "unused.invalid") {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunRejectsRemoteDatabaseBeforeConnections(t *testing.T) {
	t.Setenv("DATAGROUND_TEST_DATABASE_URL", "postgres://user:secret@database.example.test/db?sslmode=disable")
	args := []string{
		"--endpoint", "http://127.0.0.1:8333", "--bucket", "disposable", "--phase", "prepare",
		"--run-id", "0123456789abcdef0123456789abcdef", "--allow-loopback-http",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(context.Background(), args, &stdout, &stderr); code != 2 || stdout.Len() != 0 ||
		strings.Contains(stderr.String(), "database.example.test") || strings.Contains(stderr.String(), "secret") {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestRunRequiresDatabaseConfiguration(t *testing.T) {
	t.Setenv("DATAGROUND_TEST_DATABASE_URL", "")
	args := []string{
		"--endpoint", "http://127.0.0.1:8333", "--bucket", "disposable", "--phase", "prepare",
		"--run-id", "0123456789abcdef0123456789abcdef", "--allow-loopback-http",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(context.Background(), args, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
		t.Fatalf("exit code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestValidPhaseIncludesCommitLossRecoveryBoundary(t *testing.T) {
	for _, phase := range []recoveryconformance.Phase{
		recoveryconformance.PhasePrepare,
		recoveryconformance.PhaseOutage,
		recoveryconformance.PhaseRecover,
		recoveryconformance.PhaseCommitLoss,
		recoveryconformance.PhaseCommittedRecover,
		recoveryconformance.PhaseCommitConnectionLoss,
		recoveryconformance.PhaseConnectionLossRecover,
		recoveryconformance.PhasePreCommitConnectionLoss,
		recoveryconformance.PhaseRolledBackRecover,
		recoveryconformance.PhaseFailoverRecover,
		recoveryconformance.PhaseFailoverCommitLoss,
		recoveryconformance.PhaseFailoverCommitRecover,
	} {
		if !validPhase(phase) {
			t.Fatalf("phase rejected: %s", phase)
		}
	}
	if validPhase("unknown") {
		t.Fatal("unknown phase accepted")
	}
}

func TestCommitProxyFaultMatchesRecoveryPhase(t *testing.T) {
	for _, test := range []struct {
		phase recoveryconformance.Phase
		fault pgcommitproxy.FaultPoint
	}{
		{phase: recoveryconformance.PhaseCommitConnectionLoss, fault: pgcommitproxy.AfterCommitDurability},
		{phase: recoveryconformance.PhasePreCommitConnectionLoss, fault: pgcommitproxy.BeforeCommitDurability},
		{phase: recoveryconformance.PhaseFailoverCommitLoss, fault: pgcommitproxy.DuringCommitPrimaryLoss},
	} {
		fault, required := commitProxyFault(test.phase)
		if !required || fault != test.fault {
			t.Fatalf("commitProxyFault(%q) = %q, %t", test.phase, fault, required)
		}
	}
	if fault, required := commitProxyFault(recoveryconformance.PhaseRecover); required || fault != "" {
		t.Fatalf("ordinary recovery requested proxy fault %q", fault)
	}
}

func TestPrimaryLossSignalDescriptorIsConfinedToInFlightFailover(t *testing.T) {
	if !validPrimaryLossSignalFD(recoveryconformance.PhaseFailoverCommitLoss, 3) {
		t.Fatal("in-flight failover rejected a dedicated signal descriptor")
	}
	for _, test := range []struct {
		phase      recoveryconformance.Phase
		descriptor int
	}{
		{phase: recoveryconformance.PhaseFailoverCommitLoss, descriptor: -1},
		{phase: recoveryconformance.PhaseFailoverCommitLoss, descriptor: 2},
		{phase: recoveryconformance.PhasePrepare, descriptor: 3},
	} {
		if validPrimaryLossSignalFD(test.phase, test.descriptor) {
			t.Fatalf("phase %q accepted descriptor %d", test.phase, test.descriptor)
		}
	}
}

func TestCommitProxyDatabaseAddressingStaysLoopback(t *testing.T) {
	target, err := databaseTarget("postgres://dataground:secret@127.0.0.1:55432/database?sslmode=disable")
	if err != nil || target != "127.0.0.1:55432" {
		t.Fatalf("database target = %q, error = %v", target, err)
	}
	proxied, err := proxiedDatabaseURL(
		"postgres://dataground:secret@127.0.0.1:55432/database?sslmode=disable",
		"127.0.0.1:41234",
	)
	if err != nil || proxied != "postgres://dataground:secret@127.0.0.1:41234/database?sslmode=disable" {
		t.Fatalf("proxied database URL = %q, error = %v", proxied, err)
	}
	if _, err := proxiedDatabaseURL(
		"postgres://dataground:secret@127.0.0.1:55432/database?sslmode=disable",
		"192.0.2.1:41234",
	); err == nil {
		t.Fatal("remote commit proxy address accepted")
	}
}

func TestHTTPStyleLoopbackValidation(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:8333", "http://[::1]:8333"} {
		if !isHTTPStyleLoopback(endpoint) {
			t.Fatalf("loopback endpoint rejected: %s", endpoint)
		}
	}
	for _, endpoint := range []string{
		"https://127.0.0.1:8333", "http://192.0.2.1:8333", "http://localhost:8333", "not-a-url",
	} {
		if isHTTPStyleLoopback(endpoint) {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
}

func TestLoopbackPostgresValidation(t *testing.T) {
	for _, databaseURL := range []string{
		"postgres://user:secret@127.0.0.1:55432/database?sslmode=disable",
		"postgresql://user:secret@[::1]:55432/database?sslmode=disable",
	} {
		if !isLoopbackPostgresURL(databaseURL) {
			t.Fatalf("loopback database rejected")
		}
	}
	for _, databaseURL := range []string{
		"postgres://user:secret@database.example.test/database?sslmode=require",
		"postgres://user:secret@127.0.0.1:55432/database?sslmode=require",
		"postgres://user:secret@127.0.0.1:55432/database?sslmode=disable&application_name=unsafe",
		"not-a-url",
	} {
		if isLoopbackPostgresURL(databaseURL) {
			t.Fatalf("unsafe database accepted")
		}
	}
}
