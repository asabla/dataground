package persistence_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authn"
	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestAPIAuthorizationDecisionsAreAttributedAndAppendOnly(t *testing.T) {
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

	domainID := identity.New("iso")
	allowedActor := identity.New("usr")
	deniedActor := identity.New("usr")
	authorizer, err := authz.NewDevelopmentCedarAuthorizer(allowedActor, domainID)
	if err != nil {
		t.Fatalf("create development authorizer: %v", err)
	}
	repository := persistence.NewRepository(pool)
	audited, err := authz.NewAuditedAuthorizer(authorizer, repository)
	if err != nil {
		t.Fatalf("compose audited authorizer: %v", err)
	}

	allowedCorrelation := identity.New("cor")
	if err := audited.Authorize(ctx, authorizationAuditRequest(
		t,
		allowedActor,
		domainID,
		allowedCorrelation,
	)); err != nil {
		t.Fatalf("record allowed decision: %v", err)
	}
	deniedCorrelation := identity.New("cor")
	if err := audited.Authorize(ctx, authorizationAuditRequest(
		t,
		deniedActor,
		domainID,
		deniedCorrelation,
	)); !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("record denied decision: %v", err)
	}
	rows, err := pool.Query(ctx, `
		SELECT
			principal_id,
			principal_kind,
			action,
			resource_type,
			resource_id,
			outcome,
			policy_set_id,
			policy_digest,
			correlation_id
		FROM api_authorization_decisions
		WHERE isolation_domain_id = $1
		ORDER BY sequence
	`, domainID)
	if err != nil {
		t.Fatalf("read authorization decisions: %v", err)
	}
	defer rows.Close()
	type observedDecision struct {
		principalID   string
		principalKind string
		action        string
		resourceType  string
		resourceID    string
		outcome       string
		policySetID   string
		policyDigest  string
		correlationID string
	}
	var decisions []observedDecision
	for rows.Next() {
		var decision observedDecision
		if err := rows.Scan(
			&decision.principalID,
			&decision.principalKind,
			&decision.action,
			&decision.resourceType,
			&decision.resourceID,
			&decision.outcome,
			&decision.policySetID,
			&decision.policyDigest,
			&decision.correlationID,
		); err != nil {
			t.Fatalf("scan authorization decision: %v", err)
		}
		decisions = append(decisions, decision)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate authorization decisions: %v", err)
	}
	rows.Close()
	if len(decisions) != 2 {
		t.Fatalf("authorization decision count = %d, want 2", len(decisions))
	}
	if decisions[0].principalID != allowedActor ||
		decisions[0].principalKind != string(authn.PrincipalHuman) ||
		decisions[0].action != string(authz.ReadInvocation) ||
		decisions[0].resourceType != string(authz.Invocation) ||
		decisions[0].resourceID != "inv_00000000000000000001" ||
		decisions[0].outcome != string(authz.OutcomeAllowed) ||
		decisions[0].policySetID != "dataground-development-api" ||
		decisions[0].policyDigest == "" ||
		decisions[0].correlationID != allowedCorrelation {
		t.Fatalf("allowed decision = %#v", decisions[0])
	}
	if decisions[1].principalID != deniedActor ||
		decisions[1].outcome != string(authz.OutcomeDenied) ||
		decisions[1].policyDigest != decisions[0].policyDigest ||
		decisions[1].correlationID != deniedCorrelation {
		t.Fatalf("denied decision = %#v", decisions[1])
	}
	if _, err := pool.Exec(ctx, `
		UPDATE api_authorization_decisions
		SET outcome = 'denied'
		WHERE isolation_domain_id = $1
	`, domainID); err == nil {
		t.Fatal("authorization decision update was accepted")
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM api_authorization_decisions
		WHERE isolation_domain_id = $1
	`, domainID); err == nil {
		t.Fatal("authorization decision deletion was accepted")
	}

	transaction, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Exec(ctx, `
		INSERT INTO api_authorization_decisions (
			isolation_domain_id, principal_id, principal_kind, action,
			resource_type, resource_id, outcome, policy_set_id,
			policy_digest, correlation_id
		) VALUES (
			$1, $2, 'human', 'resolveInvocationApproval',
			'DataGround::InvocationApproval', $3, 'allowed',
			'dataground-development-api', $4, $5
		)
	`, domainID, allowedActor, "apr_00000000000000000001",
		"sha256:"+strings.Repeat("a", 64), identity.New("cor")); err != nil {
		_ = transaction.Rollback(ctx)
		t.Fatalf("record rollback-only invocation approval decision: %v", err)
	}
	if err := transaction.Rollback(ctx); err != nil {
		t.Fatalf("roll back invocation approval decision fixture: %v", err)
	}
}

func authorizationAuditRequest(
	t *testing.T,
	actorID string,
	domainID string,
	correlationID string,
) authz.Request {
	t.Helper()
	principal, err := authn.NewPrincipal(authn.PrincipalInput{
		ID: actorID, Kind: authn.PrincipalHuman, Issuer: "test", Subject: actorID,
		Audience: authn.APIAudience, IsolationDomains: []string{domainID},
	})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	return authz.Request{
		Principal: principal, Action: authz.ReadInvocation, ResourceType: authz.Invocation,
		ResourceID: "inv_00000000000000000001", IsolationDomainID: domainID,
		CorrelationID: correlationID,
	}
}
