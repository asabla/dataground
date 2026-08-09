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

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	cedar "github.com/cedar-policy/cedar-go"
)

func TestInvocationAuthorizationEntitySnapshotIsExactAndAppendOnly(t *testing.T) {
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
	correlationID := identity.New("cor")
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_services (
			isolation_domain_id, id, name, created_at, updated_at, created_by
		) VALUES ($1, $2, 'entity-policy-test', $3, $3, $4)
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

	entities := persistenceEntityFixture(t)
	policy, err := reconcile.NewInvocationAuthorizationPolicyWithEntities(
		reconcile.InvocationAuthorizationPolicyScope{
			IsolationDomainID: domainID,
			ServiceID:         serviceID,
			RevisionID:        revisionID,
		},
		"policy.entities.integration.v1",
		reconcile.CanonicalInvocationCedarEntitySchema(),
		[]byte(`permit(principal in DataGround::Role::"invoker", action, resource);`),
		entities,
	)
	if err != nil {
		t.Fatalf("construct policy: %v", err)
	}
	reasonDigest := sha256.Sum256([]byte("install reviewed entity snapshot"))
	record := persistence.InvocationAuthorizationPolicyRecord{
		Contract:                  policy.Contract,
		IsolationDomainID:         domainID,
		ServiceID:                 serviceID,
		RevisionID:                revisionID,
		PolicySetID:               policy.PolicySetID,
		PolicyDigest:              append([]byte(nil), policy.Digest[:]...),
		Schema:                    append([]byte(nil), policy.Schema...),
		Policies:                  append([]byte(nil), policy.Policies...),
		Entities:                  append([]byte(nil), policy.Entities...),
		InstalledBy:               actorID,
		InstallationCorrelationID: correlationID,
		ReasonDigest:              append([]byte(nil), reasonDigest[:]...),
	}
	repository := persistence.NewRepository(pool)
	if err := repository.InstallInvocationAuthorizationPolicy(ctx, record); err != nil {
		t.Fatalf("install entity policy: %v", err)
	}
	if err := repository.InstallInvocationAuthorizationPolicy(ctx, record); err != nil {
		t.Fatalf("replay entity policy: %v", err)
	}

	stored, err := repository.GetInvocationAuthorizationPolicy(ctx, domainID, serviceID, revisionID)
	if err != nil {
		t.Fatalf("read entity policy: %v", err)
	}
	if !stored.Valid() ||
		!bytes.Equal(stored.PolicyDigest, record.PolicyDigest) ||
		!bytes.Equal(stored.Entities, entities) {
		t.Fatalf("stored entity policy = %#v", stored)
	}
	source, err := reconcile.NewDurableInvocationAuthorizationPolicySource(repository)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := source.ResolveInvocationAuthorizationPolicy(
		ctx,
		reconcile.InvocationAuthorizationPolicyScope{
			IsolationDomainID: domainID,
			ServiceID:         serviceID,
			RevisionID:        revisionID,
		},
	)
	if err != nil {
		t.Fatalf("resolve entity policy: %v", err)
	}
	if resolved.Digest != policy.Digest || !bytes.Equal(resolved.Entities, entities) {
		t.Fatalf("resolved entity policy = %#v", resolved)
	}

	invalid := record
	invalid.Entities = append(append([]byte(nil), record.Entities...), '\n')
	if err := repository.InstallInvocationAuthorizationPolicy(
		ctx,
		invalid,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationPolicyRecordInvalid) {
		t.Fatalf("noncanonical entity installation error = %v", err)
	}

	var auditMetadata string
	if err := pool.QueryRow(ctx, `
		SELECT safe_metadata::text
		FROM audit_records
		WHERE isolation_domain_id = $1
		  AND action = 'invocation-authorization-policy.install'
		  AND resource_id = $2
	`, domainID, revisionID).Scan(&auditMetadata); err != nil {
		t.Fatalf("read installation audit: %v", err)
	}
	if strings.Contains(auditMetadata, string(entities)) {
		t.Fatal("installation audit retained entity snapshot content")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE invocation_authorization_policies
		SET cedar_entities = '[]'
		WHERE isolation_domain_id = $1 AND service_id = $2 AND revision_id = $3
	`, domainID, serviceID, revisionID); err == nil {
		t.Fatal("entity snapshot update was accepted")
	}

	downgradeDatabase, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer downgradeDatabase.Close()
	if err := persistence.MigrateDownTo(ctx, downgradeDatabase, 35); err == nil {
		t.Fatal("entity evidence was discarded by schema downgrade")
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
		t.Fatalf("remove entity migration fixture: %v", err)
	}
	if err := persistence.MigrateDownTo(ctx, downgradeDatabase, 35); err != nil {
		t.Fatalf("remove empty entity schema: %v", err)
	}
	if err := persistence.MigrateUp(ctx, downgradeDatabase); err != nil {
		t.Fatalf("restore current schema: %v", err)
	}
}

func persistenceEntityFixture(t *testing.T) []byte {
	t.Helper()
	raw := []byte(`[
		{"uid":{"type":"DataGround::Actor","id":"actor_1"},"attrs":{},"parents":[{"type":"DataGround::Role","id":"invoker"}]},
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
