package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func main() {
	var kind, domainID, operationID, actorID, reason, deduplicationID string
	var deadline time.Duration
	flag.StringVar(&kind, "kind", "", "service-publication or invocation-execution")
	flag.StringVar(&domainID, "isolation-domain", "", "isolation domain identifier")
	flag.StringVar(&operationID, "operation", "", "failed operation identifier")
	flag.StringVar(&actorID, "actor", "", "authorized operator identifier")
	flag.StringVar(&reason, "reason", "", "operator-visible repair reason")
	flag.StringVar(&deduplicationID, "deduplication-id", "", "stable ID for repeatable repair")
	flag.DurationVar(&deadline, "deadline", 15*time.Minute, "new operation deadline from now")
	flag.Parse()
	if kind == "" || domainID == "" || operationID == "" || actorID == "" || reason == "" || deduplicationID == "" || deadline <= 0 {
		fmt.Fprintln(os.Stderr, "all repair flags are required")
		os.Exit(2)
	}
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "DATAGROUND_DATABASE_URL is required")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err == nil {
		defer pool.Close()
		err = persistence.NewRepository(pool).RepairOperation(
			ctx, kind, domainID, operationID, actorID, reason, deduplicationID, time.Now().UTC().Add(deadline),
		)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
