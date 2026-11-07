package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func parseRetentionRecovery(args []string) (persistence.WorkspaceRetentionRecoveryInput, error) {
	var input persistence.WorkspaceRetentionRecoveryInput
	var deadline string
	flags := flag.NewFlagSet("retain-workspace", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&input.IsolationDomainID, "isolation-domain", "", "exact isolation domain")
	flags.StringVar(&input.OperationID, "operation", "", "expired cancellation operation")
	flags.Int64Var(&input.ExpectedGeneration, "expected-generation", 0, "observed failed operation generation")
	flags.StringVar(&input.OwnerBinding, "owner-binding", "", "original confirmed owner binding")
	flags.StringVar(&input.ActorID, "actor", "", "database-authorized recovery operator")
	flags.StringVar(&input.CorrelationID, "correlation-id", "", "stable recovery identifier for exact replay")
	flags.StringVar(&input.ReasonDigest, "reason-sha256", "", "sha256-prefixed digest of the reviewed recovery reason")
	flags.StringVar(&deadline, "deadline-at", "", "absolute RFC3339 deadline, at most 15 minutes ahead")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return input, errors.New("invalid retain-workspace flags")
	}
	var err error
	input.Deadline, err = time.Parse(time.RFC3339Nano, deadline)
	if err != nil || input.IsolationDomainID == "" || input.OperationID == "" || input.ExpectedGeneration < 1 || input.OwnerBinding == "" || input.ActorID == "" || input.CorrelationID == "" || input.ReasonDigest == "" {
		return input, errors.New("all retain-workspace flags are required")
	}
	return input, nil
}

func runRetentionRecovery(args []string) int {
	input, err := parseRetentionRecovery(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "DATAGROUND_DATABASE_URL is required")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "retention recovery database is unavailable")
		return 1
	}
	defer pool.Close()
	if err := persistence.NewRepository(pool).RecoverWorkspaceRetention(ctx, input); err != nil {
		var denied *persistence.DomainError
		if errors.As(err, &denied) {
			fmt.Fprintln(os.Stderr, denied.Code)
		} else {
			fmt.Fprintln(os.Stderr, "retention recovery did not confirm acceptance; retry the exact request")
		}
		return 1
	}
	fmt.Fprintln(os.Stdout, "Retention recovery acceptance recorded; inspect the operation for the retention outcome.")
	return 0
}
