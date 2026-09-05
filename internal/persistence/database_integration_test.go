package persistence_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestDatabaseTimestampDecodingUsesUTC(t *testing.T) {
	databaseURL := testDatabaseURL(t)
	if os.Getenv("DATAGROUND_TEST_TIMESTAMP_CHILD") != "true" {
		// Use a separate process so changing the timezone cannot race with other
		// tests or depend on whether time.Local has already been initialized.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDatabaseTimestampDecodingUsesUTC$", "-test.count=1")
		command.Env = append(os.Environ(), "TZ=Europe/Stockholm", "DATAGROUND_TEST_TIMESTAMP_CHILD=true")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("non-UTC database test: %v\n%s", err, output)
		}
		return
	}

	expected := time.Date(2026, 7, 1, 12, 30, 15, 123456000, time.UTC)
	if _, offset := expected.In(time.Local).Zone(); offset == 0 {
		t.Fatal("test process must use a non-UTC local timezone")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	assertUTC := func(t *testing.T, value time.Time) {
		t.Helper()
		if !value.Equal(expected) || value.Location() != time.UTC {
			t.Fatalf("timestamp = %s (%s), want unchanged UTC instant", value.Format(time.RFC3339Nano), value.Location())
		}
	}
	const query = `SELECT '2026-07-01 12:30:15.123456+00'::timestamptz,
		'2026-07-01 12:30:15.123456+00'::timestamptz, NULL::timestamptz`

	t.Run("pool", func(t *testing.T) {
		pool, err := persistence.OpenPool(ctx, databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		for _, mode := range []pgx.QueryExecMode{pgx.QueryExecModeCacheStatement, pgx.QueryExecModeSimpleProtocol} {
			connection, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Release()
			if _, err := connection.Exec(ctx, `SET TIME ZONE 'Asia/Tokyo'`); err != nil {
				t.Fatal(err)
			}
			var value time.Time
			var nullable, absent pgtype.Timestamptz
			if err := connection.QueryRow(ctx, query, mode).Scan(&value, &nullable, &absent); err != nil {
				t.Fatal(err)
			}
			assertUTC(t, value)
			assertUTC(t, nullable.Time)
			if !nullable.Valid || absent.Valid {
				t.Fatal("timestamp nullability changed")
			}
			// Force another physical connection to exercise the connection hook.
			if err := connection.Conn().Close(ctx); err != nil {
				t.Fatal(err)
			}
			connection.Release()
		}
	})

	t.Run("sql", func(t *testing.T) {
		database, err := persistence.OpenSQL(ctx, databaseURL)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		database.SetMaxIdleConns(0)
		for range 2 {
			connection, err := database.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if _, err := connection.ExecContext(ctx, `SET TIME ZONE 'Asia/Tokyo'`); err != nil {
				t.Fatal(err)
			}
			var value time.Time
			var nullable, absent sql.NullTime
			if err := connection.QueryRowContext(ctx, query).Scan(&value, &nullable, &absent); err != nil {
				t.Fatal(err)
			}
			assertUTC(t, value)
			assertUTC(t, nullable.Time)
			if !nullable.Valid || absent.Valid {
				t.Fatal("timestamp nullability changed")
			}
			if err := connection.Close(); err != nil {
				t.Fatal(err)
			}
		}
	})
}
