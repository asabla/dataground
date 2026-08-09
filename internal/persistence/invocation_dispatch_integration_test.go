package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestGovernedInvocationDispatchIsExactAtomicAndOptIn(t *testing.T) {
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

	domainID := identity.New("iso")
	serviceID := identity.New("svc")
	revisionID := identity.New("rev")
	referenceRevisionID := identity.New("rev")
	actorID := "dispatch-integration"
	if _, err := repository.CreateService(ctx, testIdempotency(domainID, "dispatch-create-service"), persistence.CreateServiceInput{
		ID: serviceID, Name: "governed-dispatch", ActorID: actorID, CorrelationID: identity.New("cor"),
	}); err != nil {
		t.Fatal(err)
	}
	for _, revision := range []struct {
		id      string
		profile string
	}{
		{id: revisionID, profile: "codex.app-server/v1"},
		{id: referenceRevisionID, profile: "reference/v1"},
	} {
		if _, err := repository.CreateRevision(ctx, testIdempotency(domainID, "dispatch-create-"+revision.id), persistence.CreateRevisionInput{
			ID: revision.id, ServiceID: serviceID, RuntimeProfile: revision.profile,
			ActorID: actorID, CorrelationID: identity.New("cor"),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE service_revisions
			SET state = 'published'
			WHERE isolation_domain_id = $1 AND id = $2
		`, domainID, revision.id); err != nil {
			t.Fatal(err)
		}
	}
	for alias, targetRevisionID := range map[string]string{
		"governed":  revisionID,
		"reference": referenceRevisionID,
	} {
		if _, err := repository.AssignAlias(ctx, testIdempotency(domainID, "dispatch-alias-"+alias), persistence.AssignAliasInput{
			ID: identity.New("als"), ServiceID: serviceID, Name: alias,
			RevisionID: targetRevisionID, ActorID: actorID, CorrelationID: identity.New("cor"),
		}); err != nil {
			t.Fatal(err)
		}
	}

	target := persistence.InvocationDispatchTarget{
		IsolationDomainID: domainID,
		ServiceID:         serviceID,
		RevisionID:        revisionID,
		RuntimeProfile:    "codex.app-server/v1",
	}
	if err := repository.RequireInvocationDispatchTarget(ctx, target); err != nil {
		t.Fatalf("require exact governed target: %v", err)
	}
	mismatches := []struct {
		name      string
		domainID  string
		serviceID string
		alias     string
		target    persistence.InvocationDispatchTarget
	}{
		{name: "domain", domainID: identity.New("iso"), serviceID: serviceID, alias: "governed", target: target},
		{name: "service", domainID: domainID, serviceID: identity.New("svc"), alias: "governed", target: target},
		{name: "revision", domainID: domainID, serviceID: serviceID, alias: "reference", target: target},
		{name: "runtime", domainID: domainID, serviceID: serviceID, alias: "reference", target: persistence.InvocationDispatchTarget{
			IsolationDomainID: domainID, ServiceID: serviceID, RevisionID: referenceRevisionID, RuntimeProfile: "codex.app-server/v1",
		}},
	}
	for _, mismatch := range mismatches {
		invocationID := identity.New("inv")
		_, err := repository.AcceptInvocation(ctx, testIdempotency(mismatch.domainID, "dispatch-mismatch-"+mismatch.name), persistence.AcceptInvocationInput{
			ID: invocationID, ServiceID: mismatch.serviceID, Alias: mismatch.alias,
			Input: map[string]any{"prompt": "do not dispatch"}, ActorID: actorID,
			CorrelationID: identity.New("cor"), Deadline: time.Now().Add(time.Minute),
			DispatchTarget: &mismatch.target,
		})
		var problem *persistence.DomainError
		if !errors.As(err, &problem) || problem.Code != "INVOCATION_DISPATCH_TARGET_MISMATCH" {
			t.Fatalf("%s mismatch error = %v", mismatch.name, err)
		}
		var invocations, operations, idempotencyRecords int
		if err := pool.QueryRow(ctx, `
			SELECT
			  (SELECT count(*) FROM invocations WHERE id = $1),
			  (SELECT count(*) FROM invocation_execution_operations WHERE invocation_id = $1),
			  (SELECT count(*) FROM idempotency_records WHERE request_path = $2)
		`, invocationID, "/integration/dispatch-mismatch-"+mismatch.name).Scan(
			&invocations, &operations, &idempotencyRecords,
		); err != nil {
			t.Fatal(err)
		}
		if invocations != 0 || operations != 0 || idempotencyRecords != 0 {
			t.Fatalf("%s mismatch committed state = invocations:%d operations:%d idempotency:%d", mismatch.name, invocations, operations, idempotencyRecords)
		}
	}

	accepted, err := repository.AcceptInvocation(ctx, testIdempotency(domainID, "dispatch-governed"), persistence.AcceptInvocationInput{
		ID: identity.New("inv"), ServiceID: serviceID, Alias: "governed",
		Input: map[string]any{"prompt": "run governed"}, ActorID: actorID,
		CorrelationID: identity.New("cor"), Deadline: time.Now().Add(time.Minute),
		DispatchTarget: &target,
	})
	if err != nil || accepted.Status != http.StatusAccepted {
		t.Fatalf("governed dispatch = (%d, %v)", accepted.Status, err)
	}
	var governedInvocation domain.Invocation
	if err := json.Unmarshal(accepted.Body, &governedInvocation); err != nil {
		t.Fatal(err)
	}
	if governedInvocation.RevisionID != revisionID {
		t.Fatalf("governed invocation revision = %q", governedInvocation.RevisionID)
	}
	claim, err := repository.ClaimNextForRuntimeProfile(
		ctx,
		persistence.OperationKindInvocation,
		"reference/v1",
		"reference-worker",
		time.Minute,
	)
	if err != nil || claim != nil {
		t.Fatalf("reference worker claimed governed invocation = (%#v, %v)", claim, err)
	}

	referenceAccepted, err := repository.AcceptInvocation(ctx, testIdempotency(domainID, "dispatch-reference"), persistence.AcceptInvocationInput{
		ID: identity.New("inv"), ServiceID: serviceID, Alias: "reference",
		Input: map[string]any{"scenario": "success"}, ActorID: actorID,
		CorrelationID: identity.New("cor"), Deadline: time.Now().Add(time.Minute),
	})
	if err != nil || referenceAccepted.Status != http.StatusAccepted {
		t.Fatalf("unconstrained reference invocation = (%d, %v)", referenceAccepted.Status, err)
	}
	claim, err = repository.ClaimNextForRuntimeProfile(
		ctx,
		persistence.OperationKindInvocation,
		"reference/v1",
		"reference-worker",
		time.Minute,
	)
	if err != nil || claim == nil {
		t.Fatalf("reference worker did not claim reference invocation = (%#v, %v)", claim, err)
	}
	var referenceInvocation domain.Invocation
	if err := json.Unmarshal(referenceAccepted.Body, &referenceInvocation); err != nil {
		t.Fatal(err)
	}
	if claim.ResourceID != referenceInvocation.Metadata.ID || claim.ResourceID == governedInvocation.Metadata.ID {
		t.Fatalf("reference worker claim = %#v", claim)
	}
}
