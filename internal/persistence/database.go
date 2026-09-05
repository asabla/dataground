package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

func OpenSQL(ctx context.Context, databaseURL string) (*sql.DB, error) {
	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	database := stdlib.OpenDB(*config, stdlib.OptionAfterConnect(configureDatabaseConnection))
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return database, nil
}

func OpenPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database pool URL: %w", err)
	}
	config.ConnConfig.RuntimeParams["application_name"] = "dataground-control-plane"
	config.AfterConnect = configureDatabaseConnection
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database pool: %w", err)
	}
	return pool, nil
}

func configureDatabaseConnection(_ context.Context, connection *pgx.Conn) error {
	// Durable trust records require canonical UTC timestamps. The default binary
	// decoder uses the host's local timezone, which can invalidate an unchanged
	// record after it is read back on a non-UTC deployment.
	connection.TypeMap().RegisterType(&pgtype.Type{
		Name: "timestamptz", OID: pgtype.TimestamptzOID,
		Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC},
	})
	return nil
}
