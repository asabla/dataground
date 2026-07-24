package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/asabla/dataground/internal/security/canaryscan"
)

const defaultMaxBytes = 256 << 20

var (
	runIDPattern        = regexp.MustCompile(`^[a-f0-9]{32}$`)
	resourceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	surfaceResourceKinds = map[string]string{
		"sandbox-process":     "sandbox",
		"sandbox-environment": "sandbox",
		"sandbox-filesystem":  "sandbox",
		"provider-arguments":  "provider",
		"gateway-logs":        "gateway",
		"sandbox-logs":        "sandbox",
		"runtime-errors":      "runtime",
	}
)

type resourceBinding struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type report struct {
	SchemaVersion   string          `json:"schemaVersion"`
	Surface         string          `json:"surface"`
	RunID           string          `json:"runID"`
	Resource        resourceBinding `json:"resource"`
	Commitment      string          `json:"canaryCommitment"`
	Status          string          `json:"status"`
	Matches         int64           `json:"matches"`
	Complete        bool            `json:"complete"`
	InputLimitBytes int64           `json:"inputLimitBytes"`
	InspectedBytes  int64           `json:"inspectedBytes"`
	Candidates      int64           `json:"candidates"`
	StartedAt       string          `json:"startedAt"`
	FinishedAt      string          `json:"finishedAt"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, time.Now))
}

func run(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	now func() time.Time,
) int {
	flags := flag.NewFlagSet("dataground-openshell-canary-scan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	commitment := flags.String("commitment", "", "lowercase sha256 commitment for a structured canary")
	runID := flags.String("run-id", "", "lowercase 128-bit evidence run nonce")
	resourceName := flags.String("resource-name", "", "portable live resource identifier")
	surface := flags.String("surface", "", "closed credential evidence surface")
	maxBytes := flags.Int64("max-bytes", defaultMaxBytes, "maximum input bytes")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 ||
		*commitment == "" ||
		!runIDPattern.MatchString(*runID) ||
		!resourceNamePattern.MatchString(*resourceName) ||
		*maxBytes <= 0 ||
		*maxBytes > defaultMaxBytes {
		fmt.Fprintln(stderr, "invalid canary scan configuration")
		return 2
	}
	resourceKind, ok := surfaceResourceKinds[*surface]
	if !ok {
		fmt.Fprintln(stderr, "invalid canary scan configuration")
		return 2
	}

	startedAt := now().UTC()
	result, scanErr := canaryscan.Scan(ctx, stdin, *maxBytes, *commitment)
	finishedAt := now().UTC()
	if finishedAt.Before(startedAt) {
		scanErr = errors.Join(scanErr, errors.New("canary scan observation clock moved backwards"))
	}
	output := report{
		SchemaVersion:   "dataground.dev.openshell-canary-scan/v1",
		Surface:         *surface,
		RunID:           *runID,
		Resource:        resourceBinding{Kind: resourceKind, Name: *resourceName},
		Commitment:      *commitment,
		Status:          "clear",
		Matches:         result.Matches,
		Complete:        scanErr == nil,
		InputLimitBytes: *maxBytes,
		InspectedBytes:  result.InspectedBytes,
		Candidates:      result.Candidates,
		StartedAt:       startedAt.Format(time.RFC3339Nano),
		FinishedAt:      finishedAt.Format(time.RFC3339Nano),
	}
	if result.Matches > 0 {
		output.Status = "matched"
	}
	if scanErr != nil {
		output.Status = "incomplete"
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(output); err != nil {
		fmt.Fprintln(stderr, "could not encode canary scan report")
		return 1
	}
	if scanErr != nil {
		switch {
		case errors.Is(scanErr, context.Canceled):
			fmt.Fprintln(stderr, "canary scan cancelled")
		case errors.Is(scanErr, canaryscan.ErrInputLimit):
			fmt.Fprintln(stderr, "canary scan input limit exceeded")
		case errors.Is(scanErr, canaryscan.ErrInvalidCommitment):
			fmt.Fprintln(stderr, "invalid canary scan commitment")
		default:
			fmt.Fprintln(stderr, "canary scan failed")
		}
		return 1
	}
	if result.Matches > 0 {
		return 1
	}
	return 0
}
