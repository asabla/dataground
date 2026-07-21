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
