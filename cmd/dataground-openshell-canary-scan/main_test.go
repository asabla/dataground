package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const (
	commandTestCanary = "dataground-canary-v1:0123456789abcdefghijklmnopqrstuvwxyz_A-B-CD"
	commandTestRunID  = "0123456789abcdef0123456789abcdef"
)

func TestRunReportsClearBoundedScan(t *testing.T) {
	t.Parallel()

	input := strings.NewReader("safe material")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		commandArgs("sandbox-environment", "sandbox-credential-check", "1024"),
		input,
		&stdout,
		&stderr,
		commandTestClock(),
	)
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	var output report
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if output.Status != "clear" || !output.Complete || output.Matches != 0 || output.Surface != "sandbox-environment" || output.RunID != commandTestRunID || output.Resource != (resourceBinding{Kind: "sandbox", Name: "sandbox-credential-check"}) || output.Commitment != commandCommitment(commandTestCanary) || output.InputLimitBytes != 1024 || output.InspectedBytes != int64(len("safe material")) || output.StartedAt != "2026-07-24T12:00:00Z" || output.FinishedAt != "2026-07-24T12:00:01Z" {
		t.Fatalf("run() report = %+v", output)
	}
}

func TestRunReportsMatchWithoutEchoingCanary(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		commandArgs("gateway-logs", "dataground-gateway", "1024"),
		strings.NewReader(commandTestCanary),
		&stdout,
		&stderr,
		commandTestClock(),
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
		commandArgs("runtime-errors", "runtime-invocation", "3"),
		strings.NewReader("four"),
		&stdout,
		&stderr,
		commandTestClock(),
	)
	if exitCode != 1 || !strings.Contains(stderr.String(), "input limit exceeded") {
		t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	var output report
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if output.Status != "incomplete" || output.Complete || output.RunID != commandTestRunID || output.Resource != (resourceBinding{Kind: "runtime", Name: "runtime-invocation"}) || output.Commitment != commandCommitment(commandTestCanary) || output.InputLimitBytes != 3 || output.InspectedBytes != 4 {
		t.Fatalf("run() report = %+v", output)
	}
}

func TestRunFailsClosedForReversedObservationWindow(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		commandArgs("sandbox-process", "sandbox-credential-check", "1024"),
		strings.NewReader("safe material"),
		&stdout,
		&stderr,
		commandClock(
			time.Date(2026, 7, 24, 12, 0, 1, 0, time.UTC),
			time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
		),
	)
	if exitCode != 1 || !strings.Contains(stderr.String(), "canary scan failed") {
		t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	var output report
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if output.Status != "incomplete" || output.Complete {
		t.Fatalf("run() report = %+v", output)
	}
}

func TestRunDerivesResourceKindForEverySurface(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"sandbox-process":     "sandbox",
		"sandbox-environment": "sandbox",
		"sandbox-filesystem":  "sandbox",
		"provider-arguments":  "provider",
		"gateway-logs":        "gateway",
		"sandbox-logs":        "sandbox",
		"runtime-errors":      "runtime",
	}
	for surface, kind := range tests {
		surface, kind := surface, kind
		t.Run(surface, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := run(
				context.Background(),
				commandArgs(surface, "checked-resource", "1024"),
				strings.NewReader(""),
				&stdout,
				&stderr,
				commandTestClock(),
			)
			if exitCode != 0 || stderr.Len() != 0 {
				t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
			}
			var output report
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatalf("decode report: %v", err)
			}
			if output.Resource != (resourceBinding{Kind: kind, Name: "checked-resource"}) {
				t.Fatalf("run() resource = %+v", output.Resource)
			}
		})
	}
}

func TestRunRejectsInvalidResourceBindingConfiguration(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"missing run id": {
			"--surface", "sandbox-process",
			"--resource-name", "checked-resource",
			"--commitment", commandCommitment(commandTestCanary),
		},
		"uppercase run id": {
			"--surface", "sandbox-process",
			"--run-id", "0123456789ABCDEF0123456789ABCDEF",
			"--resource-name", "checked-resource",
			"--commitment", commandCommitment(commandTestCanary),
		},
		"missing resource name": {
			"--surface", "sandbox-process",
			"--run-id", commandTestRunID,
			"--commitment", commandCommitment(commandTestCanary),
		},
		"non-portable resource name": {
			"--surface", "sandbox-process",
			"--run-id", commandTestRunID,
			"--resource-name", "Checked Resource",
			"--commitment", commandCommitment(commandTestCanary),
		},
	}
	for name, args := range tests {
		name, args := name, args
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := run(
				context.Background(),
				args,
				strings.NewReader(""),
				&stdout,
				&stderr,
				commandTestClock(),
			)
			if exitCode != 2 || stdout.Len() != 0 {
				t.Fatalf("run() exit = %d, stdout = %q", exitCode, stdout.String())
			}
		})
	}
}

func TestRunRejectsUnboundedInputLimit(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		commandArgs("sandbox-filesystem", "sandbox-credential-check", "268435457"),
		strings.NewReader(""),
		&stdout,
		&stderr,
		commandTestClock(),
	)
	if exitCode != 2 || stdout.Len() != 0 {
		t.Fatalf("run() exit = %d, stdout = %q", exitCode, stdout.String())
	}
}

func TestRunRejectsUnknownSurface(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		commandArgs("host-environment", "sandbox-credential-check", "268435456"),
		strings.NewReader(""),
		&stdout,
		&stderr,
		commandTestClock(),
	)
	if exitCode != 2 || stdout.Len() != 0 {
		t.Fatalf("run() exit = %d, stdout = %q", exitCode, stdout.String())
	}
}

func commandArgs(surface string, resourceName string, maxBytes string) []string {
	return []string{
		"--surface", surface,
		"--run-id", commandTestRunID,
		"--resource-name", resourceName,
		"--commitment", commandCommitment(commandTestCanary),
		"--max-bytes", maxBytes,
	}
}

func commandTestClock() func() time.Time {
	return commandClock(
		time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 24, 12, 0, 1, 0, time.UTC),
	)
}

func commandClock(values ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		value := values[index]
		if index < len(values)-1 {
			index++
		}
		return value
	}
}

func commandCommitment(canary string) string {
	sum := sha256.Sum256([]byte(canary))
	return "sha256:" + hex.EncodeToString(sum[:])
}
