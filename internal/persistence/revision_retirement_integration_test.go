package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDurableRevisionRetirementPreservesHistoryAndFencesRepair(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer func() {
		// Only the dedicated test database is used. Production downgrade must retain
		// this evidence; clear these verified fixtures before the next migration test.
		if _, err := pool.Exec(context.Background(), `TRUNCATE audit_records,api_authorization_decisions`); err != nil {
			t.Error(err)
		}
		pool.Close()
	}()
	repository := persistence.NewRepository(pool)
	domainID, otherDomain := identity.New("iso"), identity.New("iso")
	serviceID, revisionID, nextRevisionID := identity.New("svc"), identity.New("rev"), identity.New("rev")
	actor := "retirement-operator"
	for _, scope := range []string{domainID, otherDomain} {
		if _, err := repository.CreateService(ctx, testIdempotency(scope, "retire-service"), persistence.CreateServiceInput{ID: serviceID, Name: "retirement", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{revisionID, nextRevisionID} {
			if _, err := repository.CreateRevision(ctx, testIdempotency(scope, "create-"+id), persistence.CreateRevisionInput{ID: id, ServiceID: serviceID, RuntimeProfile: "reference/v1", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='published',published_at=clock_timestamp() WHERE isolation_domain_id=$1 AND id=$2`, scope, id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := repository.AssignAlias(ctx, testIdempotency(scope, "retire-alias"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: "stable", RevisionID: revisionID, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
	}
	input := persistence.RetireRevisionInput{RevisionID: revisionID, ExpectedVersion: 1, ActorID: actor, CorrelationID: identity.New("cor")}
	reject := func(key, code string) {
		t.Helper()
		_, err := repository.RetireRevision(ctx, testIdempotency(domainID, key), input)
		requireRetirementCode(t, err, code)
	}
	reject("retire-routed", "REVISION_STILL_ROUTED")
	invocationID := identity.New("inv")
	accepted, err := repository.AcceptInvocation(ctx, testIdempotency(domainID, "retire-invoke"), persistence.AcceptInvocationInput{ID: invocationID, ServiceID: serviceID, Alias: "stable", Input: map[string]any{"prompt": "retained content"}, ActorID: actor, CorrelationID: identity.New("cor"), Deadline: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var invocation domain.Invocation
	if err := json.Unmarshal(accepted.Body, &invocation); err != nil {
		t.Fatal(err)
	}
	version := 1
	if _, err := repository.AssignAlias(ctx, testIdempotency(domainID, "retire-move"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: "stable", RevisionID: nextRevisionID, ExpectedVersion: &version, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	reject("retire-invocation-active", "REVISION_STILL_ACTIVE")
	if _, err := pool.Exec(ctx, `UPDATE invocations SET state='failed',completed_at=clock_timestamp() WHERE isolation_domain_id=$1 AND id=$2`, domainID, invocationID); err != nil {
		t.Fatal(err)
	}
	reject("retire-operation-active", "REVISION_STILL_ACTIVE")
	failOperation := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET observed_state='failed' WHERE isolation_domain_id=$1 AND id=$2`, domainID, invocation.OperationID); err != nil {
			t.Fatal(err)
		}
	}
	failOperation()
	if err := repository.RepairOperation(ctx, persistence.OperationKindInvocation, domainID, invocation.OperationID, actor, "retry failed run", "retire-repair-before", time.Now().UTC().Truncate(time.Microsecond).Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	reject("retire-repair-queued", "REVISION_STILL_ACTIVE")
	failOperation()
	result, err := repository.RetireRevision(ctx, testIdempotency(domainID, "retire-final"), input)
	if err != nil {
		t.Fatal(err)
	}
	var retired domain.ServiceRevision
	if err := json.Unmarshal(result.Body, &retired); err != nil {
		t.Fatal(err)
	}
	if retired.State != "retired" || retired.Metadata.Version != 2 || retired.PublishedAt == nil {
		t.Fatal("retirement lost revision history")
	}
	replay, err := persistence.NewRepository(pool).RetireRevision(ctx, testIdempotency(domainID, "retire-final"), input)
	if err != nil || !replay.Replayed || string(replay.Body) != string(result.Body) {
		t.Fatalf("restart replay=%v", err)
	}
	if err := repository.RepairOperation(ctx, persistence.OperationKindInvocation, domainID, invocation.OperationID, actor, "retry retired run", "retire-repair-after", time.Now().UTC().Add(time.Hour)); err == nil {
		t.Fatal("retirement permitted operation repair")
	} else {
		requireRetirementCode(t, err, "OPERATION_NOT_REPAIRABLE")
	}
	// An exact historical repair receipt remains readable after retirement.
	var priorDeadline time.Time
	if err := pool.QueryRow(ctx, `SELECT deadline_at FROM invocation_execution_operations WHERE isolation_domain_id=$1 AND id=$2`, domainID, invocation.OperationID).Scan(&priorDeadline); err != nil {
		t.Fatal(err)
	}
	if err := repository.RepairOperation(ctx, persistence.OperationKindInvocation, domainID, invocation.OperationID, actor, "retry failed run", "retire-repair-before", priorDeadline); err != nil {
		t.Fatal(err)
	}
	var state, otherState, prompt string
	if err := pool.QueryRow(ctx, `SELECT r.state,i.input->>'prompt' FROM service_revisions r JOIN invocations i ON i.isolation_domain_id=r.isolation_domain_id AND i.revision_id=r.id WHERE r.isolation_domain_id=$1 AND r.id=$2`, domainID, revisionID).Scan(&state, &prompt); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2`, otherDomain, revisionID).Scan(&otherState); err != nil {
		t.Fatal(err)
	}
	if state != "retired" || prompt != "retained content" || otherState != "published" {
		t.Fatal("retirement crossed isolation scope or removed content")
	}
	for _, query := range []string{`SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='service-revision.retired'`, `SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND event_type='service-revision.retired'`} {
		var count int
		if err := pool.QueryRow(ctx, query, domainID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("retirement receipt count=%d", count)
		}
	}
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 45); err == nil || !strings.Contains(err.Error(), "cannot remove revision retirement evidence") {
		t.Fatalf("retirement downgrade=%v", err)
	}
}

func TestRevisionRetirementLockPreventsStaleAliasAndRepair(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	domainID, serviceID, revisionID := identity.New("iso"), identity.New("svc"), identity.New("rev")
	if _, err := repository.CreateService(ctx, testIdempotency(domainID, "retire-race-service"), persistence.CreateServiceInput{ID: serviceID, Name: "race", ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateRevision(ctx, testIdempotency(domainID, "retire-race-revision"), persistence.CreateRevisionInput{ID: revisionID, ServiceID: serviceID, RuntimeProfile: "reference/v1", ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='published' WHERE isolation_domain_id=$1 AND id=$2`, domainID, revisionID); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.AssignAlias(ctx, testIdempotency(domainID, "retire-race-existing"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: "existing", RevisionID: revisionID, ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	invocationID := identity.New("inv")
	accepted, err := repository.AcceptInvocation(ctx, testIdempotency(domainID, "retire-race-invoke"), persistence.AcceptInvocationInput{ID: invocationID, ServiceID: serviceID, Alias: "existing", Input: map[string]any{}, ActorID: "operator", CorrelationID: identity.New("cor"), Deadline: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var invocation domain.Invocation
	if err := json.Unmarshal(accepted.Body, &invocation); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET observed_state='failed' WHERE isolation_domain_id=$1 AND id=$2`, domainID, invocation.OperationID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE service_revisions SET state='retired' WHERE isolation_domain_id=$1 AND id=$2`, domainID, revisionID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := repository.AssignAlias(ctx, testIdempotency(domainID, "retire-race-alias"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: "stable", RevisionID: revisionID, ActorID: "operator", CorrelationID: identity.New("cor")})
		done <- err
	}()

	repairDone := make(chan error, 1)
	go func() {
		repairDone <- repository.RepairOperation(ctx, persistence.OperationKindInvocation, domainID, invocation.OperationID, "operator", "retry", "retire-race-repair", time.Now().UTC().Add(time.Hour))
	}()
	waitForRetirementLock(t, ctx, pool)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	requireRetirementCode(t, <-done, "REVISION_NOT_PUBLISHED")
	requireRetirementCode(t, <-repairDone, "OPERATION_NOT_REPAIRABLE")
}

func requireRetirementCode(t *testing.T, err error, code string) {
	t.Helper()
	var problem *persistence.DomainError
	if !errors.As(err, &problem) || problem.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}
func waitForRetirementLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT count(*) >= 2 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FOR SHARE%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("revision mutation did not wait for the retirement lock")
}
