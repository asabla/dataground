package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/asabla/dataground/internal/persistence"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(); err != nil {
		logger.Error("database migration failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		return fmt.Errorf("DATAGROUND_DATABASE_URL is required")
	}
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: dataground-migrate up|check|down-to-0")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer database.Close()

	switch os.Args[1] {
	case "up":
		return persistence.MigrateUp(ctx, database)
	case "check":
		return persistence.RequireCurrentSchema(ctx, database)
	case "down-to-0":
		return persistence.MigrateDownTo(ctx, database, 0)
	default:
		return fmt.Errorf("unknown migration command %q", os.Args[1])
	}
}
