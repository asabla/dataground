package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asabla/dataground/internal/outbox"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

const pollInterval = 250 * time.Millisecond

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	databaseURL := os.Getenv("DATAGROUND_DATABASE_URL")
	if databaseURL == "" {
		logger.Error("DATAGROUND_DATABASE_URL is required")
		os.Exit(1)
	}
	workerID := os.Getenv("DATAGROUND_WORKER_ID")
	if workerID == "" {
		hostname, _ := os.Hostname()
		workerID = "worker-" + hostname
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err == nil {
		err = persistence.RequireCurrentSchema(ctx, database)
		database.Close()
	}
	if err != nil {
		logger.Error("database schema check failed", "error", err)
		os.Exit(1)
	}
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	worker := reconcile.New(repository, reconcile.NewReferenceDriver(pool), workerID)
	dispatcher := outbox.New(repository, outbox.AcknowledgePublisher{}, workerID)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	logger.Info("starting DataGround reconciler", "worker_id", workerID)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, kind := range []string{persistence.OperationKindPublication, persistence.OperationKindInvocation} {
				ran, runErr := worker.RunOne(ctx, kind)
				if runErr != nil {
					logger.Error("reconciliation failed", "kind", kind, "error", runErr)
				} else if ran {
					logger.Debug("operation advanced", "kind", kind)
				}
			}
			if _, dispatchErr := dispatcher.RunOne(ctx); dispatchErr != nil {
				logger.Error("outbox delivery failed", "error", dispatchErr)
			}
		}
	}
}
