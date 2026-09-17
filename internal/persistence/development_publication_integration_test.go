package persistence_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

func TestGovernedDevelopmentPublicationIsAtomicScopedAndReplayable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	databaseURL := testDatabaseURL(t)
	db, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateUp(ctx, db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo := persistence.NewRepository(pool)
	store := executionpostgres.New(pool)
	defer func() {
		// Later migration tests must start without retention-protected fixtures.
		// This cleanup runs only against the dedicated test database.
		if _, err := pool.Exec(context.Background(), `TRUNCATE audit_records,
            invocation_authorization_entity_activations,
            invocation_authorization_entity_generations,
            invocation_authorization_policy_withdrawals,
            invocation_authorization_policies,
            provider_credential_authorization_decisions,provider_credential_grant_events`); err != nil {
			t.Error(err)
		}
	}()
	create := func(t *testing.T) developmentPublicationFixture {
		return newDevelopmentPublicationFixture(t, ctx, repo, store)
	}

	t.Run("concurrent replay and retirement", func(t *testing.T) {
		fixture := create(t)
		var calls atomic.Int32
		verify := func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
			calls.Add(1)
			return fixture.evidence, nil
		}
		var group sync.WaitGroup
		results := make(chan persistence.CommandResult, 4)
		failures := make(chan error, 4)
		for range 4 {
			group.Add(1)
			go func() {
				defer group.Done()
				r, err := repo.PublishDevelopmentRevision(ctx, fixture.input, verify)
				results <- r
				failures <- err
			}()
		}
		group.Wait()
		close(results)
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
		var original []byte
		fresh := 0
		for r := range results {
			if original == nil {
				original = r.Body
			}
			if !bytes.Equal(original, r.Body) {
				t.Fatal("replay changed receipt")
			}
			if !r.Replayed {
				fresh++
			}
		}
		if calls.Load() != 1 || fresh != 1 {
			t.Fatal("publication repeated verification or effects")
		}
		var receipt map[string]any
		if json.Unmarshal(original, &receipt) != nil || len(receipt) != 7 || receipt["certificationEligible"] != false {
			t.Fatal("unsafe receipt")
		}
		operation, err := repo.GetOperation(ctx, fixture.input.Target.IsolationDomainID, receipt["operationId"].(string))
		if err != nil || operation.ObservedState != "published" || operation.StateMachineVersion != 2 || operation.ResourceID != fixture.input.Target.RevisionID {
			t.Fatal("publication operation lost its terminal contract", err)
		}
		var version, operations, audits, outbox int
		if err := pool.QueryRow(ctx, `SELECT version,(SELECT count(*) FROM service_publication_operations WHERE isolation_domain_id=$1),(SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='development-publication.accept'),(SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND event_type='service-publication.published') FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2 AND state='published'`, fixture.input.Target.IsolationDomainID, fixture.input.Target.RevisionID).Scan(&version, &operations, &audits, &outbox); err != nil || version != 2 || operations != 1 || audits != 1 || outbox != 1 {
			t.Fatalf("publication not atomic: %d %d %d %d %v", version, operations, audits, outbox, err)
		}
		changed := fixture.input
		changed.VerificationDigest = "sha256:" + strings.Repeat("a", 64)
		if _, err := repo.PublishDevelopmentRevision(ctx, changed, verify); err == nil {
			t.Fatal("changed replay accepted")
		}
		reason := sha256.Sum256([]byte("withdraw after publication"))
		if err := repo.WithdrawInvocationAuthorizationPolicy(ctx, persistence.InvocationAuthorizationPolicyWithdrawal{Contract: persistence.InvocationAuthorizationPolicyWithdrawalContract, IsolationDomainID: fixture.input.Target.IsolationDomainID, ServiceID: fixture.input.Target.ServiceID, RevisionID: fixture.input.Target.RevisionID, PolicyDigest: fixture.policy.PolicyDigest, WithdrawnBy: "operator", CorrelationID: identity.New("cor"), ReasonDigest: reason[:]}); err != nil {
			t.Fatal(err)
		}
		revoke := fixture.grant
		revoke.Generation = 2
		revoke.Operation = "revoke"
		revoke.ActivatedAt = time.Time{}
		revoke.ExpiresAt = time.Time{}
		revoke.CorrelationID = identity.New("cor")
		if err := repo.ChangeProviderCredentialGrant(ctx, revoke); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.RetireRevision(ctx, persistence.Idempotency{IsolationDomainID: fixture.input.Target.IsolationDomainID, Method: "POST", Path: "/retire", Key: "retire-fixture", RequestDigest: sha256.Sum256([]byte("retire"))}, persistence.RetireRevisionInput{RevisionID: fixture.input.Target.RevisionID, ExpectedVersion: 2, ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
			t.Fatal(err)
		}
		replay, err := repo.PublishDevelopmentRevision(ctx, fixture.input, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
			t.Fatal("historical replay reverified dependencies")
			return fixture.evidence, errors.New("expired")
		})
		if err != nil || !replay.Replayed || !bytes.Equal(original, replay.Body) {
			t.Fatal("historical receipt unavailable", err)
		}
		var state string
		if err := pool.QueryRow(ctx, `SELECT state FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2`, fixture.input.Target.IsolationDomainID, fixture.input.Target.RevisionID).Scan(&state); err != nil || state != "retired" {
			t.Fatal("replay republished retired revision", err)
		}
	})

	t.Run("audit failure rolls back publication", func(t *testing.T) {
		fixture := create(t)
		connection, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Release()
		if _, err := connection.Exec(ctx, `CREATE FUNCTION pg_temp.reject_development_publication_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$`); err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(ctx, `CREATE TRIGGER publication_audit_failure BEFORE INSERT ON audit_records FOR EACH ROW WHEN (NEW.action='development-publication.accept') EXECUTE FUNCTION pg_temp.reject_development_publication_audit()`); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := connection.Exec(context.Background(), `DROP TRIGGER publication_audit_failure ON audit_records`); err != nil {
				t.Error(err)
			}
		}()
		if _, err := repo.PublishDevelopmentRevision(ctx, fixture.input, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
			return fixture.evidence, nil
		}); !errors.Is(err, persistence.ErrDevelopmentPublicationUnavailable) {
			t.Fatal("audit failure was not withheld", err)
		}
		var state string
		var operations, receipts int
		if err := pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM service_publication_operations WHERE isolation_domain_id=$1),(SELECT count(*) FROM idempotency_records WHERE isolation_domain_id=$1 AND idempotency_key=$3) FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2`, fixture.input.Target.IsolationDomainID, fixture.input.Target.RevisionID, fixture.input.CorrelationID).Scan(&state, &operations, &receipts); err != nil || state != "draft" || operations != 0 || receipts != 0 {
			t.Fatal("audit failure retained publication or replay state", state, operations, receipts, err)
		}
	})
	t.Run("pending revocation precedes verification", func(t *testing.T) {
		fixture := create(t)
		target := fixture.input.Target
		transaction, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer transaction.Rollback(context.Background())
		if _, err := transaction.Exec(ctx, `INSERT INTO provider_credential_grant_events (contract,isolation_domain_id,revision_id,provider_profile,purpose,generation,operation,actor_id,reason_digest,correlation_id) VALUES ($1,$2,$3,'codex','agent-inference',2,'revoke','operator',$4,$5)`, fixture.grant.Contract, target.IsolationDomainID, target.RevisionID, fixture.grant.ReasonDigest, identity.New("cor")); err != nil {
			t.Fatal(err)
		}
		var calls atomic.Int32
		result := make(chan error, 1)
		waitingCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		go func() {
			_, err := repo.PublishDevelopmentRevision(waitingCtx, fixture.input, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
				calls.Add(1)
				return fixture.evidence, nil
			})
			result <- err
		}()
		key := "provider-credential-grant\n" + target.IsolationDomainID + "\n" + target.RevisionID + "\ncodex\nagent-inference"
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			var waiting bool
			if err := pool.QueryRow(waitingCtx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted AND classid::bigint=((hashtextextended($1,0)>>32)&4294967295) AND objid::bigint=(hashtextextended($1,0)&4294967295) AND objsubid=1)`, key).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			select {
			case err := <-result:
				t.Fatal("publication escaped pending revocation", err)
			case <-waitingCtx.Done():
				t.Fatal("publication did not wait")
			case <-ticker.C:
			}
		}
		if err := transaction.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-result; !errors.Is(err, persistence.ErrDevelopmentPublicationUnavailable) || calls.Load() != 0 {
			t.Fatal("revoked publication reached verifier", err, calls.Load())
		}
	})
	for _, mode := range []string{"plan", "policy", "service", "domain", "version", "verification", "cancelled", "image", "profile", "expired", "grant-revoked", "grant-expired-during-verification"} {
		t.Run(mode, func(t *testing.T) {
			fixture := create(t)
			original := fixture.input.Target
			switch mode {
			case "plan":
				fixture.input.PlanDigest = "sha256:" + strings.Repeat("1", 64)
			case "policy":
				fixture.input.PolicyDigest = "sha256:" + strings.Repeat("1", 64)
			case "service":
				fixture.input.Target.ServiceID = identity.New("svc")
			case "domain":
				fixture.input.Target.IsolationDomainID = identity.New("iso")
			case "version":
				fixture.input.ExpectedVersion = 2
			case "image":
				fixture.evidence.ImageReference = "other"
			case "profile":
				fixture.evidence.Profile = "other"
			case "expired":
				fixture.evidence.ExpiresAt = time.Now().Add(-time.Second)
			case "grant-revoked":
				revoke := fixture.grant
				revoke.Generation = 2
				revoke.Operation = "revoke"
				revoke.ActivatedAt = time.Time{}
				revoke.ExpiresAt = time.Time{}
				revoke.CorrelationID = identity.New("cor")
				if err := repo.ChangeProviderCredentialGrant(ctx, revoke); err != nil {
					t.Fatal(err)
				}
			case "grant-expired-during-verification":
				renew := fixture.grant
				renew.Generation = 2
				renew.ExpiresAt = time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
				renew.CorrelationID = identity.New("cor")
				if err := repo.ChangeProviderCredentialGrant(ctx, renew); err != nil {
					t.Fatal(err)
				}
			}
			publicationCtx, stopPublication := context.WithCancel(ctx)
			defer stopPublication()
			_, err := repo.PublishDevelopmentRevision(publicationCtx, fixture.input, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
				if mode == "cancelled" {
					stopPublication()
				}

				if mode == "verification" {
					return fixture.evidence, errors.New("private verification failure")
				}
				if mode == "grant-expired-during-verification" {
					time.Sleep(1100 * time.Millisecond)
				}
				return fixture.evidence, nil
			})
			if !errors.Is(err, persistence.ErrDevelopmentPublicationUnavailable) {
				t.Fatal("invalid publication accepted", err)
			}
			var state string
			var operations int
			if err := pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM service_publication_operations WHERE isolation_domain_id=$1) FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2`, original.IsolationDomainID, original.RevisionID).Scan(&state, &operations); err != nil || state != "draft" || operations != 0 {
				t.Fatal("rejected publication persisted effects", err)
			}
		})
	}
}

type developmentPublicationFixture struct {
	input    persistence.DevelopmentPublicationInput
	evidence persistence.DevelopmentPublicationEvidence
	grant    persistence.ProviderCredentialGrantChange
	policy   persistence.InvocationAuthorizationPolicyRecord
}

func newDevelopmentPublicationFixture(t *testing.T, ctx context.Context, repo *persistence.Repository, store *executionpostgres.Store) developmentPublicationFixture {
	t.Helper()
	return newDevelopmentPublicationFixtureWithPolicy(t, ctx, repo, store, nil)
}

func newDevelopmentPublicationFixtureWithPolicy(t *testing.T, ctx context.Context, repo *persistence.Repository, store *executionpostgres.Store, policies []byte) developmentPublicationFixture {
	t.Helper()
	scope, service, revision := identity.New("iso"), identity.New("svc"), identity.New("rev")
	idem := func(key string) persistence.Idempotency {
		return persistence.Idempotency{IsolationDomainID: scope, Method: "POST", Path: "/fixture", Key: key, RequestDigest: sha256.Sum256([]byte(key))}
	}
	if _, err := repo.CreateService(ctx, idem("create-service"), persistence.CreateServiceInput{ID: service, Name: "publication fixture", ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRevision(ctx, idem("create-revision"), persistence.CreateRevisionInput{ID: revision, ServiceID: service, RuntimeProfile: persistence.GovernedInvocationRuntimeProfile, RequiredCapabilities: []string{persistence.GovernedInvocationRuntimeProfile}, ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	policy, err := reconcile.NewInvocationAuthorizationPolicyWithApprovalEntities(reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: scope, ServiceID: service, RevisionID: revision}, "publication-policy", reconcile.CanonicalInvocationCedarApprovalSchema(), []byte("permit(principal, action, resource);"), persistenceEntityFixture(t))
	if policies != nil {
		policy, err = reconcile.NewInvocationAuthorizationPolicyWithPublicationEntities(reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: scope, ServiceID: service, RevisionID: revision}, "publication-policy", reconcile.CanonicalPublicationCedarSchema(), policies, persistenceEntityFixture(t))
	}
	if err != nil {
		t.Fatal(err)
	}
	reason := sha256.Sum256([]byte("reviewed fixture"))
	record := persistence.InvocationAuthorizationPolicyRecord{Contract: policy.Contract, IsolationDomainID: scope, ServiceID: service, RevisionID: revision, PolicySetID: policy.PolicySetID, PolicyDigest: policy.Digest[:], Schema: policy.Schema, Policies: policy.Policies, Entities: policy.Entities, InstalledBy: "operator", InstallationCorrelationID: identity.New("cor"), ReasonDigest: reason[:]}
	if err := repo.InstallInvocationAuthorizationPolicy(ctx, record); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:a1d56c0470c3264c4c37183352d783ebb67911d92ef2eb6ec5f7c76c61f69f39"
	bundle := execution.EnforcementBundleRecord{SchemaVersion: execution.EnforcementBundleSchemaV1, IsolationDomainID: scope, RevisionID: revision, ID: "strict-fixture", Digest: digest, MediaType: execution.EnforcementBundleMediaType, SizeBytes: 10, Provenance: execution.EnforcementBundleProvenance{Producer: "rosetta", SourceRevision: strings.Repeat("a", 40), CompilerVersion: "1.0.0", CatalogVersion: "rosetta/v1", TargetContractVersion: "rosetta/openshell-policy-v1", Mode: "strict", InputDigest: "sha256:" + strings.Repeat("a", 64), BindingDigest: "sha256:" + strings.Repeat("b", 64)}}
	if _, err := store.BindEnforcementBundle(ctx, execution.EnforcementBundleBinding{Record: bundle, ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	plan := execution.ExecutionPlan{SchemaVersion: execution.ExecutionPlanSchemaV1, IsolationDomainID: scope, RevisionID: revision, RuntimeProfile: persistence.GovernedInvocationRuntimeProfile, EnvironmentRevisionID: "environment", ImageReference: "ghcr.io/asabla/dataground-codex-candidate@sha256:" + strings.Repeat("c", 64), EnvironmentManifestDigest: "sha256:" + strings.Repeat("d", 64), EnforcementBundleID: bundle.ID, EnforcementBundleDigest: digest, RuntimeMatrixID: "matrix", RuntimeMatrixDigest: "sha256:" + strings.Repeat("e", 64), ProviderProfiles: []string{"codex"}, RequiredCapabilities: []string{persistence.GovernedInvocationRuntimeProfile}}
	if _, err := store.BindExecutionPlan(ctx, execution.ExecutionPlanBinding{Plan: plan, ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	planDigest, _ := execution.DigestExecutionPlan(plan)
	grant := persistence.ProviderCredentialGrantChange{Contract: persistence.ProviderCredentialGrantContract, IsolationDomainID: scope, RevisionID: revision, ProviderProfile: "codex", Purpose: persistence.ProviderCredentialPurposeAgentInference, Generation: 1, Operation: "activate", ActivatedAt: time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond), ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond), ActorID: "operator", ReasonDigest: reason[:], CorrelationID: identity.New("cor")}
	if err := repo.ChangeProviderCredentialGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	return developmentPublicationFixture{input: persistence.DevelopmentPublicationInput{Contract: persistence.DevelopmentPublicationContract, Target: persistence.InvocationDispatchTarget{IsolationDomainID: scope, ServiceID: service, RevisionID: revision, RuntimeProfile: persistence.GovernedInvocationRuntimeProfile}, ExpectedVersion: 1, PlanDigest: planDigest, PolicyDigest: "sha256:" + hex.EncodeToString(policy.Digest[:]), VerificationDigest: "sha256:" + strings.Repeat("f", 64), ActorID: "operator", CorrelationID: identity.New("cor")}, evidence: persistence.DevelopmentPublicationEvidence{AcceptanceID: identity.New("rtlocal"), Generation: 1, Profile: persistence.StrictDevelopmentPublicationProfile, ImageReference: plan.ImageReference, ExpiresAt: time.Now().UTC().Add(time.Hour)}, grant: grant, policy: record}
}
