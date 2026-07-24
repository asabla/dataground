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

type commandResource struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type commandReport struct {
	SchemaVersion    string          `json:"schemaVersion"`
	Surface          string          `json:"surface"`
	RunID            string          `json:"runID"`
	Resource         commandResource `json:"resource"`
	CanaryCommitment string          `json:"canaryCommitment"`
	InputCommitment  string          `json:"inputCommitment"`
	Status           string          `json:"status"`
	Matches          int64           `json:"matches"`
	Complete         bool            `json:"complete"`
	InputLimitBytes  int64           `json:"inputLimitBytes"`
	InspectedBytes   int64           `json:"inspectedBytes"`
	Candidates       int64           `json:"candidates"`
	StartedAt        string          `json:"startedAt"`
	FinishedAt       string          `json:"finishedAt"`
}

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
	)
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	var output commandReport
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if output.Status != "clear" || !output.Complete || output.Matches != 0 || output.Surface != "sandbox-environment" || output.RunID != commandTestRunID || output.Resource != (commandResource{Kind: "sandbox", Name: "sandbox-credential-check"}) || output.CanaryCommitment != commandCommitment(commandTestCanary) || !validInputCommitment(output.InputCommitment) || output.InputLimitBytes != 1024 || output.InspectedBytes != int64(len("safe material")) || !validObservationWindow(output.StartedAt, output.FinishedAt) {
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
	)
	if exitCode != 1 || stderr.Len() != 0 {
		t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	if strings.Contains(stdout.String(), commandTestCanary) {
		t.Fatal("report exposed canary plaintext")
	}
	var output commandReport
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
	)
	if exitCode != 1 || !strings.Contains(stderr.String(), "input limit exceeded") {
		t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	var output commandReport
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if output.Status != "incomplete" || output.Complete || output.RunID != commandTestRunID || output.Resource != (commandResource{Kind: "runtime", Name: "runtime-invocation"}) || output.CanaryCommitment != commandCommitment(commandTestCanary) || !validInputCommitment(output.InputCommitment) || output.InputLimitBytes != 3 || output.InspectedBytes != 4 {
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
			)
			if exitCode != 0 || stderr.Len() != 0 {
				t.Fatalf("run() exit = %d, stderr = %q", exitCode, stderr.String())
			}
			var output commandReport
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
				t.Fatalf("decode report: %v", err)
			}
			if output.Resource != (commandResource{Kind: kind, Name: "checked-resource"}) {
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
		"malformed commitment": {
			"--surface", "sandbox-process",
			"--run-id", commandTestRunID,
			"--resource-name", "checked-resource",
			"--commitment", "sha256:00",
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
	)
	if exitCode != 2 || stdout.Len() != 0 {
		t.Fatalf("run() exit = %d, stdout = %q", exitCode, stdout.String())
	}
}

func validObservationWindow(started, finished string) bool {
	startedAt, startErr := time.Parse(time.RFC3339Nano, started)
	finishedAt, finishErr := time.Parse(time.RFC3339Nano, finished)
	return startErr == nil &&
		finishErr == nil &&
		strings.HasSuffix(started, "Z") &&
		strings.HasSuffix(finished, "Z") &&
		!finishedAt.Before(startedAt)
}

func validInputCommitment(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 ||
		!strings.HasPrefix(value, "sha256:") ||
		strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil
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

func commandCommitment(canary string) string {
	sum := sha256.Sum256([]byte(canary))
	return "sha256:" + hex.EncodeToString(sum[:])
}
