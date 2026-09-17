package persistence_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestQueuedGovernedPublicationFencesVerificationAndPreservesRecovery(t *testing.T) {
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
		// Dedicated test database: release retention-protected fixtures before
		// the independent downgrade suite, without changing production guards.
		_, err := pool.Exec(context.Background(), `TRUNCATE governed_publication_requests;
            DELETE FROM service_publication_operations WHERE state_machine_version=3;
            TRUNCATE audit_records,invocation_authorization_entity_activations,invocation_authorization_entity_generations,
            invocation_authorization_policy_withdrawals,invocation_authorization_policies,
            provider_credential_authorization_decisions,provider_credential_grant_events`)
		if err != nil {
			t.Error(err)
		}
	}()
	newFixture := func(t *testing.T) (developmentPublicationFixture, persistence.Idempotency) {
		f := newDevelopmentPublicationFixture(t, ctx, repo, store)
		return f, persistence.Idempotency{IsolationDomainID: f.input.Target.IsolationDomainID, Method: "POST", Path: "/publication", Key: "queue-publication", RequestDigest: sha256.Sum256([]byte("publish"))}
	}
	claim := func(t *testing.T, f developmentPublicationFixture) persistence.OperationClaim {
		t.Helper()
		claim, err := repo.ClaimNextForServiceRevision(ctx, persistence.OperationKindPublication, f.input.Target.IsolationDomainID, f.input.Target.ServiceID, f.input.Target.RevisionID, "publication-worker", time.Minute)
		// Queue due_at is database time. A VM clock can lead the worker clock
		// slightly, so poll the exact request as the real worker does.
		for attempt := 0; err == nil && claim == nil && attempt < 100; attempt++ {
			time.Sleep(10 * time.Millisecond)
			claim, err = repo.ClaimNextForServiceRevision(ctx, persistence.OperationKindPublication, f.input.Target.IsolationDomainID, f.input.Target.ServiceID, f.input.Target.RevisionID, "publication-worker", time.Minute)
		}
		if err != nil || claim == nil {
			t.Fatal("claim", err)
		}
		if claim.ObservedState == "queued" {
			if err := repo.Advance(ctx, *claim, "validating", nil); err != nil {
				t.Fatal(err)
			}
			claim, err = repo.ClaimNextForServiceRevision(ctx, persistence.OperationKindPublication, f.input.Target.IsolationDomainID, f.input.Target.ServiceID, f.input.Target.RevisionID, "publication-worker", time.Minute)
			if err != nil || claim == nil {
				t.Fatal("validation claim", err)
			}
		}
		return *claim
	}
	assertDraft := func(t *testing.T, f developmentPublicationFixture) {
		t.Helper()
		var state string
		var events, evidence int
		if err := pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND event_type='service-publication.published'),(SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='development-publication.accept') FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2`, f.input.Target.IsolationDomainID, f.input.Target.RevisionID).Scan(&state, &events, &evidence); err != nil || state != "draft" || events != 0 || evidence != 0 {
			t.Fatal("failed verification published", state, events, evidence, err)
		}
	}
	t.Run("concurrent acceptance and exact recovery", func(t *testing.T) {
		f, idem := newFixture(t)
		results := make(chan persistence.CommandResult, 4)
		failures := make(chan error, 4)
		var group sync.WaitGroup
		for range 4 {
			group.Add(1)
			go func() {
				defer group.Done()
				result, err := repo.QueueDevelopmentPublication(ctx, idem, f.input, time.Now().Add(time.Hour))
				results <- result
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
		var operation domain.Operation
		for result := range results {
			if original == nil {
				original = result.Body
			}
			if result.Status != 202 || !bytes.Equal(original, result.Body) {
				t.Fatal("acceptance changed")
			}
			if !result.Replayed {
				fresh++
			}
		}
		if fresh != 1 || json.Unmarshal(original, &operation) != nil || operation.StateMachineVersion != 3 || operation.ObservedState != "queued" {
			t.Fatal("queue identity changed")
		}
		if operation, err := repo.FindAuthorizedDevelopmentPublication(ctx, f.input); err == nil || operation != nil {
			t.Fatal("discovery accepted operator request", err)
		}
		changed := f.input
		changed.PolicyDigest = "sha256:" + strings.Repeat("0", 64)
		if _, err := repo.QueueDevelopmentPublication(ctx, idem, changed, time.Now().Add(time.Hour)); err == nil {
			t.Fatal("changed pins replayed")
		}
		if c, err := repo.ClaimNextForRuntimeProfile(ctx, persistence.OperationKindPublication, "reference/v1", "reference-worker", time.Minute); err != nil || c != nil {
			t.Fatal("reference worker claimed governed publication", err)
		}
		c := claim(t, f)
		if err := repo.Advance(ctx, c, "applying", nil); err == nil {
			t.Fatal("reference effect route allowed")
		}
		if err := persistence.MigrateDownTo(ctx, db, 56); err == nil {
			t.Fatal("downgrade discarded queued publication")
		}
		if _, err := pool.Exec(ctx, `UPDATE governed_publication_requests SET policy_digest=$2 WHERE isolation_domain_id=$1`, f.input.Target.IsolationDomainID, changed.PolicyDigest); err == nil {
			t.Fatal("queued pins mutated")
		}
		if err := repo.CompleteDevelopmentPublication(ctx, c, f.input, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) { return f.evidence, nil }); err != nil {
			t.Fatal(err)
		}
		got, err := repo.GetOperation(ctx, f.input.Target.IsolationDomainID, operation.Metadata.ID)
		if err != nil || got.ObservedState != "published" || got.StateMachineVersion != 3 {
			t.Fatal(got, err)
		}
		replayInput := f.input
		replayInput.CorrelationID = identity.New("cor")
		replay, err := persistence.NewRepository(pool).QueueDevelopmentPublication(ctx, idem, replayInput, time.Now().Add(time.Hour))
		if err != nil || !replay.Replayed || !bytes.Equal(original, replay.Body) {
			t.Fatal("lost acceptance acknowledgement not recovered", err)
		}
		if err := repo.CompleteDevelopmentPublication(ctx, c, f.input, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
			t.Fatal("terminal publication reverified")
			return f.evidence, nil
		}); err == nil {
			t.Fatal("stale terminal claim reused")
		}
		var events, audits, version, effects int
		if err := pool.QueryRow(ctx, `SELECT version,(SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND event_type='service-publication.published'),(SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='development-publication.accept'),(SELECT count(*) FROM reference_runtime_receipts WHERE isolation_domain_id=$1) FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2`, f.input.Target.IsolationDomainID, f.input.Target.RevisionID).Scan(&version, &events, &audits, &effects); err != nil || version != 2 || events != 1 || audits != 1 || effects != 0 {
			t.Fatal("publication was not atomic", err)
		}
	})
	t.Run("fenced claim rejects before verifier", func(t *testing.T) {
		f, idem := newFixture(t)
		if _, err := repo.QueueDevelopmentPublication(ctx, idem, f.input, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		old := claim(t, f)
		if _, err := pool.Exec(ctx, `UPDATE service_publication_operations SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, old.IsolationDomainID, old.ID); err != nil {
			t.Fatal(err)
		}
		fresh := claim(t, f)
		if fresh.FencingToken <= old.FencingToken {
			t.Fatal("replacement did not fence")
		}
		if err := repo.CompleteDevelopmentPublication(ctx, old, f.input, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
			t.Fatal("stale verifier ran")
			return f.evidence, nil
		}); !errors.Is(err, persistence.ErrLeaseLost) {
			t.Fatal(err)
		}
		changed := f.input
		changed.VerificationDigest = "sha256:" + strings.Repeat("a", 64)
		if err := repo.CompleteDevelopmentPublication(ctx, fresh, changed, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
			t.Fatal("substituted verifier ran")
			return f.evidence, nil
		}); err == nil {
			t.Fatal("changed verifier accepted")
		}
		assertDraft(t, f)
		if err := repo.CompleteDevelopmentPublication(ctx, fresh, f.input, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) { return f.evidence, nil }); err != nil {
			t.Fatal(err)
		}
	})
	for _, failure := range []string{"expired acceptance", "grant revoked", "policy withdrawn", "lease expired", "operation expired", "verifier failed", "cancelled context", "wrong actor", "wrong domain"} {
		t.Run(failure, func(t *testing.T) {
			f, idem := newFixture(t)
			if _, err := repo.QueueDevelopmentPublication(ctx, idem, f.input, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			c := claim(t, f)
			testCtx, testCancel := context.WithCancel(ctx)
			defer testCancel()
			switch failure {
			case "grant revoked":
				revoke := f.grant
				revoke.Generation = 2
				revoke.Operation = "revoke"
				revoke.ActivatedAt = time.Time{}
				revoke.ExpiresAt = time.Time{}
				revoke.CorrelationID = identity.New("cor")
				if err := repo.ChangeProviderCredentialGrant(ctx, revoke); err != nil {
					t.Fatal(err)
				}
			case "policy withdrawn":
				reason := sha256.Sum256([]byte("withdraw"))
				if err := repo.WithdrawInvocationAuthorizationPolicy(ctx, persistence.InvocationAuthorizationPolicyWithdrawal{Contract: persistence.InvocationAuthorizationPolicyWithdrawalContract, IsolationDomainID: c.IsolationDomainID, ServiceID: f.input.Target.ServiceID, RevisionID: c.ResourceID, PolicyDigest: f.policy.PolicyDigest, WithdrawnBy: "operator", CorrelationID: identity.New("cor"), ReasonDigest: reason[:]}); err != nil {
					t.Fatal(err)
				}
			case "lease expired":
				if _, err := pool.Exec(ctx, `UPDATE service_publication_operations SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, c.IsolationDomainID, c.ID); err != nil {
					t.Fatal(err)
				}
			case "operation expired":
				if _, err := pool.Exec(ctx, `UPDATE service_publication_operations SET deadline_at=clock_timestamp()-interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, c.IsolationDomainID, c.ID); err != nil {
					t.Fatal(err)
				}
			case "wrong actor":
				c.ActorID = "intruder"
				f.input.ActorID = "intruder"
			case "wrong domain":
				c.IsolationDomainID = identity.New("iso")
			}
			err := repo.CompleteDevelopmentPublication(testCtx, c, f.input, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
				switch failure {
				case "expired acceptance":
					f.evidence.ExpiresAt = time.Now().Add(-time.Hour)
				case "verifier failed":
					return f.evidence, errors.New("private verifier output")
				case "cancelled context":
					testCancel()
				default:
					t.Fatal("invalid state reached verifier")
				}
				return f.evidence, nil
			})
			if err == nil || strings.Contains(err.Error(), "private verifier") {
				t.Fatal("invalid publication accepted or leaked", err)
			}
			assertDraft(t, f)
		})
	}
	t.Run("audit failure rolls back completion", func(t *testing.T) {
		f, idem := newFixture(t)
		if _, err := repo.QueueDevelopmentPublication(ctx, idem, f.input, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		c := claim(t, f)
		if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_queued_publication_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
            IF NEW.action='development-publication.accept' THEN RAISE EXCEPTION 'injected publication audit failure'; END IF; RETURN NEW; END; $$;
            CREATE TRIGGER reject_queued_publication_audit BEFORE INSERT ON audit_records FOR EACH ROW EXECUTE FUNCTION reject_queued_publication_audit()`); err != nil {
			t.Fatal(err)
		}
		defer pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS reject_queued_publication_audit ON audit_records; DROP FUNCTION IF EXISTS reject_queued_publication_audit()`)
		verify := func(context.Context) (persistence.DevelopmentPublicationEvidence, error) { return f.evidence, nil }
		if err := repo.CompleteDevelopmentPublication(ctx, c, f.input, verify); err == nil {
			t.Fatal("audit failure committed publication")
		}
		assertDraft(t, f)
		op, err := repo.GetOperation(ctx, c.IsolationDomainID, c.ID)
		if err != nil || op.ObservedState != "validating" || op.TerminalResult != nil {
			t.Fatal("operation update escaped rollback", err)
		}
		if _, err := pool.Exec(ctx, `DROP TRIGGER reject_queued_publication_audit ON audit_records; DROP FUNCTION reject_queued_publication_audit()`); err != nil {
			t.Fatal(err)
		}
		if err := repo.CompleteDevelopmentPublication(ctx, c, f.input, verify); err != nil {
			t.Fatal("read-only verification could not retry after rollback", err)
		}
	})
	t.Run("lease expires during verification", func(t *testing.T) {
		f, idem := newFixture(t)
		if _, err := repo.QueueDevelopmentPublication(ctx, idem, f.input, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		c := claim(t, f)
		if _, err := pool.Exec(ctx, `UPDATE service_publication_operations SET lease_expires_at=clock_timestamp()+interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, c.IsolationDomainID, c.ID); err != nil {
			t.Fatal(err)
		}
		called := false
		err := repo.CompleteDevelopmentPublication(ctx, c, f.input, func(verifyCtx context.Context) (persistence.DevelopmentPublicationEvidence, error) {
			called = true
			// Even an uncooperative verifier that returns a valid proof after
			// its deadline cannot commit a publication with the stale lease.
			<-verifyCtx.Done()
			return f.evidence, nil
		})
		if !called || err == nil {
			t.Fatal("expired in-flight verification published", err)
		}
		assertDraft(t, f)
	})
	t.Run("repair preserves inputs and changes effective actor", func(t *testing.T) {
		f, idem := newFixture(t)
		if _, err := repo.QueueDevelopmentPublication(ctx, idem, f.input, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		c := claim(t, f)
		if err := repo.Fail(ctx, c, persistence.OperationFailureEffectInvalid); err != nil {
			t.Fatal(err)
		}
		correlation := identity.New("cor")
		if err := repo.RepairOperation(ctx, c.Kind, c.IsolationDomainID, c.ID, "repair-operator", "retry unchanged reviewed inputs", correlation, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		repaired := claim(t, f)
		if repaired.Command != "repair" || repaired.ActorID != "repair-operator" || repaired.CorrelationID != correlation || repaired.FencingToken <= c.FencingToken {
			t.Fatal("repair did not retain current authority")
		}
		if err := repo.CompleteDevelopmentPublication(ctx, repaired, f.input, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
			t.Fatal("original actor used after repair")
			return f.evidence, nil
		}); err == nil {
			t.Fatal("stale repair actor accepted")
		}
		f.input.ActorID = repaired.ActorID
		f.input.CorrelationID = repaired.CorrelationID
		if err := repo.CompleteDevelopmentPublication(ctx, repaired, f.input, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) { return f.evidence, nil }); err != nil {
			t.Fatal(err)
		}
		var actor string
		if err := pool.QueryRow(ctx, `SELECT actor_id FROM audit_records WHERE isolation_domain_id=$1 AND action='development-publication.accept'`, c.IsolationDomainID).Scan(&actor); err != nil || actor != "repair-operator" {
			t.Fatal("repair audit attributed to original caller", err)
		}
	})
	t.Run("consumer claims only independently pinned exact operation", func(t *testing.T) {
		f, idem := newFixture(t)
		result, err := repo.QueueDevelopmentPublication(ctx, idem, f.input, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		var operation domain.Operation
		if json.Unmarshal(result.Body, &operation) != nil {
			t.Fatal("invalid acceptance")
		}
		input := f.input
		input.ActorID, input.CorrelationID = "", ""
		for _, field := range []string{"domain", "service", "revision", "version", "plan", "policy", "verification", "operation"} {
			changed, id := input, operation.Metadata.ID
			switch field {
			case "domain":
				changed.Target.IsolationDomainID = identity.New("iso")
			case "service":
				changed.Target.ServiceID = identity.New("svc")
			case "revision":
				changed.Target.RevisionID = identity.New("rev")
			case "version":
				changed.ExpectedVersion++
			case "plan":
				changed.PlanDigest = "sha256:" + strings.Repeat("0", 64)
			case "policy":
				changed.PolicyDigest = "sha256:" + strings.Repeat("0", 64)
			case "verification":
				changed.VerificationDigest = "sha256:" + strings.Repeat("0", 64)
			case "operation":
				id = identity.New("op")
			}
			if c, err := repo.ClaimDevelopmentPublication(ctx, id, changed, "substituted-worker", time.Minute); err == nil || c != nil {
				t.Fatal("changed consumer input claimed operation", field)
			}
		}
		getClaim := func(worker string) *persistence.OperationClaim {
			t.Helper()
			for attempt := 0; attempt < 100; attempt++ {
				c, err := repo.ClaimDevelopmentPublication(ctx, operation.Metadata.ID, input, worker, 2*time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				if c != nil {
					return c
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("exact consumer did not claim operation")
			return nil
		}
		first := getClaim("consumer-a")
		if first.Attempt != 1 || first.ID != operation.Metadata.ID || first.ActorID != f.input.ActorID {
			t.Fatal("rejected claims changed accepted operation")
		}
		if c, err := repo.ClaimDevelopmentPublication(ctx, operation.Metadata.ID, input, "contender", time.Minute); err != nil || c != nil {
			t.Fatal("contender stole active lease", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE service_publication_operations SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE isolation_domain_id=$1 AND id=$2`, first.IsolationDomainID, first.ID); err != nil {
			t.Fatal(err)
		}
		replacement := getClaim("consumer-b")
		if replacement.FencingToken <= first.FencingToken {
			t.Fatal("replacement did not fence old consumer")
		}
		if err := repo.Advance(ctx, *replacement, "validating", nil); err != nil {
			t.Fatal(err)
		}
		validating := getClaim("consumer-b")
		if err := repo.CompleteDevelopmentPublication(ctx, *validating, f.input, func(context.Context) (persistence.DevelopmentPublicationEvidence, error) { return f.evidence, nil }); err != nil {
			t.Fatal(err)
		}
		if err := persistence.NewRepository(pool).RequireDevelopmentPublication(ctx, operation.Metadata.ID, input); err != nil {
			t.Fatal("replacement could not inspect exact terminal operation", err)
		}
		if c, err := repo.ClaimDevelopmentPublication(ctx, operation.Metadata.ID, input, "terminal-replay", time.Minute); err != nil || c != nil {
			t.Fatal("terminal operation reclaimed", err)
		}
	})

}
