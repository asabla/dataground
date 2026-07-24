package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

const commandTestCanary = "dataground-canary-v1:0123456789abcdefghijklmnopqrstuvwxyz_A-B-CD"

func TestRunReportsClearBoundedScan(t *testing.T) {
	t.Parallel()

	input := strings.NewReader("safe material")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"--surface", "sandbox-environment", "--commitment", commandCommitment(commandTestCanary), "--max-bytes", "1024"},
		input,
		&stdout,
		&stderr,
	)
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	var output report
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if output.Status != "clear" || !output.Complete || output.Matches != 0 || output.Surface != "sandbox-environment" || output.Commitment != commandCommitment(commandTestCanary) || output.InputLimitBytes != 1024 || output.InspectedBytes != int64(len("safe material")) {
		t.Fatalf("run() report = %+v", output)
	}
}

func TestRunReportsMatchWithoutEchoingCanary(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"--surface", "gateway-logs", "--commitment", commandCommitment(commandTestCanary), "--max-bytes", "1024"},
		strings.NewReader(commandTestCanary),
		&stdout,
		&stderr,
	)
	if exitCode != 1 || stderr.Len() != 0 {
		t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	if strings.Contains(stdout.String(), commandTestCanary) {
		t.Fatal("report exposed canary plaintext")
	}
	var output report
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if output.Status != "matched" || !output.Complete || output.Matches != 1 || output.InputLimitBytes != 1024 || output.InspectedBytes != int64(len(commandTestCanary)) || output.Candidates != 1 {
		t.Fatalf("run() report = %+v", output)
	}
}

func TestRunFailsClosedForTruncatedInput(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"--surface", "runtime-errors", "--commitment", commandCommitment(commandTestCanary), "--max-bytes", "3"},
		strings.NewReader("four"),
		&stdout,
		&stderr,
	)
	if exitCode != 1 || !strings.Contains(stderr.String(), "input limit exceeded") {
		t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	var output report
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if output.Status != "incomplete" || output.Complete || output.Commitment != commandCommitment(commandTestCanary) || output.InputLimitBytes != 3 || output.InspectedBytes != 4 {
		t.Fatalf("run() report = %+v", output)
	}
}

func TestRunRejectsUnknownSurface(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"--surface", "host-environment", "--commitment", commandCommitment(commandTestCanary)},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if exitCode != 2 || stdout.Len() != 0 {
		t.Fatalf("run() exit = %d, stdout = %q", exitCode, stdout.String())
	}
}

func commandCommitment(canary string) string {
	sum := sha256.Sum256([]byte(canary))
	return "sha256:" + hex.EncodeToString(sum[:])
}
