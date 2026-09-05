package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/reference"
)

func TestDurablePublicationSchemaValidationAndUpgradeRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	scope, other, serviceID, actor := identity.New("iso"), identity.New("iso"), identity.New("svc"), identity.New("usr")
	for _, domainID := range []string{scope, other} {
		if _, err := repository.CreateService(ctx, testIdempotency(domainID, "schema-service"), persistence.CreateServiceInput{ID: serviceID, Name: "schema contracts", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
	}
	create := func(domainID, id string, input, output map[string]any) {
		t.Helper()
		if _, err := repository.CreateRevision(ctx, testIdempotency(domainID, "schema-create-"+id), persistence.CreateRevisionInput{ID: id, ServiceID: serviceID, RuntimeProfile: "reference/v1", InputSchema: input, OutputSchema: output, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
	}
	publish := func(domainID, id, key string) (persistence.CommandResult, error) {
		return repository.AcceptPublication(ctx, testIdempotency(domainID, key), persistence.AcceptPublicationInput{RevisionID: id, ExpectedVersion: 1, ActorID: actor, CorrelationID: "cor_schema-publication", Deadline: time.Now().UTC().Add(time.Hour)}, reference.Capabilities())
	}
	invalid := map[string]any{"$ref": "https://private.example/schema"}
	for _, output := range []bool{false, true} {
		id := identity.New("rev")
		if output {
			create(scope, id, nil, invalid)
		} else {
			create(scope, id, invalid, nil)
		}
		create(other, id, nil, nil)
		_, err := publish(scope, id, "invalid-publication-"+id)
		expected := "REVISION_INPUT_SCHEMA_INVALID"
		if output {
			expected = "REVISION_OUTPUT_SCHEMA_INVALID"
		}
		var problem *persistence.DomainError
		if !errors.As(err, &problem) || problem.Code != expected || strings.Contains(problem.Message, "private.example") {
			t.Fatalf("publication rejection = %v", err)
		}
		var count int
		for _, query := range []string{
			`SELECT count(*) FROM service_publication_operations WHERE isolation_domain_id=$1`,
			`SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND resource_type='service-publication'`,
			`SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND event_type='service-publication.accepted'`,
			`SELECT count(*) FROM idempotency_records WHERE isolation_domain_id=$1 AND idempotency_key LIKE 'invalid-publication-%'`,
		} {
			if err := pool.QueryRow(ctx, query, scope).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid publication wrote state: %d %v", count, err)
			}
		}
		accepted, err := publish(other, id, "valid-publication-"+id)
		if err != nil || accepted.Status != http.StatusAccepted {
			t.Fatalf("other scope rejected: %v", err)
		}
		replayed, err := publish(other, id, "valid-publication-"+id)
		if err != nil || !replayed.Replayed || string(replayed.Body) != string(accepted.Body) {
			t.Fatalf("publication replay changed: %v", err)
		}
	}
	// Model pre-upgrade acceptance by inserting an invalid contract after the
	// valid acceptance, then recover through a fresh repository and worker.
	id := identity.New("rev")
	create(scope, id, nil, nil)
	accepted, err := publish(scope, id, "legacy-publication")
	if err != nil {
		t.Fatal(err)
	}
	var operation domain.Operation
	if err := json.Unmarshal(accepted.Body, &operation); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE service_revisions SET output_schema=$3 WHERE isolation_domain_id=$1 AND id=$2`, scope, id, invalid); err != nil {
		t.Fatal(err)
	}
	repository = persistence.NewRepository(pool)
	worker := reconcile.New(repository, reconcile.NewReferenceDriver(pool), "schema-upgrade-worker")
	runToTerminal(t, ctx, worker, repository, scope, operation.Metadata.ID, "failed")
	operation, err = repository.GetOperation(ctx, scope, operation.Metadata.ID)
	if err != nil || operation.Error == nil || operation.Error.Code != "REVISION_OUTPUT_SCHEMA_INVALID" || operation.Error.CorrelationID == "" || operation.Error.Retryable {
		t.Fatalf("legacy failure = %+v %v", operation, err)
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2`, scope, id).Scan(&state); err != nil || state != "draft" {
		t.Fatalf("invalid revision became published: %s %v", state, err)
	}
	var effects int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM reference_runtime_receipts WHERE isolation_domain_id=$1`, scope).Scan(&effects); err != nil || effects != 0 {
		t.Fatalf("legacy queue applied effects: %d %v", effects, err)
	}
	for previous, next := range map[string]string{"validating": "applying", "applying": "observing", "observing": "published"} {
		revisionID := identity.New("rev")
		create(scope, revisionID, nil, nil)
		receipt, err := publish(scope, revisionID, "legacy-"+previous)
		if err != nil {
			t.Fatal(err)
		}
		var pending domain.Operation
		if err := json.Unmarshal(receipt.Body, &pending); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE service_publication_operations SET observed_state=$3 WHERE isolation_domain_id=$1 AND id=$2`, scope, pending.Metadata.ID, previous); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE service_revisions SET input_schema=$3 WHERE isolation_domain_id=$1 AND id=$2`, scope, revisionID, invalid); err != nil {
			t.Fatal(err)
		}
		claim, err := repository.ClaimNextInIsolationDomain(ctx, persistence.OperationKindPublication, scope, "schema-resume-worker", time.Minute)
		if err != nil || claim == nil || claim.ID != pending.Metadata.ID {
			t.Fatalf("resume claim = %+v %v", claim, err)
		}
		stale := *claim
		stale.FencingToken++
		if err := repository.Advance(ctx, stale, next, nil); !errors.Is(err, persistence.ErrLeaseLost) {
			t.Fatalf("stale claim changed schema failure: %v", err)
		}
		if err := repository.Advance(ctx, *claim, next, map[string]any{"private-result": "must not be retained"}); err != nil {
			t.Fatal(err)
		}
		failed, err := repository.GetOperation(ctx, scope, pending.Metadata.ID)
		if err != nil || failed.ObservedState != "failed" || failed.Error == nil || failed.Error.Code != "REVISION_INPUT_SCHEMA_INVALID" || len(failed.TerminalResult) != 0 {
			t.Fatalf("resume from %s = %+v %v", previous, failed, err)
		}
		if err := pool.QueryRow(ctx, `SELECT state FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2`, scope, revisionID).Scan(&state); err != nil || state != "draft" {
			t.Fatalf("resume published invalid revision: %s %v", state, err)
		}
	}

}
