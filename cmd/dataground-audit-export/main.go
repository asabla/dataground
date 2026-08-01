package main

import (
	"context"
	"encoding/json"
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
	var domainID, cursor string
	var limit int
	flags.StringVar(&domainID, "isolation-domain", "", "isolation domain identifier")
	flags.StringVar(&cursor, "cursor", "", "opaque cursor returned by the preceding page")
	flags.IntVar(&limit, "limit", 500, "maximum records in this page")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || domainID == "" || limit < 1 || limit > 1000 {
		return errors.New("isolation-domain is required and limit must be between 1 and 1000")
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
	export, err := persistence.NewRepository(pool).ExportAuthorizationDecisions(
		operationCtx,
		domainID,
		cursor,
		limit,
	)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(export); err != nil {
		return fmt.Errorf("write authorization audit export: %w", err)
	}
	return nil
}
