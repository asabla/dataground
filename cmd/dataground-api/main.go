package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultAddress = "127.0.0.1:8080"

func main() {
	address := os.Getenv("DATAGROUND_HTTP_ADDRESS")
	if address == "" {
		address = defaultAddress
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	handler := api.NewHandler()
	var pool *pgxpool.Pool
	if databaseURL := os.Getenv("DATAGROUND_DATABASE_URL"); databaseURL != "" {
		startupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		database, err := persistence.OpenSQL(startupCtx, databaseURL)
		if err == nil {
			err = persistence.RequireCurrentSchema(startupCtx, database)
			database.Close()
		}
		if err == nil {
			pool, err = persistence.OpenPool(startupCtx, databaseURL)
		}
		cancel()
		if err != nil {
			logger.Error("durable API startup failed", "error", err)
			os.Exit(1)
		}
		defer pool.Close()
		handler = api.NewDurableHandler(persistence.NewRepository(pool))
		logger.Info("durable PostgreSQL mode enabled")
	} else if address != defaultAddress {
		logger.Error("process-local reference mode may only bind to the default loopback address")
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP server shutdown failed", "error", err)
		}
	}()

	logger.Info("starting DataGround API", "address", address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}
