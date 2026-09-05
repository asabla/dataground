package persistence_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/authz"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	cedar "github.com/cedar-policy/cedar-go"
)

func TestInvocationAuthorizationEntityRefreshIsSequentialAndFailClosed(t *testing.T) {
	for _, contract := range []string{"v2", "v3", "v4"} {
		t.Run(contract, func(t *testing.T) { testInvocationAuthorizationEntityRefresh(t, contract) })
	}
}

func testInvocationAuthorizationEntityRefresh(t *testing.T, contract string) {
	t.Helper()
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
	serviceID := identity.New("svc")
	revisionID := identity.New("rev")
	actorID := identity.New("usr")
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_services (
			isolation_domain_id, id, name, created_at, updated_at, created_by
		) VALUES ($1, $2, 'entity-refresh-test', $3, $3, $4)
	`, domainID, serviceID, now, actorID); err != nil {
		t.Fatalf("insert service: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_revisions (
			isolation_domain_id, id, service_id, revision_number, state,
			runtime_profile, created_at, updated_at, created_by
		) VALUES ($1, $2, $3, 1, 'published', 'codex.app-server/v1', $4, $4, $5)
	`, domainID, revisionID, serviceID, now, actorID); err != nil {
		t.Fatalf("insert revision: %v", err)
	}
	initialEntities := persistenceEntityFixture(t)
	constructor := reconcile.NewInvocationAuthorizationPolicyWithEntities
	schema := reconcile.CanonicalInvocationCedarEntitySchema()
	digestFor := authz.InvocationAuthorizationPolicyV2Digest
	switch contract {
	case "v3":
		constructor = reconcile.NewInvocationAuthorizationPolicyWithApprovalEntities
		schema = reconcile.CanonicalInvocationCedarApprovalSchema()
		digestFor = authz.InvocationAuthorizationPolicyV3Digest
	case "v4":
		constructor = reconcile.NewInvocationAuthorizationPolicyWithQuestionEntities
		schema = reconcile.CanonicalInvocationCedarQuestionSchema()
		digestFor = authz.InvocationAuthorizationPolicyV4Digest
	}
	policy, err := constructor(
		reconcile.InvocationAuthorizationPolicyScope{
			IsolationDomainID: domainID, ServiceID: serviceID, RevisionID: revisionID,
		},
		"policy.entity-refresh.integration.v1",
		schema,
		[]byte(`permit(principal in DataGround::Role::"invoker", action, resource);`),
		initialEntities,
	)
	if err != nil {
		t.Fatal(err)
	}
	installReason := sha256.Sum256([]byte("install reviewed policy"))
	repository := persistence.NewRepository(pool)
	if err := repository.InstallInvocationAuthorizationPolicy(
		ctx,
		persistence.InvocationAuthorizationPolicyRecord{
			Contract: policy.Contract, IsolationDomainID: domainID, ServiceID: serviceID,
			RevisionID: revisionID, PolicySetID: policy.PolicySetID,
			PolicyDigest: policy.Digest[:], Schema: policy.Schema, Policies: policy.Policies,
			Entities: policy.Entities, InstalledBy: actorID,
			InstallationCorrelationID: identity.New("cor"), ReasonDigest: installReason[:],
		},
	); err != nil {
		t.Fatalf("install policy: %v", err)
	}

	refreshedEntities := canonicalRefreshEntities(t, "actor_2")
	entityDigest := sha256.Sum256(refreshedEntities)
	publicationReason := sha256.Sum256([]byte("publish complete entity generation"))
	generation := persistence.InvocationAuthorizationEntityGeneration{
		Contract:          persistence.InvocationAuthorizationEntityGenerationContract,
		IsolationDomainID: domainID, ServiceID: serviceID, RevisionID: revisionID,
		Generation: 1, EntityDigest: entityDigest[:], Entities: refreshedEntities,
		PublishedBy: actorID, CorrelationID: identity.New("cor"),
		ReasonDigest: publicationReason[:],
	}
	if err := repository.PublishInvocationAuthorizationEntityGeneration(ctx, generation); err != nil {
		t.Fatalf("publish generation: %v", err)
	}
	if err := repository.PublishInvocationAuthorizationEntityGeneration(ctx, generation); err != nil {
		t.Fatalf("replay generation: %v", err)
	}
	changedGeneration := generation
	changedGeneration.PublishedBy = identity.New("usr")
	if err := repository.PublishInvocationAuthorizationEntityGeneration(
		ctx, changedGeneration,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationEntityRefreshConflict) {
		t.Fatalf("changed generation replay error = %v", err)
	}
	gap := generation
	gap.Generation = 3
	gap.CorrelationID = identity.New("cor")
	if err := repository.PublishInvocationAuthorizationEntityGeneration(
		ctx, gap,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationEntityRefreshConflict) {
		t.Fatalf("generation gap error = %v", err)
	}
	beforeActivation, err := repository.GetActiveInvocationAuthorizationPolicy(
		ctx, domainID, serviceID, revisionID,
	)
	if err != nil || !bytes.Equal(beforeActivation.PolicyDigest, policy.Digest[:]) ||
		!bytes.Equal(beforeActivation.Entities, initialEntities) {
		t.Fatalf("pre-activation policy = %#v, %v", beforeActivation, err)
	}
	stagedEntities := canonicalRefreshEntities(t, "actor_3")
	stagedDigest := sha256.Sum256(stagedEntities)
	staged := generation
	staged.Generation = 2
	staged.EntityDigest = stagedDigest[:]
	staged.Entities = stagedEntities
	staged.CorrelationID = identity.New("cor")
	if err := repository.PublishInvocationAuthorizationEntityGeneration(ctx, staged); err != nil {
		t.Fatalf("stage second generation: %v", err)
	}

	activationReason := sha256.Sum256([]byte("activate reviewed entity generation"))
	activation := persistence.InvocationAuthorizationEntityActivation{
		Contract:          persistence.InvocationAuthorizationEntityActivationContract,
		IsolationDomainID: domainID, ServiceID: serviceID, RevisionID: revisionID,
		Generation: 1, InstalledPolicyDigest: policy.Digest[:], ActivatedBy: actorID,
		CorrelationID: identity.New("cor"), ReasonDigest: activationReason[:],
	}
	wrongPolicy := activation
	wrongPolicy.InstalledPolicyDigest = bytes.Repeat([]byte{0xff}, sha256.Size)
	if err := repository.ActivateInvocationAuthorizationEntityGeneration(
		ctx, wrongPolicy,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationEntityRefreshConflict) {
		t.Fatalf("wrong installed policy digest error = %v", err)
	}
	skippedActivation := activation
	skippedActivation.Generation = 2
	skippedActivation.CorrelationID = identity.New("cor")
	if err := repository.ActivateInvocationAuthorizationEntityGeneration(
		ctx, skippedActivation,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationEntityRefreshConflict) {
		t.Fatalf("activation gap error = %v", err)
	}
	if contract != "v2" {
		wrongDomain := activation
		digest := authz.InvocationAuthorizationPolicyV2Digest(policy.Schema, policy.Policies, refreshedEntities)
		wrongDomain.EffectivePolicyDigest = digest[:]
		if err := repository.ActivateInvocationAuthorizationEntityGeneration(ctx, wrongDomain); !errors.Is(err, persistence.ErrInvocationAuthorizationEntityRefreshConflict) {
			t.Fatalf("cross-contract effective digest accepted: %v", err)
		}
	}
	if err := repository.ActivateInvocationAuthorizationEntityGeneration(ctx, activation); err != nil {
		t.Fatalf("activate generation: %v", err)
	}
	if err := repository.ActivateInvocationAuthorizationEntityGeneration(ctx, activation); err != nil {
		t.Fatalf("replay activation: %v", err)
	}
	changedActivation := activation
	changedActivation.ActivatedBy = identity.New("usr")
	if err := repository.ActivateInvocationAuthorizationEntityGeneration(
		ctx, changedActivation,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationEntityRefreshConflict) {
		t.Fatalf("changed activation replay error = %v", err)
	}
	active, err := repository.GetActiveInvocationAuthorizationPolicy(
		ctx, domainID, serviceID, revisionID,
	)
	effectiveDigest := digestFor(
		policy.Schema, policy.Policies, refreshedEntities,
	)
	if err != nil || !bytes.Equal(active.Entities, refreshedEntities) ||
		!bytes.Equal(active.PolicyDigest, effectiveDigest[:]) {
		t.Fatalf("active refreshed policy = %#v, %v", active, err)
	}
	source, err := reconcile.NewDurableInvocationAuthorizationPolicySource(repository)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := source.ResolveInvocationAuthorizationPolicy(
		ctx,
		reconcile.InvocationAuthorizationPolicyScope{
			IsolationDomainID: domainID, ServiceID: serviceID, RevisionID: revisionID,
		},
	)
	if err != nil || resolved.Digest != effectiveDigest ||
		!bytes.Equal(resolved.Entities, refreshedEntities) {
		t.Fatalf("resolved refreshed policy = %#v, %v", resolved, err)
	}
	assertRefreshedMembership(t, ctx, policy, "actor_1", "actor_2")
	assertRefreshedMembership(t, ctx, resolved, "actor_2", "actor_1")
	if resolved.Contract != policy.Contract || !bytes.Equal(resolved.Schema, policy.Schema) || !bytes.Equal(resolved.Policies, policy.Policies) {
		t.Fatal("entity activation changed installed policy capabilities")
	}
	var activationAudit string
	if err := pool.QueryRow(ctx, `
		SELECT safe_metadata::text
		FROM audit_records
		WHERE isolation_domain_id = $1
		  AND action = 'invocation-authorization-entities.activate'
		  AND resource_id = $2
	`, domainID, revisionID).Scan(&activationAudit); err != nil {
		t.Fatalf("read activation audit: %v", err)
	}
	if strings.Contains(activationAudit, string(refreshedEntities)) ||
		!strings.Contains(activationAudit, "effectivePolicyDigest") ||
		!strings.Contains(activationAudit, "entityDigest") {
		t.Fatalf("activation audit metadata = %s", activationAudit)
	}

	secondActivation := activation
	secondActivation.Generation = 2
	secondActivation.CorrelationID = identity.New("cor")
	wrongInstalled := secondActivation
	wrongInstalled.InstalledPolicyDigest = effectiveDigest[:]
	if err := repository.ActivateInvocationAuthorizationEntityGeneration(ctx, wrongInstalled); !errors.Is(err, persistence.ErrInvocationAuthorizationEntityRefreshConflict) {
		t.Fatalf("effective digest substituted for installed digest: %v", err)
	}
	if err := repository.ActivateInvocationAuthorizationEntityGeneration(ctx, secondActivation); err != nil {
		t.Fatal(err)
	}
	if err := repository.ActivateInvocationAuthorizationEntityGeneration(ctx, activation); err != nil {
		t.Fatalf("historical activation replay: %v", err)
	}
	restartedSource, err := reconcile.NewDurableInvocationAuthorizationPolicySource(persistence.NewRepository(pool))
	if err != nil {
		t.Fatal(err)
	}
	latest, err := restartedSource.ResolveInvocationAuthorizationPolicy(ctx, reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: domainID, ServiceID: serviceID, RevisionID: revisionID})
	if err != nil || latest.Digest != digestFor(policy.Schema, policy.Policies, stagedEntities) {
		t.Fatalf("historical replay or source replacement rolled back active membership: %v", err)
	}
	assertRefreshedMembership(t, ctx, latest, "actor_3", "actor_2")

	withdrawalReason := sha256.Sum256([]byte("withdraw policy while refresh is active"))
	if err := repository.WithdrawInvocationAuthorizationPolicy(
		ctx,
		persistence.InvocationAuthorizationPolicyWithdrawal{
			Contract:          persistence.InvocationAuthorizationPolicyWithdrawalContract,
			IsolationDomainID: domainID, ServiceID: serviceID, RevisionID: revisionID,
			PolicyDigest: policy.Digest[:], WithdrawnBy: actorID,
			ReasonDigest: withdrawalReason[:], CorrelationID: identity.New("cor"),
		},
	); err != nil {
		t.Fatalf("withdraw policy: %v", err)
	}
	generation.Generation = 3
	generation.CorrelationID = identity.New("cor")
	if err := repository.PublishInvocationAuthorizationEntityGeneration(
		ctx, generation,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationEntityRefreshUnavailable) {
		t.Fatalf("post-withdrawal publication error = %v", err)
	}
	stagedActivation := activation
	stagedActivation.Generation = 2
	stagedActivation.CorrelationID = identity.New("cor")
	if err := repository.ActivateInvocationAuthorizationEntityGeneration(
		ctx, stagedActivation,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationEntityRefreshUnavailable) {
		t.Fatalf("post-withdrawal activation error = %v", err)
	}
	if _, err := repository.GetActiveInvocationAuthorizationPolicy(
		ctx, domainID, serviceID, revisionID,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationPolicyRecordMissing) {
		t.Fatalf("post-withdrawal resolution error = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE invocation_authorization_entity_generations SET published_by = $1
		WHERE isolation_domain_id = $2 AND service_id = $3 AND revision_id = $4
	`, actorID, domainID, serviceID, revisionID); err == nil {
		t.Fatal("entity generation mutation was accepted")
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM invocation_authorization_entity_activations
		WHERE isolation_domain_id = $1 AND service_id = $2 AND revision_id = $3
	`, domainID, serviceID, revisionID); err == nil {
		t.Fatal("entity activation deletion was accepted")
	}
	downgradeDatabase, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer downgradeDatabase.Close()
	if err := persistence.MigrateDownTo(ctx, downgradeDatabase, 39); err == nil {
		t.Fatal("entity refresh evidence was discarded by schema downgrade")
	}
	if err := persistence.RequireCurrentSchema(ctx, downgradeDatabase); err != nil {
		t.Fatalf("failed downgrade changed current schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		TRUNCATE invocation_authorization_entity_activations,
		         invocation_authorization_entity_generations,
		         invocation_authorization_policy_withdrawals,
		         invocation_authorization_policies
	`); err != nil {
		t.Fatalf("remove entity refresh migration fixture: %v", err)
	}
	if err := persistence.MigrateDownTo(ctx, downgradeDatabase, 39); err != nil {
		t.Fatalf("remove empty entity refresh schema: %v", err)
	}
	if err := persistence.MigrateUp(ctx, downgradeDatabase); err != nil {
		t.Fatalf("restore current schema: %v", err)
	}
}

func canonicalRefreshEntities(t *testing.T, actorID string) []byte {
	t.Helper()
	raw := []byte(`[
		{"uid":{"type":"DataGround::Actor","id":"` + actorID + `"},"attrs":{},"parents":[{"type":"DataGround::Role","id":"invoker"}]},
		{"uid":{"type":"DataGround::Role","id":"invoker"},"attrs":{},"parents":[]}
	]`)
	var entities cedar.EntityMap
	if err := json.Unmarshal(raw, &entities); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(entities)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func assertRefreshedMembership(t *testing.T, ctx context.Context, policy reconcile.InvocationAuthorizationPolicy, allowed, denied string) {
	t.Helper()
	input := reconcile.InvocationCedarInput{
		Contract:          reconcile.InvocationCedarContract,
		Principal:         reconcile.InvocationCedarEntityUID{Type: "DataGround::Actor", ID: allowed},
		Action:            reconcile.InvocationCedarEntityUID{Type: "DataGround::Action", ID: "admit"},
		Resource:          reconcile.InvocationCedarEntityUID{Type: "DataGround::Invocation", ID: identity.New("inv")},
		IsolationDomainID: policy.IsolationDomainID, ServiceID: policy.ServiceID, RevisionID: policy.RevisionID,
		OperationID: identity.New("op"), CorrelationID: identity.New("cor"),
	}
	if policy.Contract == reconcile.InvocationAuthorizationPolicyApprovalContract {
		input.Action.ID = "approve"
		input.Approval = &reconcile.InvocationApprovalAuthorizationContext{ID: identity.New("apr"), RequestedAction: "process.execute", Decision: "approve", Phase: "effect"}
	}
	if policy.Contract == reconcile.InvocationAuthorizationPolicyQuestionContract {
		input.Contract = reconcile.InvocationCedarQuestionContract
		input.Action.ID = "answer"
		input.Question = &reconcile.InvocationQuestionAuthorizationContext{ID: identity.New("qst"), Version: 2, Phase: "effect", QuestionCount: 1, FreeTextCount: 1}
	}
	evaluator := reconcile.NewCedarInvocationAuthorizationEvaluator()
	if err := evaluator.EvaluateInvocationAuthorization(ctx, policy, input); err != nil {
		t.Fatalf("current role member denied: %v", err)
	}
	input.Principal.ID = denied
	if err := evaluator.EvaluateInvocationAuthorization(ctx, policy, input); !errors.Is(err, reconcile.ErrInvocationAuthorizationDenied) {
		t.Fatalf("absent role member authorized: %v", err)
	}
}
