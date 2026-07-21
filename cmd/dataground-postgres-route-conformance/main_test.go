package main

import (
	"bytes"
	"context"
	"testing"
)

func TestRunRejectsIncompleteConfiguration(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := run(context.Background(), []string{"--mode", "serve"}, &stdout, &stderr)
	if status != 2 || stdout.Len() != 0 || stderr.String() != "invalid PostgreSQL route conformance configuration\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func TestRunServeRejectsMissingHealthIdentityWithoutStartingListener(t *testing.T) {
	t.Setenv("DATAGROUND_ROUTER_HEALTH_DATABASE_URL", "")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := run(context.Background(), []string{
		"--mode", "serve",
		"--listen-address", "127.0.0.1:0",
		"--control-socket", "/tmp/dataground-missing-health.sock",
		"--primary-target", "127.0.0.1:55432",
		"--promoted-target", "127.0.0.1:55433",
		"--route", "primary",
	}, &stdout, &stderr)
	if status != 2 || stdout.Len() != 0 ||
		stderr.String() != "invalid PostgreSQL route conformance configuration\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func TestRunRejectsUnknownModeWithoutEchoingArguments(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := run(context.Background(), []string{"--mode", "secret-value"}, &stdout, &stderr)
	if status != 2 || stdout.Len() != 0 || stderr.String() != "invalid PostgreSQL route conformance configuration\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func TestDatabaseRoleRejectsNonLoopbackURL(t *testing.T) {
	if _, err := databaseRole(
		context.Background(),
		"postgres://user:secret@192.0.2.1:5432/database?sslmode=disable",
	); err == nil {
		t.Fatal("database role probe accepted a non-loopback URL")
	}
}

func TestLoopbackDatabaseURLRejectsConnectionOverrides(t *testing.T) {
	for _, databaseURL := range []string{
		"postgres://user:secret@127.0.0.1:5432/database?sslmode=disable&host=192.0.2.1",
		"postgres://user:secret@127.0.0.1:5432/database?sslmode=disable&service=external",
		"postgres://user:secret@127.0.0.1:5432/database?sslmode=require",
	} {
		if err := validateLoopbackDatabaseURL(databaseURL); err == nil {
			t.Fatalf("connection override was accepted: %q", databaseURL)
		}
	}
}

func TestTargetHealthProbeRejectsNonLoopbackTargetWithoutLeakingIdentity(t *testing.T) {
	probe, err := targetHealthProbe(
		"postgres://user:secret@127.0.0.1:55431/database?sslmode=disable",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = probe(context.Background(), "192.0.2.1:5432")
	if err == nil {
		t.Fatal("health probe accepted a non-loopback target")
	}
	if bytes.Contains([]byte(err.Error()), []byte("secret")) {
		t.Fatalf("health probe error leaked identity: %q", err)
	}
}

func TestRunSelectRequiresPromotionGeneration(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := run(context.Background(), []string{
		"--mode", "select",
		"--control-socket", "/tmp/dataground-unused-route.sock",
	}, &stdout, &stderr)
	if status != 2 || stdout.Len() != 0 ||
		stderr.String() != "invalid PostgreSQL route conformance configuration\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func TestRunPoolModeSanitizesConfigurationFailure(t *testing.T) {
	t.Setenv(
		"DATAGROUND_TEST_DATABASE_URL",
		"postgres://user:secret@192.0.2.1:5432/database?sslmode=disable",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := run(context.Background(), []string{"--mode", "pool"}, &stdout, &stderr)
	if status != 1 || stdout.Len() != 0 ||
		stderr.String() != "PostgreSQL pool reconnection conformance failed\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}
