package persistence_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/api"
	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

func TestAuthorizedPublicationAPIToFencedCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	url := testDatabaseURL(t)
	db, err := persistence.OpenSQL(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := persistence.MigrateUp(ctx, db); err != nil {
		t.Fatal(err)
	}
	pool, err := persistence.OpenPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := persistence.NewRepository(pool)
	store := executionpostgres.New(pool)
	defer func() {
		// Only the dedicated test database removes retained evidence, after checking
		// that the production downgrade and mutation guards reject its removal.
		if _, err := pool.Exec(context.Background(), `TRUNCATE publication_authorization_decisions,governed_publication_requests;
   DELETE FROM service_publication_operations WHERE state_machine_version=4;
   TRUNCATE api_authorization_decisions,audit_records,invocation_authorization_entity_activations,invocation_authorization_entity_generations,
   invocation_authorization_policy_withdrawals,invocation_authorization_policies,provider_credential_authorization_decisions,provider_credential_grant_events`); err != nil {
			t.Error(err)
		}
	}()
	source, err := reconcile.NewDurableInvocationAuthorizationPolicySource(repo)
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := reconcile.NewPublicationAuthorizer(source, repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "entry denied", "effect denied", "withdrawn", "audit failure", "invalid input schema", "invalid output schema"} {
		t.Run(mode, func(t *testing.T) {
			policy := `permit(principal, action, resource);`
			if mode == "entry denied" {
				policy = `forbid(principal,action,resource);`
			}
			if mode == "effect denied" {
				policy = `permit(principal,action==DataGround::Action::"publish",resource) when {context.phase=="entry"};`
			}
			f := newDevelopmentPublicationFixtureWithPolicy(t, ctx, repo, store, []byte(policy))
			scope := f.input.Target.IsolationDomainID
			const token = "authorized-publication-api-token-thirty-two-bytes"
			authenticator, err := authn.NewDevelopmentAuthenticator(authn.DevelopmentConfig{BearerToken: []byte(token), PrincipalID: "operator", IsolationDomainID: scope})
			if err != nil {
				t.Fatal(err)
			}
			authorizer, err := authz.NewDevelopmentCedarAuthorizer("operator", scope)
			if err != nil {
				t.Fatal(err)
			}
			audited, err := authz.NewAuditedAuthorizer(authorizer, repo)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := api.NewGovernedDurableHandler(ctx, repo, authenticator, audited, f.input.Target); err == nil {
				t.Fatal("dispatch-only API started with draft")
			}
			handler, err := api.NewPublishingDurableHandler(ctx, repo, authenticator, audited, f.input)
			if err != nil {
				t.Fatal(err)
			}
			path := "/v1/isolation-domains/" + scope + "/service-revisions/" + f.input.Target.RevisionID + "/actions/publish"
			call := func(path, body, bearer, key string, status int) []byte {
				t.Helper()
				request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Idempotency-Key", "publication-test-"+key)
				if bearer != "" {
					request.Header.Set("Authorization", "Bearer "+bearer)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != status {
					t.Fatalf("status %d want %d: %s", response.Code, status, response.Body.String())
				}
				return bytes.Clone(response.Body.Bytes())
			}
			call(path, `{"expectedVersion":1}`, "", "unauthenticated", 401)
			call(strings.Replace(path, scope, identity.New("iso"), 1), `{"expectedVersion":1}`, token, "cross-domain", 403)
			call(path, `{"expectedVersion":1,"actorId":"forged"}`, token, "forged", 400)
			call(path, `{"expectedVersion":1,"policyDigest":"forged"}`, token, "forged-pin", 400)
			call(path, `{"expectedVersion":2}`, token, "wrong-version", 409)
			if strings.HasPrefix(mode, "invalid ") {
				column := "input_schema"
				code := "REVISION_INPUT_SCHEMA_INVALID"
				if mode == "invalid output schema" {
					column, code = "output_schema", "REVISION_OUTPUT_SCHEMA_INVALID"
				}
				if _, err := pool.Exec(ctx, `UPDATE service_revisions SET `+column+`='{"$ref":"https://private.invalid/schema"}' WHERE isolation_domain_id=$1 AND id=$2`, scope, f.input.Target.RevisionID); err != nil {
					t.Fatal(err)
				}
				body := call(path, `{"expectedVersion":1}`, token, "publish", 409)
				if !bytes.Contains(body, []byte(code)) || bytes.Contains(body, []byte("private.invalid")) {
					t.Fatal("unsafe schema error", string(body))
				}
				return
			}
			if mode == "entry denied" {
				body := call(path, `{"expectedVersion":1}`, token, "publish", 403)
				if !bytes.Contains(body, []byte("PUBLICATION_FORBIDDEN")) {
					t.Fatal(string(body))
				}
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM service_publication_operations WHERE isolation_domain_id=$1`, scope).Scan(&count); err != nil || count != 0 {
					t.Fatal("denied entry queued", count, err)
				}
				return
			}
			original := call(path, `{"expectedVersion":1}`, token, "publish", 202)
			replay := call(path, `{"expectedVersion":1}`, token, "publish", 202)
			if !bytes.Equal(original, replay) {
				t.Fatal("exact acceptance replay changed")
			}
			var operation domain.Operation
			if err := json.Unmarshal(original, &operation); err != nil || operation.StateMachineVersion != 4 || operation.ObservedState != "queued" {
				t.Fatal(string(original), err)
			}
			operationID := operation.Metadata.ID
			if err := repo.RequireDevelopmentPublication(ctx, operationID, f.input); err == nil {
				t.Fatal("old consumer accepted authorized request")
			}
			if _, err := repo.ClaimDevelopmentPublication(ctx, operationID, f.input, "old-worker", time.Minute); err == nil {
				t.Fatal("old consumer claimed authorized request")
			}
			if err := persistence.MigrateDownTo(ctx, db, 58); err == nil {
				t.Fatal("downgrade removed authorized operation")
			}
			claim, err := repo.ClaimAuthorizedDevelopmentPublication(ctx, operationID, f.input, "authorized-worker", time.Minute)
			if err != nil || claim == nil {
				t.Fatal("queued claim", err)
			}
			if claim.ActorID != "operator" {
				t.Fatal("caller substituted actor")
			}
			if err := repo.Advance(ctx, *claim, "validating", nil); err != nil {
				t.Fatal(err)
			}
			claim, err = repo.ClaimAuthorizedDevelopmentPublication(ctx, operationID, f.input, "authorized-worker", time.Minute)
			if err != nil || claim == nil {
				t.Fatal("validating claim", err)
			}
			f.input.ActorID, f.input.CorrelationID = claim.ActorID, claim.CorrelationID
			verify := func(context.Context) (persistence.DevelopmentPublicationEvidence, error) { return f.evidence, nil }
			if err := repo.Advance(ctx, *claim, "applying", nil); err == nil {
				t.Fatal("generic effects bypassed authorization")
			}
			if err := repo.CompleteDevelopmentPublication(ctx, *claim, f.input, verify); err == nil {
				t.Fatal("operator completion bypassed authorization")
			}
			if err := repo.CompleteAuthorizedDevelopmentPublication(ctx, *claim, f.input, verify, nil); err == nil {
				t.Fatal("missing authorizer accepted")
			}
			stale := *claim
			stale.FencingToken--
			if err := repo.CompleteAuthorizedDevelopmentPublication(ctx, stale, f.input, verify, publisher.AuthorizePublication); err == nil {
				t.Fatal("stale claim published")
			}
			if mode == "withdrawn" {
				if err := repo.WithdrawInvocationAuthorizationPolicy(ctx, persistence.InvocationAuthorizationPolicyWithdrawal{Contract: persistence.InvocationAuthorizationPolicyWithdrawalContract, IsolationDomainID: scope, ServiceID: f.input.Target.ServiceID, RevisionID: f.input.Target.RevisionID, PolicyDigest: f.policy.PolicyDigest, WithdrawnBy: "operator", CorrelationID: identity.New("cor"), ReasonDigest: f.policy.ReasonDigest}); err != nil {
					t.Fatal(err)
				}
				call(path, `{"expectedVersion":1}`, token, "publish", 503)
			}
			if mode == "audit failure" {
				connection, err := pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer connection.Release()
				if _, err := connection.Exec(ctx, `CREATE FUNCTION pg_temp.reject_publication_decision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$;
     CREATE TRIGGER reject_publication_decision BEFORE INSERT ON publication_authorization_decisions FOR EACH ROW EXECUTE FUNCTION pg_temp.reject_publication_decision()`); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := connection.Exec(context.Background(), `DROP TRIGGER reject_publication_decision ON publication_authorization_decisions`); err != nil {
						t.Error(err)
					}
				}()
				call(path, `{"expectedVersion":1}`, token, "publish", 503)
			}
			err = repo.CompleteAuthorizedDevelopmentPublication(ctx, *claim, f.input, verify, publisher.AuthorizePublication)
			revision, readErr := repo.GetServiceRevision(ctx, scope, f.input.Target.RevisionID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var events, evidence int
			if queryErr := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND event_type='service-publication.published'),(SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='development-publication.accept')`, scope).Scan(&events, &evidence); queryErr != nil {
				t.Fatal(queryErr)
			}
			if mode != "success" {
				if err == nil || revision.State != "draft" || events != 0 || evidence != 0 {
					t.Fatal("failed authority published", err, revision.State, events, evidence)
				}
				return
			}
			if err != nil || revision.State != "published" || revision.Metadata.Version != 2 || events != 1 || evidence != 1 {
				t.Fatal("completion", err, revision.State, events, evidence)
			}
			var decisions int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM publication_authorization_decisions WHERE isolation_domain_id=$1 AND phase='effect' AND outcome='allowed' AND actor_id='operator' AND fencing_token=$2`, scope, claim.FencingToken).Scan(&decisions); err != nil || decisions != 1 {
				t.Fatal("effect audit", decisions, err)
			}
			if _, err := api.NewPublishingDurableHandler(ctx, repo, authenticator, audited, f.input); err != nil {
				t.Fatal("published restart", err)
			}

			if _, err := repo.AssignAlias(ctx, persistence.Idempotency{IsolationDomainID: scope, Method: "POST", Path: "/alias", Key: "publication-alias", RequestDigest: [32]byte{1}}, persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: f.input.Target.ServiceID, Name: "published", RevisionID: f.input.Target.RevisionID, ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
				t.Fatal(err)
			}
			invocationBody := call("/v1/isolation-domains/"+scope+"/agent-services/"+f.input.Target.ServiceID+"/invocations", `{"alias":"published","input":{}}`, token, "invoke-published", 202)
			var invocation domain.Invocation
			if err := json.Unmarshal(invocationBody, &invocation); err != nil || invocation.RevisionID != f.input.Target.RevisionID {
				t.Fatal("published revision not callable", string(invocationBody), err)
			}
			changed := f.input
			changed.VerificationDigest = "sha256:" + strings.Repeat("0", 64)
			if _, err := api.NewPublishingDurableHandler(ctx, repo, authenticator, audited, changed); err == nil {
				t.Fatal("restart accepted different pins")
			}
			if !bytes.Equal(original, call(path, `{"expectedVersion":1}`, token, "publish", 202)) {
				t.Fatal("terminal acceptance replay changed")
			}
		})
	}
}
