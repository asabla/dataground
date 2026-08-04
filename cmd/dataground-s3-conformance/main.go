package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asabla/dataground/internal/execution/s3artifactconformance"
	"github.com/asabla/dataground/internal/execution/s3auditconformance"
	"github.com/asabla/dataground/internal/execution/s3conformance"
	"github.com/asabla/dataground/internal/execution/s3store"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("dataground-s3-conformance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("endpoint", "", "S3 origin without credentials, path, query, or fragment")
	bucket := flags.String("bucket", "", "caller-provisioned disposable bucket")
	style := flags.String("addressing-style", string(s3store.PathStyle), "path or virtual-hosted")
	runID := flags.String("run-id", "", "unique 32-character lowercase hexadecimal run identifier")
	allowLoopbackHTTP := flags.Bool("allow-loopback-http", false, "allow explicit plaintext loopback development endpoint")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *endpoint == "" || *bucket == "" ||
		*runID == "" || !*allowLoopbackHTTP || !isHTTPStyleLoopback(*endpoint) {
		fmt.Fprintln(stderr, "invalid S3 conformance command configuration")
		return 2
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	store, err := s3store.New(s3store.Config{
		Endpoint:             *endpoint,
		Bucket:               *bucket,
		AddressingStyle:      s3store.AddressingStyle(*style),
		AllowHTTPForLoopback: *allowLoopbackHTTP,
		HTTPClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, "invalid S3 conformance storage configuration")
		return 2
	}

	report, enforcementErr := s3conformance.Run(ctx, store, s3conformance.Config{RunID: *runID})
	artifactErr := s3artifactconformance.Run(
		ctx,
		store,
		s3artifactconformance.Config{RunID: *runID},
	)
	auditExportErr := s3auditconformance.Run(
		ctx,
		store,
		s3auditconformance.Config{RunID: *runID},
	)
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fmt.Fprintln(stderr, "could not encode S3 conformance report")
		return 1
	}
	if enforcementErr != nil || artifactErr != nil || auditExportErr != nil {
		fmt.Fprintln(stderr, "S3 object conformance failed")
		return 1
	}
	return 0
}

func isHTTPStyleLoopback(raw string) bool {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Scheme != "http" {
		return false
	}
	address := net.ParseIP(endpoint.Hostname())
	return address != nil && address.IsLoopback()
}
