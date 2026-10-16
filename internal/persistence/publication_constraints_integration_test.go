package persistence_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPublicationDecisionMigrationPreservesFencingAndSourceAuthority(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	db, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE publication_authorization_decisions`); err != nil {
			t.Error(err)
		}
		if err := persistence.MigrateUp(context.Background(), db); err != nil {
			t.Error(err)
		}
	}()
	insert := `INSERT INTO publication_authorization_decisions
 (contract,isolation_domain_id,service_id,revision_id,operation_id,actor_id,correlation_id,phase,expected_version,fencing_token,plan_digest,verification_digest,policy_contract,policy_set_id,policy_digest,outcome)
 VALUES ('dataground.publication-authorization-decision/v1',$1,$2,$3,$4,'publisher',$5,$6,1,$7,$8,$8,$9,'publication-policy',$8,$10)`
	args := func(phase string, token int64, contract, outcome string) []any {
		return []any{identity.New("iso"), identity.New("svc"), identity.New("rev"), identity.New("op"), identity.New("cor"), phase, token, "sha256:" + strings.Repeat("a", 64), contract, outcome}
	}
	for _, contract := range []string{"dataground.invocation-authorization-policy/v4", "dataground.invocation-authorization-policy/v5", "dataground.invocation-authorization-policy/v6"} {
		for _, outcome := range []string{"allowed", "denied"} {
			for _, phase := range []string{"entry", "effect"} {
				for _, token := range []int64{-1, 0, 1, 9007199254740991, 9007199254740992} {
					tx, err := pool.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					_, err = tx.Exec(ctx, insert, args(phase, token, contract, outcome)...)
					_ = tx.Rollback(ctx)
					valid := (phase == "entry" && token == 0 || phase == "effect" && token > 0 && token <= 9007199254740991) && (outcome != "allowed" || contract != "dataground.invocation-authorization-policy/v4")
					if (err == nil) != valid {
						t.Fatalf("unexpected constraint result for %s %s %s %d: %v", contract, outcome, phase, token, err)
					}
					if !valid {
						var pgErr *pgconn.PgError
						if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
							t.Fatal("expected check constraint denial", err)
						}
					}
				}
			}
		}
	}
	if _, err := pool.Exec(ctx, insert, args("entry", 0, "dataground.invocation-authorization-policy/v6", "allowed")...); err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateDownTo(ctx, db, 70); err == nil {
		t.Fatal("downgrade discarded source decision support")
	}
	if _, err := pool.Exec(ctx, `TRUNCATE publication_authorization_decisions`); err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateDownTo(ctx, db, 70); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, args("effect", 0, "dataground.invocation-authorization-policy/v5", "allowed")...); err == nil {
		t.Fatal("rollback removed fencing")
	}
	// Reproduce the actual schema-70 defect to verify upgrade of retained history.
	// Only this disposable database deliberately installs the old broken check.
	if _, err := pool.Exec(ctx, `ALTER TABLE publication_authorization_decisions DROP CONSTRAINT publication_authorization_decisions_check,
 ADD CONSTRAINT publication_authorization_decisions_check CHECK (outcome<>'allowed' OR policy_contract IN ('dataground.invocation-authorization-policy/v5','dataground.invocation-authorization-policy/v6'))`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, args("effect", 0, "dataground.invocation-authorization-policy/v5", "allowed")...); err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateUp(ctx, db); err == nil {
		t.Fatal("migration admitted invalid historical fencing")
	}
	var rows, version int
	if err := pool.QueryRow(ctx, `SELECT count(*),(SELECT max(version) FROM dataground_schema_migrations) FROM publication_authorization_decisions`).Scan(&rows, &version); err != nil || rows != 1 || version != 70 {
		t.Fatal("failed migration changed history", rows, version, err)
	}
	// Removal is test cleanup, never a production migration repair strategy.
	if _, err := pool.Exec(ctx, `TRUNCATE publication_authorization_decisions`); err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateUp(ctx, db); err != nil {
		t.Fatal(err)
	}
}
