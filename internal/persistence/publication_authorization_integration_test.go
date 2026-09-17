package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

func TestPublicationAuthorizationPersistsExactPhasesAndRetainsEvidence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	defer func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE publication_authorization_decisions,governed_publication_requests;
            DELETE FROM service_publication_operations WHERE state_machine_version=3;
            TRUNCATE invocation_authorization_entity_activations,invocation_authorization_entity_generations,invocation_authorization_policy_withdrawals,invocation_authorization_policies,audit_records`); err != nil {
			t.Error(err)
		}
	}()
	scope, service, revision := identity.New("iso"), identity.New("svc"), identity.New("rev")
	idem := func(key string) persistence.Idempotency {
		return persistence.Idempotency{IsolationDomainID: scope, Method: "POST", Path: "/fixture", Key: key, RequestDigest: sha256.Sum256([]byte(key))}
	}
	if _, err := repo.CreateService(ctx, idem("create-service"), persistence.CreateServiceInput{ID: service, Name: "publication authorization", ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRevision(ctx, idem("create-revision"), persistence.CreateRevisionInput{ID: revision, ServiceID: service, RuntimeProfile: persistence.GovernedInvocationRuntimeProfile, RequiredCapabilities: []string{persistence.GovernedInvocationRuntimeProfile}, ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	policy, err := reconcile.NewInvocationAuthorizationPolicyWithPublicationEntities(reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: scope, ServiceID: service, RevisionID: revision}, "publication-policy", reconcile.CanonicalPublicationCedarSchema(), []byte(`permit(principal in DataGround::Role::"invoker",action==DataGround::Action::"publish",resource);`), persistenceEntityFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	reason := sha256.Sum256([]byte("reviewed publication scope"))
	record := persistence.InvocationAuthorizationPolicyRecord{Contract: policy.Contract, IsolationDomainID: scope, ServiceID: service, RevisionID: revision, PolicySetID: policy.PolicySetID, PolicyDigest: policy.Digest[:], Schema: policy.Schema, Policies: policy.Policies, Entities: policy.Entities, InstalledBy: "operator", InstallationCorrelationID: identity.New("cor"), ReasonDigest: reason[:]}
	if err := repo.InstallInvocationAuthorizationPolicy(ctx, record); err != nil {
		t.Fatal(err)
	}
	source, err := reconcile.NewDurableInvocationAuthorizationPolicySource(repo)
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := reconcile.NewPublicationAuthorizer(source, repo)
	if err != nil {
		t.Fatal(err)
	}
	input := persistence.DevelopmentPublicationInput{Contract: persistence.DevelopmentPublicationContract, Target: persistence.InvocationDispatchTarget{IsolationDomainID: scope, ServiceID: service, RevisionID: revision, RuntimeProfile: persistence.GovernedInvocationRuntimeProfile}, ExpectedVersion: 1, PlanDigest: "sha256:" + strings.Repeat("a", 64), PolicyDigest: "sha256:" + hex.EncodeToString(policy.Digest[:]), VerificationDigest: "sha256:" + strings.Repeat("b", 64), ActorID: "actor_1", CorrelationID: identity.New("cor")}
	operationID := identity.Derived("op", scope+":"+revision+":governed-publication:v1")
	request := authz.PublicationRequest{ActorID: input.ActorID, IsolationDomainID: scope, ServiceID: service, RevisionID: revision, OperationID: operationID, CorrelationID: input.CorrelationID, Phase: "entry", ExpectedVersion: 1, PlanDigest: input.PlanDigest, VerificationDigest: input.VerificationDigest, PolicyDigest: input.PolicyDigest}
	if err := authorizer.AuthorizePublication(ctx, request); err != nil {
		t.Fatal("entry", err)
	}
	result, err := repo.QueueDevelopmentPublication(ctx, idem("queue-publication"), input, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var operation domain.Operation
	if json.Unmarshal(result.Body, &operation) != nil || operation.Metadata.ID != operationID {
		t.Fatal("entry decision lost operation binding")
	}
	claim, err := repo.ClaimDevelopmentPublication(ctx, operationID, input, "publication-worker", time.Minute)
	if err != nil || claim == nil {
		t.Fatal("claim", err)
	}
	if err := repo.Advance(ctx, *claim, "validating", nil); err != nil {
		t.Fatal(err)
	}
	claim, err = repo.ClaimDevelopmentPublication(ctx, operationID, input, "publication-worker", time.Minute)
	if err != nil || claim == nil {
		t.Fatal(err)
	}
	request.Phase, request.FencingToken = "effect", claim.FencingToken
	// Completion owns this row lock. Its separate audit connection must record
	// the decision without waiting for a foreign-key lock on that same row.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `SELECT id FROM service_publication_operations WHERE isolation_domain_id=$1 AND id=$2 FOR UPDATE`, scope, operationID); err != nil {
		t.Fatal(err)
	}
	auditCtx, stopAudit := context.WithTimeout(ctx, 5*time.Second)
	err = authorizer.AuthorizePublication(auditCtx, request)
	stopAudit()
	if err != nil {
		t.Fatal("effect audit blocked or failed", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"token", "actor", "correlation", "plan", "version", "operation", "domain"} {
		changed := request
		switch change {
		case "token":
			changed.FencingToken++
		case "actor":
			changed.ActorID = "actor_2"
		case "correlation":
			changed.CorrelationID = identity.New("cor")
		case "plan":
			changed.PlanDigest = "sha256:" + strings.Repeat("c", 64)
		case "version":
			changed.ExpectedVersion++
		case "operation":
			changed.OperationID = identity.New("op")
		case "domain":
			changed.IsolationDomainID = identity.New("iso")
		}
		if err := authorizer.AuthorizePublication(ctx, changed); err == nil {
			t.Fatal("substituted effect audited as allowed", change)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM publication_authorization_decisions WHERE isolation_domain_id=$1 AND operation_id=$2 AND outcome='allowed' AND policy_contract=$3 AND policy_digest=$4`, scope, operationID, policy.Contract, input.PolicyDigest).Scan(&count); err != nil || count != 2 {
		t.Fatal("exact decision provenance lost", count, err)
	}
	for _, sql := range []string{`UPDATE publication_authorization_decisions SET outcome='denied'`, `DELETE FROM publication_authorization_decisions`} {
		if _, err := pool.Exec(ctx, sql); err == nil {
			t.Fatal("decision evidence mutated")
		}
	}
	if err := persistence.MigrateDownTo(ctx, db, 57); err == nil {
		t.Fatal("downgrade discarded authorization evidence")
	}
	// An audit write failure cannot release a successfully evaluated permit.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_publication_decision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private audit failure'; END; $$;
        CREATE TRIGGER reject_publication_decision BEFORE INSERT ON publication_authorization_decisions FOR EACH ROW EXECUTE FUNCTION reject_publication_decision()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS reject_publication_decision ON publication_authorization_decisions; DROP FUNCTION IF EXISTS reject_publication_decision()`)
	if err := authorizer.AuthorizePublication(ctx, request); err != reconcile.ErrPublicationAuthorizationUnavailable {
		t.Fatal("audit failure released permit", err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_publication_decision ON publication_authorization_decisions; DROP FUNCTION reject_publication_decision()`); err != nil {
		t.Fatal(err)
	}
	// Refresh changes the effective digest. An old reviewed pin fails before a
	// new evaluation; a newly reviewed pin sees removal of the original actor.
	refreshedEntities := canonicalRefreshEntities(t, "actor_2")
	entityDigest := sha256.Sum256(refreshedEntities)
	generation := persistence.InvocationAuthorizationEntityGeneration{Contract: persistence.InvocationAuthorizationEntityGenerationContract, IsolationDomainID: scope, ServiceID: service, RevisionID: revision, Generation: 1, EntityDigest: entityDigest[:], Entities: refreshedEntities, PublishedBy: "operator", CorrelationID: identity.New("cor"), ReasonDigest: reason[:]}
	if err := repo.PublishInvocationAuthorizationEntityGeneration(ctx, generation); err != nil {
		t.Fatal(err)
	}
	activation := persistence.InvocationAuthorizationEntityActivation{Contract: persistence.InvocationAuthorizationEntityActivationContract, IsolationDomainID: scope, ServiceID: service, RevisionID: revision, Generation: 1, InstalledPolicyDigest: policy.Digest[:], ActivatedBy: "operator", CorrelationID: identity.New("cor"), ReasonDigest: reason[:]}
	if err := repo.ActivateInvocationAuthorizationEntityGeneration(ctx, activation); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.AuthorizePublication(ctx, request); err != reconcile.ErrPublicationAuthorizationUnavailable {
		t.Fatal("stale policy pin survived refresh", err)
	}
	active, err := source.ResolveInvocationAuthorizationPolicy(ctx, reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: scope, ServiceID: service, RevisionID: revision})
	if err != nil {
		t.Fatal(err)
	}
	request.Phase, request.FencingToken = "entry", 0
	request.PolicyDigest = "sha256:" + hex.EncodeToString(active.Digest[:])
	if err := authorizer.AuthorizePublication(ctx, request); !errors.Is(err, reconcile.ErrPublicationAuthorizationDenied) {
		t.Fatal("removed publisher retained authority", err)
	}
	if err := repo.WithdrawInvocationAuthorizationPolicy(ctx, persistence.InvocationAuthorizationPolicyWithdrawal{Contract: persistence.InvocationAuthorizationPolicyWithdrawalContract, IsolationDomainID: scope, ServiceID: service, RevisionID: revision, PolicyDigest: policy.Digest[:], WithdrawnBy: "operator", CorrelationID: identity.New("cor"), ReasonDigest: reason[:]}); err != nil {
		t.Fatal(err)
	}
	if err := authorizer.AuthorizePublication(ctx, request); err != reconcile.ErrPublicationAuthorizationUnavailable {
		t.Fatal("withdrawn policy authorized publication", err)
	}
}
