package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

func TestDurableAliasWithdrawalPreservesScopeReplayAndHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer func() {
		// Only the disposable test database is cleared after downgrade protection is checked.
		if _, err := pool.Exec(context.Background(), `UPDATE service_aliases SET withdrawn_at=NULL; TRUNCATE audit_records,api_authorization_decisions`); err != nil {
			t.Error(err)
		}
		pool.Close()
	}()
	repository := persistence.NewRepository(pool)
	scope, other, serviceID, revisionID, aliasID := identity.New("iso"), identity.New("iso"), identity.New("svc"), identity.New("rev"), identity.New("als")
	actor := "withdrawal-operator"
	for _, domainID := range []string{scope, other} {
		if _, err := repository.CreateService(ctx, testIdempotency(domainID, "withdraw-service"), persistence.CreateServiceInput{ID: serviceID, Name: "withdrawal", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.CreateRevision(ctx, testIdempotency(domainID, "withdraw-revision"), persistence.CreateRevisionInput{ID: revisionID, ServiceID: serviceID, RuntimeProfile: "reference/v1", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='published',published_at=clock_timestamp() WHERE isolation_domain_id=$1 AND id=$2`, domainID, revisionID); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.AssignAlias(ctx, testIdempotency(domainID, "withdraw-route"), persistence.AssignAliasInput{ID: aliasID, ServiceID: serviceID, Name: "stable", RevisionID: revisionID, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
	}
	// Exercise the durable HTTP route and its new audited action before the
	// lifecycle assertions below, using an independent alias in the same scope.
	if _, err := repository.AssignAlias(ctx, testIdempotency(scope, "withdraw-http-route"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: "http", RevisionID: revisionID, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	const token = "withdrawal-http-test-bearer-with-thirty-two-bytes"
	principal := identity.New("usr")
	authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: principal, IsolationDomainID: scope})
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := authz.NewDevelopmentCedarAuthorizer(principal, scope)
	if err != nil {
		t.Fatal(err)
	}
	audited, err := authz.NewAuditedAuthorizer(authorizer, repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := api.NewDurableHandler(repository, authenticator, audited)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/isolation-domains/"+scope+"/agent-services/"+serviceID+"/aliases/http/actions/withdraw", strings.NewReader(`{"expectedVersion":1}`))
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Idempotency-Key", "withdraw-http-command")
	httpResponse := httptest.NewRecorder()
	handler.ServeHTTP(httpResponse, httpRequest)
	var httpReceipt domain.ServiceAlias
	if err := json.Unmarshal(httpResponse.Body.Bytes(), &httpReceipt); err != nil || httpResponse.Code != http.StatusOK || httpReceipt.WithdrawnAt == nil {
		t.Fatalf("durable HTTP withdrawal=%d %s", httpResponse.Code, httpResponse.Body.String())
	}
	var decisions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_authorization_decisions WHERE isolation_domain_id=$1 AND action='withdrawServiceAlias'`, scope).Scan(&decisions); err != nil || decisions != 1 {
		t.Fatalf("withdrawal decision was not audited: %d %v", decisions, err)
	}

	input := persistence.WithdrawAliasInput{ServiceID: serviceID, Name: "stable", ExpectedVersion: 1, ActorID: actor, CorrelationID: identity.New("cor")}
	wrong := input
	wrong.ExpectedVersion = 2
	if _, err := repository.WithdrawAlias(ctx, testIdempotency(scope, "withdraw-stale"), wrong); err == nil {
		t.Fatal("stale withdrawal accepted")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='service-alias.withdrawn' AND resource_id=$2`, scope, aliasID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale withdrawal wrote audit: %d %v", count, err)
	}
	invocationInput := persistence.AcceptInvocationInput{ID: identity.New("inv"), ServiceID: serviceID, Alias: "stable", Input: map[string]any{"prompt": "preserve"}, ActorID: actor, CorrelationID: identity.New("cor"), Deadline: time.Now().UTC().Add(time.Hour)}
	accepted, err := repository.AcceptInvocation(ctx, testIdempotency(scope, "withdraw-accepted"), invocationInput)
	if err != nil {
		t.Fatal(err)
	}
	var invocation domain.Invocation
	if err := json.Unmarshal(accepted.Body, &invocation); err != nil {
		t.Fatal(err)
	}
	withdrawn, err := repository.WithdrawAlias(ctx, testIdempotency(scope, "withdraw-command"), input)
	if err != nil {
		t.Fatal(err)
	}
	var receipt domain.ServiceAlias
	if err := json.Unmarshal(withdrawn.Body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.WithdrawnAt == nil || receipt.Metadata.Version != 2 || receipt.Metadata.ID != aliasID {
		t.Fatal("withdrawal lost version or identity")
	}
	if _, err := repository.GetServiceAlias(ctx, scope, serviceID, "stable"); err == nil {
		t.Fatal("withdrawn route visible")
	}
	if _, err := repository.AcceptInvocation(ctx, testIdempotency(scope, "withdraw-new-invocation"), invocationInput); err == nil {
		t.Fatal("withdrawn route invoked")
	}
	if active, err := repository.GetServiceAlias(ctx, other, serviceID, "stable"); err != nil || active.Metadata.Version != 1 {
		t.Fatal("withdrawal crossed isolation scope")
	}
	repository = persistence.NewRepository(pool)
	replayed, err := repository.AcceptInvocation(ctx, testIdempotency(scope, "withdraw-accepted"), invocationInput)
	if err != nil || !replayed.Replayed || string(replayed.Body) != string(accepted.Body) {
		t.Fatal("withdrawal broke historical invocation replay")
	}
	zero := 0
	reassigned, err := repository.AssignAlias(ctx, testIdempotency(scope, "withdraw-recreate"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: "stable", RevisionID: revisionID, ExpectedVersion: &zero, ActorID: actor, CorrelationID: identity.New("cor")})
	if err != nil {
		t.Fatal(err)
	}
	var active domain.ServiceAlias
	if err := json.Unmarshal(reassigned.Body, &active); err != nil {
		t.Fatal(err)
	}
	if active.WithdrawnAt != nil || active.Metadata.ID != aliasID || active.Metadata.Version != 3 {
		t.Fatal("recreation reset alias identity")
	}
	if _, err := repository.WithdrawAlias(ctx, testIdempotency(scope, "withdraw-stale-after-reuse"), input); err == nil {
		t.Fatal("stale client withdrew recreated alias")
	}
	replayed, err = repository.WithdrawAlias(ctx, testIdempotency(scope, "withdraw-command"), input)
	if err != nil || !replayed.Replayed || string(replayed.Body) != string(withdrawn.Body) {
		t.Fatal("withdrawal replay changed")
	}
	if active, err := repository.GetServiceAlias(ctx, scope, serviceID, "stable"); err != nil || active.Metadata.Version != 3 {
		t.Fatal("replay changed current routing")
	}
	input.ExpectedVersion = 3
	input.CorrelationID = identity.New("cor")
	if _, err := repository.WithdrawAlias(ctx, testIdempotency(scope, "withdraw-final"), input); err != nil {
		t.Fatal(err)
	}
	retirement := persistence.RetireRevisionInput{RevisionID: revisionID, ExpectedVersion: 1, ActorID: actor, CorrelationID: identity.New("cor")}
	_, err = repository.RetireRevision(ctx, testIdempotency(scope, "withdraw-retire-active"), retirement)
	var problem *persistence.DomainError
	if !errors.As(err, &problem) || problem.Code != "REVISION_STILL_ACTIVE" {
		t.Fatalf("withdrawal lost active work: %v", err)
	}
	if _, err := repository.AcceptCancellation(ctx, testIdempotency(scope, "withdraw-cancel"), persistence.AcceptCancellationInput{InvocationID: invocation.Metadata.ID, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	worker := reconcile.New(repository, reconcile.NewReferenceDriver(pool), "withdrawal-worker")
	runToTerminal(t, ctx, worker, repository, scope, invocation.OperationID, "cancelled")
	if _, err := repository.RetireRevision(ctx, testIdempotency(scope, "withdraw-retire-last"), retirement); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='service-alias.withdrawn' AND resource_id=$2`,
		`SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND event_type='service-alias.withdrawn' AND aggregate_id=$2`,
	} {
		if err := pool.QueryRow(ctx, query, scope, aliasID).Scan(&count); err != nil || count != 2 {
			t.Fatalf("withdrawal receipt count %d %v", count, err)
		}
	}
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 46); err == nil || !strings.Contains(err.Error(), "cannot remove alias withdrawal evidence") {
		t.Fatalf("withdrawal evidence downgraded: %v", err)
	}
}

func TestDurableAliasWithdrawalSerializesWithAdmissionAndReassignment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer func() {
		if _, err := pool.Exec(context.Background(), `UPDATE service_aliases SET withdrawn_at=NULL; TRUNCATE audit_records,api_authorization_decisions`); err != nil {
			t.Error(err)
		}
		pool.Close()
	}()
	repository := persistence.NewRepository(pool)
	scope, serviceID, revisionID := identity.New("iso"), identity.New("svc"), identity.New("rev")
	actor := "withdrawal-racer"
	if _, err := repository.CreateService(ctx, testIdempotency(scope, "race-service"), persistence.CreateServiceInput{ID: serviceID, Name: "race", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateRevision(ctx, testIdempotency(scope, "race-revision"), persistence.CreateRevisionInput{ID: revisionID, ServiceID: serviceID, RuntimeProfile: "reference/v1", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE service_revisions SET state='published' WHERE isolation_domain_id=$1 AND id=$2`, scope, revisionID); err != nil {
		t.Fatal(err)
	}
	for index, name := range []string{"admission", "assignment"} {
		if _, err := repository.AssignAlias(ctx, testIdempotency(scope, "race-route-"+name), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: name, RevisionID: revisionID, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		results := make([]error, 2)
		go func() {
			defer wait.Done()
			<-start
			_, results[0] = repository.WithdrawAlias(ctx, testIdempotency(scope, "race-withdraw-"+name), persistence.WithdrawAliasInput{ServiceID: serviceID, Name: name, ExpectedVersion: 1, ActorID: actor, CorrelationID: identity.New("cor")})
		}()
		go func() {
			defer wait.Done()
			<-start
			if index == 0 {
				_, results[1] = repository.AcceptInvocation(ctx, testIdempotency(scope, "race-invoke"), persistence.AcceptInvocationInput{ID: identity.New("inv"), ServiceID: serviceID, Alias: name, Input: map[string]any{}, ActorID: actor, CorrelationID: identity.New("cor"), Deadline: time.Now().UTC().Add(time.Hour)})
			} else {
				version := 1
				_, results[1] = repository.AssignAlias(ctx, testIdempotency(scope, "race-reassign"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: serviceID, Name: name, RevisionID: revisionID, ExpectedVersion: &version, ActorID: actor, CorrelationID: identity.New("cor")})
			}
		}()
		close(start)
		wait.Wait()
		for _, err := range results {
			var problem *persistence.DomainError
			if err != nil && !errors.As(err, &problem) {
				t.Fatalf("concurrent operation failed outside domain contract: %v", err)
			}
		}
		if index == 0 {
			if results[0] != nil {
				t.Fatal("admission prevented withdrawal")
			}
			if _, err := repository.AcceptInvocation(ctx, testIdempotency(scope, "race-after-withdraw"), persistence.AcceptInvocationInput{ID: identity.New("inv"), ServiceID: serviceID, Alias: name, Input: map[string]any{}, ActorID: actor, CorrelationID: identity.New("cor"), Deadline: time.Now().UTC().Add(time.Hour)}); err == nil {
				t.Fatal("post-withdrawal invocation accepted")
			}
		} else if (results[0] == nil) == (results[1] == nil) {
			t.Fatalf("competing version updates did not have one winner: %v", results)
		}
	}
}
