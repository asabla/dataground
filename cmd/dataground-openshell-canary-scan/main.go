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
	"time"

	"github.com/asabla/dataground/internal/security/canaryscan"
)

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
	maxBytes := flags.Int64("max-bytes", canaryscan.MaxInputBytes, "maximum input bytes")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "invalid canary scan configuration")
		return 2
	}

	output, scanErr := canaryscan.ScanReport(ctx, stdin, canaryscan.ReportConfig{
		Surface:          *surface,
		RunID:            *runID,
		ResourceName:     *resourceName,
		CanaryCommitment: *commitment,
		MaxBytes:         *maxBytes,
		Now:              now,
	})
	if errors.Is(scanErr, canaryscan.ErrInvalidConfiguration) {
		fmt.Fprintln(stderr, "invalid canary scan configuration")
		return 2
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
		default:
			fmt.Fprintln(stderr, "canary scan failed")
		}
		return 1
	}
	if output.Matches > 0 {
		return 1
	}
	return 0
}
