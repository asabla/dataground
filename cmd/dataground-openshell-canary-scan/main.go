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
	"syscall"

	"github.com/asabla/dataground/internal/security/canaryscan"
)

const defaultMaxBytes = 256 << 20

var surfaces = map[string]struct{}{
	"sandbox-process":     {},
	"sandbox-environment": {},
	"sandbox-filesystem":  {},
	"provider-arguments":  {},
	"gateway-logs":        {},
	"sandbox-logs":        {},
	"runtime-errors":      {},
}

type report struct {
	SchemaVersion  string `json:"schemaVersion"`
	Surface        string `json:"surface"`
	Commitment     string `json:"canaryCommitment"`
	Status         string `json:"status"`
	Matches        int64  `json:"matches"`
	Complete       bool   `json:"complete"`
	InspectedBytes int64  `json:"inspectedBytes"`
	Candidates     int64  `json:"candidates"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("dataground-openshell-canary-scan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	commitment := flags.String("commitment", "", "lowercase sha256 commitment for a structured canary")
	surface := flags.String("surface", "", "closed credential evidence surface")
	maxBytes := flags.Int64("max-bytes", defaultMaxBytes, "maximum input bytes")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *commitment == "" || *maxBytes <= 0 {
		fmt.Fprintln(stderr, "invalid canary scan configuration")
		return 2
	}
	if _, ok := surfaces[*surface]; !ok {
		fmt.Fprintln(stderr, "invalid canary scan configuration")
		return 2
	}

	result, scanErr := canaryscan.Scan(ctx, stdin, *maxBytes, *commitment)
	output := report{
		SchemaVersion:  "dataground.dev.openshell-canary-scan/v1",
		Surface:        *surface,
		Commitment:     *commitment,
		Status:         "clear",
		Matches:        result.Matches,
		Complete:       scanErr == nil,
		InspectedBytes: result.InspectedBytes,
		Candidates:     result.Candidates,
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
