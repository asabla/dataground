package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
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
