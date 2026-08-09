package persistence

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	currentSchemaVersion int64 = 40
	migrationLockKey     int64 = 0x4441544147524f55
	upMarker                   = "-- dataground:up"
	downMarker                 = "-- dataground:down"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version int64
	name    string
	up      string
	down    string
}

func MigrateUp(ctx context.Context, database *sql.DB) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer transaction.Rollback()
	if err := prepareMigrations(ctx, transaction); err != nil {
		return err
	}
	version, err := schemaVersion(ctx, transaction)
	if err != nil {
		return err
	}
	for _, candidate := range migrations {
		if candidate.version <= version {
			continue
		}
		if _, err := transaction.ExecContext(ctx, candidate.up); err != nil {
			return fmt.Errorf("apply migration %s: %w", candidate.name, err)
		}
		if _, err := transaction.ExecContext(
			ctx,
			`INSERT INTO dataground_schema_migrations (version, name, applied_at) VALUES ($1, $2, now())`,
			candidate.version,
			candidate.name,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", candidate.name, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return RequireCurrentSchema(ctx, database)
}

func MigrateDownTo(ctx context.Context, database *sql.DB, target int64) error {
	if target < 0 || target >= currentSchemaVersion {
		return fmt.Errorf("down migration target must be between 0 and %d", currentSchemaVersion-1)
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer transaction.Rollback()
	if err := prepareMigrations(ctx, transaction); err != nil {
		return err
	}
	version, err := schemaVersion(ctx, transaction)
	if err != nil {
		return err
	}
	for index := len(migrations) - 1; index >= 0; index-- {
		candidate := migrations[index]
		if candidate.version > version || candidate.version <= target {
			continue
		}
		if _, err := transaction.ExecContext(ctx, candidate.down); err != nil {
			return fmt.Errorf("roll back migration %s: %w", candidate.name, err)
		}
		if _, err := transaction.ExecContext(
			ctx,
			`DELETE FROM dataground_schema_migrations WHERE version = $1`,
			candidate.version,
		); err != nil {
			return fmt.Errorf("remove migration record %s: %w", candidate.name, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit migration rollback: %w", err)
	}
	return nil
}

func RequireCurrentSchema(ctx context.Context, database *sql.DB) error {
	var version int64
	err := database.QueryRowContext(
		ctx,
		`SELECT COALESCE(max(version), 0) FROM dataground_schema_migrations`,
	).Scan(&version)
	if err != nil {
		return fmt.Errorf("read database schema version: %w", err)
	}
	if version != currentSchemaVersion {
		return fmt.Errorf("database schema version %d is not supported; expected %d", version, currentSchemaVersion)
	}
	return nil
}

func prepareMigrations(ctx context.Context, transaction *sql.Tx) error {
	if _, err := transaction.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS dataground_schema_migrations (
			version bigint PRIMARY KEY,
			name text NOT NULL,
			applied_at timestamptz NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("prepare migration history: %w", err)
	}
	return nil
}

func schemaVersion(ctx context.Context, transaction *sql.Tx) (int64, error) {
	var version int64
	if err := transaction.QueryRowContext(
		ctx,
		`SELECT COALESCE(max(version), 0) FROM dataground_schema_migrations`,
	).Scan(&version); err != nil {
		return 0, fmt.Errorf("read migration history: %w", err)
	}
	return version, nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		underscore := strings.IndexByte(entry.Name(), '_')
		if underscore <= 0 {
			return nil, fmt.Errorf("migration %q does not start with a numeric version", entry.Name())
		}
		version, err := strconv.ParseInt(entry.Name()[:underscore], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("migration %q has an invalid version", entry.Name())
		}
		content, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		sections := strings.Split(string(content), downMarker)
		if len(sections) != 2 || !strings.HasPrefix(strings.TrimSpace(sections[0]), upMarker) {
			return nil, fmt.Errorf("migration %q must contain one up and one down section", entry.Name())
		}
		up := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(sections[0]), upMarker))
		down := strings.TrimSpace(sections[1])
		if up == "" || down == "" {
			return nil, fmt.Errorf("migration %q contains an empty section", entry.Name())
		}
		migrations = append(migrations, migration{version: version, name: entry.Name(), up: up, down: down})
	}
	sort.Slice(migrations, func(left, right int) bool {
		return migrations[left].version < migrations[right].version
	})
	for index, candidate := range migrations {
		if candidate.version != int64(index+1) {
			return nil, fmt.Errorf("migration versions must be contiguous from 1; got %d at index %d", candidate.version, index)
		}
	}
	if len(migrations) == 0 || migrations[len(migrations)-1].version != currentSchemaVersion {
		return nil, fmt.Errorf("embedded migrations do not reach schema version %d", currentSchemaVersion)
	}
	return migrations, nil
}
