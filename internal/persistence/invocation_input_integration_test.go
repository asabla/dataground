package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestDurableInvocationInputContractIsAtomicScopedAndReplayable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	repository := persistence.NewRepository(pool)
	domainID, otherDomain := identity.New("iso"), identity.New("iso")
	serviceID, revisionID, nextID, invalidID := identity.New("svc"), identity.New("rev"), identity.New("rev"), identity.New("rev")
	actorID := identity.New("usr")
	schema := func(property, kind string) map[string]any {
		return map[string]any{"type": "object", "required": []string{property}, "properties": map[string]any{property: map[string]any{"type": kind}}, "additionalProperties": false}
	}
	createRevision := func(scope, id string, contract map[string]any) {
		t.Helper()
		_, err := repository.CreateRevision(ctx, testIdempotency(scope, "input-create-"+id), persistence.CreateRevisionInput{ID: id, ServiceID: serviceID, RuntimeProfile: "reference/v1", InputSchema: contract, ActorID: actorID, CorrelationID: identity.New("cor")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='published' WHERE isolation_domain_id=$1 AND id=$2`, scope, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []string{domainID, otherDomain} {
		if _, err := repository.CreateService(ctx, testIdempotency(scope, "input-service"), persistence.CreateServiceInput{ID: serviceID, Name: "input contract", ActorID: actorID, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
		contract := schema("prompt", "string")
		if scope == otherDomain {
			contract = schema("count", "integer")
		}
		createRevision(scope, revisionID, contract)
		if _, err := repository.AssignAlias(ctx, testIdempotency(scope, "input-route"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: "stable", RevisionID: revisionID, ActorID: actorID, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
	}
	createRevision(domainID, nextID, schema("count", "integer"))
	createRevision(domainID, invalidID, map[string]any{"$ref": "https://private.example/schema"})
	if _, err := repository.AssignAlias(ctx, testIdempotency(domainID, "input-invalid-route"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: "invalid", RevisionID: invalidID, ActorID: actorID, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	input := persistence.AcceptInvocationInput{ID: identity.New("inv"), ServiceID: serviceID, Alias: "stable", Input: map[string]any{"prompt": 42}, ActorID: actorID, CorrelationID: identity.New("cor"), Deadline: time.Now().UTC().Add(time.Hour)}
	_, err := repository.AcceptInvocation(ctx, testIdempotency(domainID, "input-invalid-value"), input)
	requireInputContractCode(t, err, "INVOCATION_INPUT_INVALID")
	input.Alias = "invalid"
	_, err = repository.AcceptInvocation(ctx, testIdempotency(domainID, "input-invalid-schema"), input)
	requireInputContractCode(t, err, "REVISION_INPUT_SCHEMA_INVALID")
	for _, table := range []string{"invocations", "invocation_execution_operations", "invocation_events"} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE isolation_domain_id=$1", domainID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("invalid input changed %s: count=%d err=%v", table, count, err)
		}
	}
	for _, query := range []string{
		`SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND resource_type='invocation'`,
		`SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND event_type='invocation.accepted'`,
		`SELECT count(*) FROM idempotency_records WHERE isolation_domain_id=$1 AND idempotency_key IN ('input-invalid-value','input-invalid-schema')`,
	} {
		var count int
		if err := pool.QueryRow(ctx, query, domainID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("invalid input committed a receipt: count=%d err=%v", count, err)
		}
	}
	input.Alias = "stable"
	input.Input = map[string]any{"prompt": "private-input"}
	accepted, err := repository.AcceptInvocation(ctx, testIdempotency(domainID, "input-accepted"), input)
	if err != nil || accepted.Status != http.StatusAccepted {
		t.Fatalf("valid input rejected: %v", err)
	}
	_, err = repository.AcceptInvocation(ctx, testIdempotency(otherDomain, "input-other-scope"), input)
	requireInputContractCode(t, err, "INVOCATION_INPUT_INVALID")
	version := 1
	if _, err := repository.AssignAlias(ctx, testIdempotency(domainID, "input-move-route"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: "stable", RevisionID: nextID, ExpectedVersion: &version, ActorID: actorID, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	replayed, err := persistence.NewRepository(pool).AcceptInvocation(ctx, testIdempotency(domainID, "input-accepted"), input)
	if err != nil || !replayed.Replayed || string(replayed.Body) != string(accepted.Body) {
		t.Fatalf("accepted replay changed after routing moved: %v", err)
	}
	_, err = repository.AcceptInvocation(ctx, testIdempotency(domainID, "input-new-attempt"), input)
	requireInputContractCode(t, err, "INVOCATION_INPUT_INVALID")
	input.ID = identity.New("inv")
	input.Input = map[string]any{"count": 2}
	validNext, err := repository.AcceptInvocation(ctx, testIdempotency(domainID, "input-next-valid"), input)
	if err != nil {
		t.Fatal(err)
	}
	var invocation domain.Invocation
	if json.Unmarshal(validNext.Body, &invocation) != nil || invocation.RevisionID != nextID {
		t.Fatal("input validation did not bind the accepted revision")
	}

	const token = "input-contract-test-bearer-with-at-least-thirty-two-bytes"
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: actorID, IsolationDomainID: domainID})
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := authz.NewDevelopmentCedarAuthorizer(actorID, domainID)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(repository, authenticator, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	for alias, expected := range map[string]int{"stable": http.StatusBadRequest, "invalid": http.StatusConflict} {
		request := httptest.NewRequest(http.MethodPost, "/v1/isolation-domains/"+domainID+"/agent-services/"+serviceID+"/invocations", strings.NewReader(`{"alias":"`+alias+`","input":{"private-field":"private-value"}}`))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "input-http-"+alias)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var envelope api.ErrorEnvelope
		if json.Unmarshal(response.Body.Bytes(), &envelope) != nil || response.Code != expected || envelope.Error.CorrelationID == "" || envelope.Error.Retryable || strings.Contains(response.Body.String(), "private-") || strings.Contains(response.Body.String(), "private.example") {
			t.Fatalf("durable input rejection = %d %s", response.Code, response.Body.String())
		}
	}
}

func requireInputContractCode(t *testing.T, err error, code string) {
	t.Helper()
	var problem *persistence.DomainError
	if !errors.As(err, &problem) || problem.Code != code || problem.Retryable {
		t.Fatalf("input contract rejection = %v, want %s", err, code)
	}
}
