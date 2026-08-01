package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/persistence"
)

func TestInvocationAuthorizationDecisionsAreAttributedAndAppendOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL := testDatabaseURL(t)
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 0); err != nil {
		database.Close()
		t.Fatalf("reset schema: %v", err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		database.Close()
		t.Fatalf("migrate schema: %v", err)
	}
	database.Close()
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	repository := persistence.NewRepository(pool)
	record := authz.InvocationDecisionRecord{
		ActorID:           "operator@example.invalid",
		IsolationDomainID: "iso_00000000000000000001",
		OperationID:       "op_00000000000000000001",
		InvocationID:      "inv_00000000000000000001",
		ServiceID:         "svc_00000000000000000001",
		RevisionID:        "rev_00000000000000000001",
		Action:            authz.InvocationRun,
		Outcome:           authz.OutcomeAllowed,
		PolicySetID:       "policy.integration",
		PolicyDigest:      "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		CorrelationID:     "cor_00000000000000000001",
	}
	if err := repository.RecordInvocationAuthorizationDecision(ctx, record); err != nil {
		t.Fatalf("record invocation decision: %v", err)
	}

	var actorID, action, outcome, policySetID, policyDigest, correlationID string
	if err := pool.QueryRow(ctx, `
		SELECT actor_id, action, outcome, policy_set_id, policy_digest, correlation_id
		FROM invocation_authorization_decisions
		WHERE isolation_domain_id = $1 AND operation_id = $2
	`, record.IsolationDomainID, record.OperationID).Scan(
		&actorID,
		&action,
		&outcome,
		&policySetID,
		&policyDigest,
		&correlationID,
	); err != nil {
		t.Fatalf("read invocation decision: %v", err)
	}
	if actorID != record.ActorID ||
		action != string(record.Action) ||
		outcome != string(record.Outcome) ||
		policySetID != record.PolicySetID ||
		policyDigest != record.PolicyDigest ||
		correlationID != record.CorrelationID {
		t.Fatalf("persisted decision = %q %q %q %q %q %q", actorID, action, outcome, policySetID, policyDigest, correlationID)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE invocation_authorization_decisions
		SET outcome = 'denied'
		WHERE isolation_domain_id = $1 AND operation_id = $2
	`, record.IsolationDomainID, record.OperationID); err == nil {
		t.Fatal("invocation authorization decision update was accepted")
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM invocation_authorization_decisions
		WHERE isolation_domain_id = $1 AND operation_id = $2
	`, record.IsolationDomainID, record.OperationID); err == nil {
		t.Fatal("invocation authorization decision deletion was accepted")
	}
}
