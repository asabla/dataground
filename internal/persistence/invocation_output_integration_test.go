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
	"github.com/asabla/dataground/internal/reconcile"
)

func TestDurableInvocationOutputIsScopedClaimFencedAndTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	serviceID, revisionID, actor := identity.New("svc"), identity.New("rev"), identity.New("usr")
	for _, kind := range []string{"valid", "invalid-output", "invalid-schema", "absent", "governed-envelope"} {
		scope := identity.New("iso")
		if _, err := repository.CreateService(ctx, testIdempotency(scope, "output-service"), persistence.CreateServiceInput{ID: serviceID, Name: "output contract", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
		runtimeProfile := "reference/v1"
		var schema map[string]any
		switch kind {
		case "governed-envelope":
			runtimeProfile = persistence.GovernedInvocationRuntimeProfile
			schema = map[string]any{"type": "object", "required": []string{"answer"}, "properties": map[string]any{"answer": map[string]any{"type": "integer"}}, "additionalProperties": false}
		case "valid":
			schema = map[string]any{"type": "object", "required": []string{"status"}, "properties": map[string]any{"status": map[string]any{"const": "succeeded"}}}
		case "invalid-output":
			schema = map[string]any{"type": "object", "required": []string{"private-missing-field"}}
		case "invalid-schema":
			schema = map[string]any{"$ref": "https://private.example/schema"}
		}
		// Direct publication models a retained revision from before schema admission.
		if _, err := repository.CreateRevision(ctx, testIdempotency(scope, "output-revision"), persistence.CreateRevisionInput{ID: revisionID, ServiceID: serviceID, RuntimeProfile: runtimeProfile, OutputSchema: schema, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='published' WHERE isolation_domain_id=$1 AND id=$2`, scope, revisionID); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.AssignAlias(ctx, testIdempotency(scope, "output-route"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: "stable", RevisionID: revisionID, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
		input := persistence.AcceptInvocationInput{ID: identity.New("inv"), ServiceID: serviceID, Alias: "stable", Input: map[string]any{}, ActorID: actor, CorrelationID: identity.New("cor"), Deadline: time.Now().UTC().Add(time.Hour)}
		if kind == "governed-envelope" {
			input.DispatchTarget = &persistence.InvocationDispatchTarget{IsolationDomainID: scope, ServiceID: serviceID, RevisionID: revisionID, RuntimeProfile: runtimeProfile}
		}
		accepted, err := repository.AcceptInvocation(ctx, testIdempotency(scope, "output-invoke"), input)
		if err != nil {
			t.Fatal(err)
		}
		var invocation domain.Invocation
		if err := json.Unmarshal(accepted.Body, &invocation); err != nil {
			t.Fatal(err)
		}
		// Move the alias to a different contract before completion. Only the accepted
		// immutable revision may decide whether this invocation's output is valid.
		replacement := identity.New("rev")
		if _, err := repository.CreateRevision(ctx, testIdempotency(scope, "replacement-revision"), persistence.CreateRevisionInput{ID: replacement, ServiceID: serviceID, RuntimeProfile: "reference/v1", OutputSchema: map[string]any{"type": "string"}, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='published' WHERE isolation_domain_id=$1 AND id=$2`, scope, replacement); err != nil {
			t.Fatal(err)
		}
		version := 1
		if _, err := repository.AssignAlias(ctx, testIdempotency(scope, "move-route"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: "stable", RevisionID: replacement, ExpectedVersion: &version, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
		if kind == "governed-envelope" {
			// This persistence-only fixture represents a result already checked
			// and wrapped by the governed driver; it does not run a native turn.
			if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET observed_state='observing' WHERE isolation_domain_id=$1 AND id=$2`, scope, invocation.OperationID); err != nil {
				t.Fatal(err)
			}
		}
		worker := reconcile.New(repository, reconcile.NewReferenceDriver(pool), "output-worker")
		for attempt := 0; attempt < 10; attempt++ {
			op, err := repository.GetOperation(ctx, scope, invocation.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if op.ObservedState == "observing" {
				break
			}
			if _, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil {
				t.Fatal(err)
			}
		}
		// A new repository recovers after the runtime effect completed, but before
		// the public invocation result and terminal lifecycle transition committed.
		repository = persistence.NewRepository(pool)
		claim, err := repository.ClaimNextInIsolationDomain(ctx, persistence.OperationKindInvocation, scope, "output-recovery-worker", time.Minute)
		if err != nil || claim == nil || claim.ObservedState != "observing" {
			t.Fatalf("completion claim: %+v %v", claim, err)
		}
		output := map[string]any{"status": "succeeded", "private-output": "not a diagnostic"}
		if kind == "governed-envelope" {
			output = map[string]any{"status": "succeeded", "output": map[string]any{"answer": 42}}
		}
		stale := *claim
		stale.FencingToken++
		if err := repository.Advance(ctx, stale, "succeeded", output); !errors.Is(err, persistence.ErrLeaseLost) {
			t.Fatalf("stale completion: %v", err)
		}
		before, err := repository.GetInvocation(ctx, scope, invocation.Metadata.ID)
		if err != nil || before.State == "failed" || before.State == "succeeded" || before.Result != nil {
			t.Fatalf("stale completion changed result: %+v %v", before, err)
		}
		if err := repository.Advance(ctx, *claim, "succeeded", output); err != nil {
			t.Fatal(err)
		}
		result, err := repository.GetInvocation(ctx, scope, invocation.Metadata.ID)
		if err != nil {
			t.Fatal(err)
		}
		op, err := repository.GetOperation(ctx, scope, invocation.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		if kind == "valid" || kind == "absent" || kind == "governed-envelope" {
			if result.State != "succeeded" || result.Result["status"] != "succeeded" || result.RevisionID != revisionID || op.ObservedState != "succeeded" {
				t.Fatalf("valid output rejected: %+v %+v", result, op)
			}
		} else {
			code := "INVOCATION_OUTPUT_INVALID"
			if kind == "invalid-schema" {
				code = "REVISION_OUTPUT_SCHEMA_INVALID"
			}
			if result.State != "failed" || result.Result != nil || result.Error == nil || result.Error.Code != code || result.Error.CorrelationID != invocation.CorrelationID || result.Error.Retryable || result.CompletedAt == nil {
				t.Fatalf("output failure: %+v", result)
			}
			if op.ObservedState != "failed" || op.TerminalResult != nil || op.Error == nil || op.Error.Code != code {
				t.Fatalf("operation failure: %+v", op)
			}
			encoded, _ := json.Marshal(result.Error)
			if strings.Contains(string(encoded), "private") {
				t.Fatal("validator details leaked")
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2 AND event_type='lifecycle.succeeded'`, scope, invocation.Metadata.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed output recorded success: %d %v", count, err)
			}
		}
		if err := repository.Advance(ctx, *claim, "succeeded", output); !errors.Is(err, persistence.ErrLeaseLost) {
			t.Fatalf("duplicate completion rewrote terminal state: %v", err)
		}
		replay, err := repository.AcceptInvocation(ctx, testIdempotency(scope, "output-invoke"), input)
		if err != nil || !replay.Replayed || string(replay.Body) != string(accepted.Body) {
			t.Fatalf("acceptance replay changed: %v", err)
		}
	}
}
