package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("dataground-audit-export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var exportID, domainID, actorID, reason, correlationID, cursor string
	var limit int
	flags.StringVar(&exportID, "export-id", "", "stable authorization audit export identifier")
	flags.StringVar(&domainID, "isolation-domain", "", "isolation domain identifier")
	flags.StringVar(&actorID, "actor", "", "authorized operator identifier")
	flags.StringVar(&reason, "reason", "", "operator-visible export reason")
	flags.StringVar(&correlationID, "correlation-id", "", "stable export correlation identifier")
	flags.StringVar(&cursor, "cursor", "", "opaque continuation cursor from the preceding page")
	flags.IntVar(&limit, "limit", 500, "maximum records in this page")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 ||
		exportID == "" ||
		domainID == "" ||
		actorID == "" ||
		reason == "" ||
		correlationID == "" ||
		limit < 1 ||
		limit > 1000 {
		return errors.New("export-id, isolation-domain, actor, reason, correlation-id, and a limit from 1 to 1000 are required")
	}
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATAGROUND_DATABASE_URL is required")
	}
	operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	database, err := persistence.OpenSQL(operationCtx, databaseURL)
	if err != nil {
		return err
	}
	if err := persistence.RequireCurrentSchema(operationCtx, database); err != nil {
		database.Close()
		return err
	}
	if err := database.Close(); err != nil {
		return err
	}
	pool, err := persistence.OpenPool(operationCtx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	reasonDigest := sha256.Sum256([]byte(reason))
	document, err := persistence.NewRepository(pool).ExportAuthorizationDecisionsAudited(
		operationCtx,
		persistence.AuthorizationAuditExportRequest{
			ExportID:          exportID,
			IsolationDomainID: domainID,
			RequestedBy:       actorID,
			ReasonDigest:      reasonDigest[:],
			CorrelationID:     correlationID,
			Cursor:            cursor,
			Limit:             limit,
		},
	)
	if err != nil {
		return err
	}
	if _, err := output.Write(append(document, '\n')); err != nil {
		return fmt.Errorf("write authorization audit export: %w", err)
	}
	return nil
}
