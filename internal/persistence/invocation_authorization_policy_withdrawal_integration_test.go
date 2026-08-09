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

func TestInvocationAuthorizationPolicyWithdrawalIsExactAndAppendOnly(t *testing.T) {
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
	installerID := identity.New("usr")
	revokerID := identity.New("usr")
	installationCorrelationID := identity.New("cor")
	withdrawalCorrelationID := identity.New("cor")
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_services (
			isolation_domain_id, id, name, created_at, updated_at, created_by
		) VALUES ($1, $2, 'policy-withdrawal-test', $3, $3, $4)
	`, domainID, serviceID, now, installerID); err != nil {
		t.Fatalf("insert service: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_revisions (
			isolation_domain_id, id, service_id, revision_number, state,
			runtime_profile, created_at, updated_at, created_by
		) VALUES ($1, $2, $3, 1, 'published', 'codex.app-server/v1', $4, $4, $5)
	`, domainID, revisionID, serviceID, now, installerID); err != nil {
		t.Fatalf("insert revision: %v", err)
	}
	policy, err := reconcile.NewInvocationAuthorizationPolicyWithEntities(
		reconcile.InvocationAuthorizationPolicyScope{
			IsolationDomainID: domainID,
			ServiceID:         serviceID,
			RevisionID:        revisionID,
		},
		"policy.withdrawal.integration.v1",
		reconcile.CanonicalInvocationCedarEntitySchema(),
		[]byte(`permit(principal, action, resource);`),
		persistenceEntityFixture(t),
	)
	if err != nil {
		t.Fatalf("construct policy: %v", err)
	}
	installationReasonDigest := sha256.Sum256([]byte("install reviewed policy"))
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
		InstalledBy:               installerID,
		InstallationCorrelationID: installationCorrelationID,
		ReasonDigest:              installationReasonDigest[:],
	}
	repository := persistence.NewRepository(pool)
	if err := repository.InstallInvocationAuthorizationPolicy(ctx, record); err != nil {
		t.Fatalf("install policy: %v", err)
	}
	if _, err := repository.GetActiveInvocationAuthorizationPolicy(
		ctx,
		domainID,
		serviceID,
		revisionID,
	); err != nil {
		t.Fatalf("read active policy: %v", err)
	}

	reason := "emergency withdrawal after unsafe capability review"
	reasonDigest := sha256.Sum256([]byte(reason))
	withdrawal := persistence.InvocationAuthorizationPolicyWithdrawal{
		Contract:          persistence.InvocationAuthorizationPolicyWithdrawalContract,
		IsolationDomainID: domainID,
		ServiceID:         serviceID,
		RevisionID:        revisionID,
		PolicyDigest:      append([]byte(nil), policy.Digest[:]...),
		WithdrawnBy:       revokerID,
		ReasonDigest:      reasonDigest[:],
		CorrelationID:     withdrawalCorrelationID,
	}
	wrongDigest := withdrawal
	wrongDigest.PolicyDigest = bytes.Repeat([]byte{0xff}, sha256.Size)
	if err := repository.WithdrawInvocationAuthorizationPolicy(
		ctx,
		wrongDigest,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationPolicyWithdrawalDigestMismatch) {
		t.Fatalf("wrong digest withdrawal error = %v", err)
	}
	if err := repository.WithdrawInvocationAuthorizationPolicy(ctx, withdrawal); err != nil {
		t.Fatalf("withdraw policy: %v", err)
	}
	if err := repository.WithdrawInvocationAuthorizationPolicy(ctx, withdrawal); err != nil {
		t.Fatalf("replay withdrawal: %v", err)
	}
	changed := withdrawal
	changed.WithdrawnBy = installerID
	if err := repository.WithdrawInvocationAuthorizationPolicy(
		ctx,
		changed,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationPolicyWithdrawalConflict) {
		t.Fatalf("changed withdrawal replay error = %v", err)
	}

	historical, err := repository.GetInvocationAuthorizationPolicy(
		ctx,
		domainID,
		serviceID,
		revisionID,
	)
	if err != nil || !bytes.Equal(historical.PolicyDigest, policy.Digest[:]) {
		t.Fatalf("historical policy = %#v, %v", historical, err)
	}
	if _, err := repository.GetActiveInvocationAuthorizationPolicy(
		ctx,
		domainID,
		serviceID,
		revisionID,
	); !errors.Is(err, persistence.ErrInvocationAuthorizationPolicyRecordMissing) {
		t.Fatalf("active policy after withdrawal error = %v", err)
	}
	source, err := reconcile.NewDurableInvocationAuthorizationPolicySource(repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ResolveInvocationAuthorizationPolicy(
		ctx,
		reconcile.InvocationAuthorizationPolicyScope{
			IsolationDomainID: domainID,
			ServiceID:         serviceID,
			RevisionID:        revisionID,
		},
	); !errors.Is(err, reconcile.ErrInvocationAuthorizationPolicyUnavailable) {
		t.Fatalf("withdrawn source resolution error = %v", err)
	}

	var auditMetadata string
	if err := pool.QueryRow(ctx, `
		SELECT safe_metadata::text
		FROM audit_records
		WHERE isolation_domain_id = $1
		  AND action = 'invocation-authorization-policy.withdraw'
		  AND resource_id = $2
	`, domainID, revisionID).Scan(&auditMetadata); err != nil {
		t.Fatalf("read withdrawal audit: %v", err)
	}
	if strings.Contains(auditMetadata, reason) ||
		!strings.Contains(auditMetadata, "policyDigest") ||
		!strings.Contains(auditMetadata, "reasonDigest") {
		t.Fatalf("withdrawal audit metadata = %s", auditMetadata)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE invocation_authorization_policy_withdrawals
		SET withdrawn_by = $1
		WHERE isolation_domain_id = $2 AND service_id = $3 AND revision_id = $4
	`, installerID, domainID, serviceID, revisionID); err == nil {
		t.Fatal("withdrawal update was accepted")
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM invocation_authorization_policy_withdrawals
		WHERE isolation_domain_id = $1 AND service_id = $2 AND revision_id = $3
	`, domainID, serviceID, revisionID); err == nil {
		t.Fatal("withdrawal deletion was accepted")
	}

	downgradeDatabase, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer downgradeDatabase.Close()
	if err := persistence.MigrateDownTo(ctx, downgradeDatabase, 37); err == nil {
		t.Fatal("withdrawal evidence was discarded by schema downgrade")
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
		t.Fatalf("remove withdrawal migration fixture: %v", err)
	}
	if err := persistence.MigrateDownTo(ctx, downgradeDatabase, 37); err != nil {
		t.Fatalf("remove empty withdrawal schema: %v", err)
	}
	if err := persistence.MigrateUp(ctx, downgradeDatabase); err != nil {
		t.Fatalf("restore current schema: %v", err)
	}
}
