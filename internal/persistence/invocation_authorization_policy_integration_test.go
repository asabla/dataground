package persistence_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

func TestInvocationAuthorizationPolicyInstallationIsExactAuditedAndAppendOnly(t *testing.T) {
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
		) VALUES ($1, $2, 'policy-source-test', $3, $3, $4)
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

	policy, err := reconcile.NewInvocationAuthorizationPolicy(
		reconcile.InvocationAuthorizationPolicyScope{
			IsolationDomainID: domainID,
			ServiceID:         serviceID,
			RevisionID:        revisionID,
		},
		"policy.integration.v1",
		reconcile.CanonicalInvocationCedarSchema(),
		[]byte("permit(principal, action, resource);"),
	)
	if err != nil {
		t.Fatalf("construct policy: %v", err)
	}
	reasonDigest := sha256.Sum256([]byte("approve development invocation policy"))
	record := persistence.InvocationAuthorizationPolicyRecord{
		Contract:                  policy.Contract,
		IsolationDomainID:         policy.IsolationDomainID,
		ServiceID:                 policy.ServiceID,
		RevisionID:                policy.RevisionID,
		PolicySetID:               policy.PolicySetID,
		PolicyDigest:              append([]byte(nil), policy.Digest[:]...),
		Schema:                    append([]byte(nil), policy.Schema...),
		Policies:                  append([]byte(nil), policy.Policies...),
		InstalledBy:               actorID,
		InstallationCorrelationID: correlationID,
		ReasonDigest:              append([]byte(nil), reasonDigest[:]...),
	}
	repository := persistence.NewRepository(pool)
	if err := repository.InstallInvocationAuthorizationPolicy(ctx, record); err != nil {
		t.Fatalf("install policy: %v", err)
	}
	if err := repository.InstallInvocationAuthorizationPolicy(ctx, record); err != nil {
		t.Fatalf("replay identical policy installation: %v", err)
	}

	stored, err := repository.GetInvocationAuthorizationPolicy(ctx, domainID, serviceID, revisionID)
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if !stored.Valid() ||
		stored.PolicySetID != record.PolicySetID ||
		!bytes.Equal(stored.PolicyDigest, record.PolicyDigest) ||
		!bytes.Equal(stored.Schema, record.Schema) ||
		!bytes.Equal(stored.Policies, record.Policies) ||
		stored.InstalledBy != actorID ||
		stored.InstallationCorrelationID != correlationID ||
		!bytes.Equal(stored.ReasonDigest, reasonDigest[:]) {
		t.Fatalf("stored policy = %#v", stored)
	}

	source, err := reconcile.NewDurableInvocationAuthorizationPolicySource(repository)
	if err != nil {
		t.Fatalf("construct durable source: %v", err)
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
		t.Fatalf("resolve durable policy: %v", err)
	}
	if resolved.Digest != policy.Digest || string(resolved.Policies) != string(policy.Policies) {
		t.Fatalf("resolved policy = %#v", resolved)
	}

	var auditCount int
	var auditMetadata string
	if err := pool.QueryRow(ctx, `
		SELECT count(*), max(safe_metadata::text)
		FROM audit_records
		WHERE isolation_domain_id = $1
		  AND action = 'invocation-authorization-policy.install'
		  AND resource_id = $2
		  AND actor_id = $3
		  AND correlation_id = $4
	`, domainID, revisionID, actorID, correlationID).Scan(&auditCount, &auditMetadata); err != nil {
		t.Fatalf("read installation audit: %v", err)
	}
	if auditCount != 1 ||
		!strings.Contains(auditMetadata, policy.PolicySetID) ||
		strings.Contains(auditMetadata, string(policy.Policies)) ||
		strings.Contains(auditMetadata, "approve development invocation policy") {
		t.Fatalf("installation audit = count %d metadata %q", auditCount, auditMetadata)
	}

	conflictingPolicy, err := reconcile.NewInvocationAuthorizationPolicy(
		reconcile.InvocationAuthorizationPolicyScope{
			IsolationDomainID: domainID,
			ServiceID:         serviceID,
			RevisionID:        revisionID,
		},
		"policy.integration.v2",
		reconcile.CanonicalInvocationCedarSchema(),
		[]byte("forbid(principal, action, resource);"),
	)
	if err != nil {
		t.Fatalf("construct conflicting policy: %v", err)
	}
	conflict := record
	conflict.PolicySetID = conflictingPolicy.PolicySetID
	conflict.PolicyDigest = append([]byte(nil), conflictingPolicy.Digest[:]...)
	conflict.Policies = append([]byte(nil), conflictingPolicy.Policies...)
	if err := repository.InstallInvocationAuthorizationPolicy(
		ctx,
		conflict,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationPolicyRecordConflict) {
		t.Fatalf("conflicting installation error = %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE invocation_authorization_policies
		SET policy_set_id = 'changed'
		WHERE isolation_domain_id = $1 AND service_id = $2 AND revision_id = $3
	`, domainID, serviceID, revisionID); err == nil {
		t.Fatal("policy update was accepted")
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM invocation_authorization_policies
		WHERE isolation_domain_id = $1 AND service_id = $2 AND revision_id = $3
	`, domainID, serviceID, revisionID); err == nil {
		t.Fatal("policy deletion was accepted")
	}
}

func TestInvocationAuthorizationPolicyInstallationRejectsScopeDrift(t *testing.T) {
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
	otherServiceID := identity.New("svc")
	revisionID := identity.New("rev")
	actorID := identity.New("usr")
	now := time.Now().UTC()
	for _, candidate := range []string{serviceID, otherServiceID} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO agent_services (
				isolation_domain_id, id, name, created_at, updated_at, created_by
			) VALUES ($1, $2, $3, $4, $4, $5)
		`, domainID, candidate, candidate, now, actorID); err != nil {
			t.Fatalf("insert service: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_revisions (
			isolation_domain_id, id, service_id, revision_number, state,
			runtime_profile, created_at, updated_at, created_by
		) VALUES ($1, $2, $3, 1, 'published', 'codex.app-server/v1', $4, $4, $5)
	`, domainID, revisionID, serviceID, now, actorID); err != nil {
		t.Fatalf("insert revision: %v", err)
	}

	policy, err := reconcile.NewInvocationAuthorizationPolicy(
		reconcile.InvocationAuthorizationPolicyScope{
			IsolationDomainID: domainID,
			ServiceID:         otherServiceID,
			RevisionID:        revisionID,
		},
		"policy.scope-drift",
		reconcile.CanonicalInvocationCedarSchema(),
		[]byte("permit(principal, action, resource);"),
	)
	if err != nil {
		t.Fatalf("construct policy: %v", err)
	}
	reasonDigest := sha256.Sum256([]byte("scope drift"))
	err = persistence.NewRepository(pool).InstallInvocationAuthorizationPolicy(
		ctx,
		persistence.InvocationAuthorizationPolicyRecord{
			Contract:                  policy.Contract,
			IsolationDomainID:         domainID,
			ServiceID:                 otherServiceID,
			RevisionID:                revisionID,
			PolicySetID:               policy.PolicySetID,
			PolicyDigest:              append([]byte(nil), policy.Digest[:]...),
			Schema:                    append([]byte(nil), policy.Schema...),
			Policies:                  append([]byte(nil), policy.Policies...),
			InstalledBy:               actorID,
			InstallationCorrelationID: identity.New("cor"),
			ReasonDigest:              append([]byte(nil), reasonDigest[:]...),
		},
	)
	if err == nil {
		t.Fatal("scope-drifted policy installation was accepted")
	}
}
