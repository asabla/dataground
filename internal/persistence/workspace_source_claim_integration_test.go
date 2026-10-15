package persistence_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reuse the real catalog, grant, storage, and publication fixture. CI also runs
// this composition against the pinned disposable object backend.
func testClaimedWorkspaceSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, record workspace.SourceRecord, service, revision string, objects workspace.SourceObjects) {
	repo := persistence.NewRepository(pool)
	sourcePolicy := installSourceReadPolicy(t, ctx, pool, record.IsolationDomainID, service, revision)
	scope := record.IsolationDomainID
	if _, err := repo.AssignAlias(ctx, testIdempotency(scope, "source-claim-alias"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: service, Name: "source", RevisionID: revision, ActorID: "publisher", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	invocationID, correlation := identity.New("inv"), identity.New("cor")
	if _, err := repo.AcceptInvocation(ctx, testIdempotency(scope, "source-claim-invocation"), persistence.AcceptInvocationInput{ID: invocationID, ServiceID: service, Alias: "source", Input: map[string]any{"prompt": "inspect source"}, ActorID: "source-requester", CorrelationID: correlation, Deadline: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	worker := reconcile.New(repo, reconcile.NewReferenceDriver(pool), "source-claim-setup")
	for range 2 {
		if ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil || !ran {
			t.Fatal("advance invocation", ran, err)
		}
	}
	claim, err := repo.ClaimNext(ctx, persistence.OperationKindInvocation, "source-reader", time.Minute)
	if err != nil || claim == nil {
		t.Fatal("claim invocation", err)
	}
	effect, err := repo.PrepareEffect(ctx, *claim, "run-invocation", sha256.Sum256([]byte("source-runtime")))
	if err != nil {
		t.Fatal(err)
	}
	observed := &sourceReadObserver{SourceObjects: objects}
	read := func(c persistence.OperationClaim, e persistence.EffectRecord, r workspace.SourceRecord, g int64) (*workspace.Snapshot, error) {
		return persistence.NewRepository(pool).ReadClaimedWorkspaceSource(ctx, c, e, observed, r, g, &reconcile.SourceCedarEvaluator{})
	}
	if snapshot, err := read(*claim, effect, record, 1); snapshot != nil || err == nil || observed.reads.Load() != 0 {
		t.Fatal("unreserved runtime accessed source", err)
	}
	preparation := testWorkspacePreparation(t, ctx, pool, claim, effect, record, objects)
	if _, err := repo.BeginInvocationRuntimeAttempt(ctx, *claim, effect); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ReadPreparingWorkspaceSource(ctx, *claim, effect, observed, preparation, &reconcile.SourceCedarEvaluator{}); err == nil || observed.reads.Load() != 0 {
		t.Fatal("preparation continued after runtime reservation")
	}
	// Attribution and timestamps come from the stored operation, not these hints.
	claim.ActorID = "forged-actor"
	claim.CorrelationID = identity.New("cor")
	claim.LeaseExpiresAt = time.Now().Add(24 * time.Hour)
	claim.DeadlineAt = claim.LeaseExpiresAt
	snapshot, err := read(*claim, effect, record, 1)
	if err != nil || snapshot == nil {
		t.Fatal("claimed source read", err)
	}
	if _, err := snapshot.Materialize(ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_source_use_decisions WHERE isolation_domain_id=$1 AND operation_id=$2 AND claim_contract='dataground.invocation-source-read/v3' AND policy_contract='dataground.invocation-authorization-policy/v6' AND policy_outcome='allowed' AND invocation_id=$3 AND effect_id=$4 AND claim_owner=$5 AND claim_token=$6 AND actor_id='source-requester' AND correlation_id=$7 AND outcome='allowed' AND claim_expires_at<clock_timestamp()+interval '2 minutes'`, scope, claim.ID, invocationID, effect.EffectID, claim.LeaseOwner, claim.FencingToken, correlation).Scan(&count); err != nil || count != 2 {
		t.Fatal("claim attribution audit", count, err)
	}
	for name, mutate := range map[string]func(*persistence.OperationClaim, *persistence.EffectRecord, *workspace.SourceRecord){
		"domain": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *workspace.SourceRecord) {
			c.IsolationDomainID = identity.New("iso")
		},
		"operation": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *workspace.SourceRecord) {
			c.ID = identity.New("op")
		},
		"invocation": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *workspace.SourceRecord) {
			c.ResourceID = identity.New("inv")
		},
		"owner": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *workspace.SourceRecord) {
			c.LeaseOwner = "replacement"
		},
		"token": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *workspace.SourceRecord) {
			c.FencingToken++
		},
		"version": func(c *persistence.OperationClaim, _ *persistence.EffectRecord, _ *workspace.SourceRecord) {
			c.StateMachineVersion++
		},
		"effect digest": func(_ *persistence.OperationClaim, e *persistence.EffectRecord, _ *workspace.SourceRecord) {
			e.RequestDigest[0]++
		},
		"effect phase": func(_ *persistence.OperationClaim, e *persistence.EffectRecord, _ *workspace.SourceRecord) {
			e.Phase = "start-invocation"
		},
		"source domain": func(_ *persistence.OperationClaim, _ *persistence.EffectRecord, r *workspace.SourceRecord) {
			r.IsolationDomainID = identity.New("iso")
		},
		"source workspace": func(_ *persistence.OperationClaim, _ *persistence.EffectRecord, r *workspace.SourceRecord) {
			r.WorkspaceID = identity.New("wsp")
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, e, r := *claim, effect, record
			mutate(&c, &e, &r)
			before := observed.reads.Load()
			if s, err := read(c, e, r, 1); s != nil || err == nil || observed.reads.Load() != before {
				t.Fatal("changed claim reached source", err)
			}
		})
	}
	before := observed.reads.Load()
	if s, err := read(*claim, effect, record, 2); s != nil || !errors.Is(err, workspace.ErrSourceUnauthorized) || observed.reads.Load() != before {
		t.Fatal("generation substitution reached source", err)
	}
	// A completed attempt is not permission for another source read.
	if _, err := pool.Exec(ctx, `UPDATE invocation_runtime_attempts SET status='failed',result='{}',completed_at=clock_timestamp() WHERE isolation_domain_id=$1 AND operation_id=$2`, scope, claim.ID); err != nil {
		t.Fatal(err)
	}
	if s, err := read(*claim, effect, record, 1); s != nil || err == nil || observed.reads.Load() != before {
		t.Fatal("completed attempt reached source", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE invocation_runtime_attempts SET status='reserved',result=NULL,completed_at=NULL WHERE isolation_domain_id=$1 AND operation_id=$2`, scope, claim.ID); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"lease_token", "observed_state", "lease_expires_at", "deadline_at"} {
		t.Run("during read "+field, func(t *testing.T) {
			var restore string
			observed.beforeRead = func() {
				var update string
				switch field {
				case "lease_token":
					update = "lease_token=lease_token+1"
					restore = "lease_token=lease_token-1"
				case "observed_state":
					update = "observed_state='cancelling'"
					restore = "observed_state='running'"
				case "lease_expires_at":
					update = "lease_expires_at=clock_timestamp()-interval '1 second'"
					restore = "lease_expires_at=clock_timestamp()+interval '1 minute'"
				case "deadline_at":
					update = "deadline_at=clock_timestamp()-interval '1 second'"
					restore = "deadline_at=clock_timestamp()+interval '1 minute'"
				}
				if _, err := pool.Exec(ctx, "UPDATE invocation_execution_operations SET "+update+" WHERE isolation_domain_id=$1 AND id=$2", scope, claim.ID); err != nil {
					t.Fatal(err)
				}
			}
			s, err := read(*claim, effect, record, 1)
			observed.beforeRead = nil
			if _, restoreErr := pool.Exec(ctx, "UPDATE invocation_execution_operations SET "+restore+" WHERE isolation_domain_id=$1 AND id=$2", scope, claim.ID); restoreErr != nil {
				t.Fatal(restoreErr)
			}
			if s != nil || err == nil {
				t.Fatal("changed runtime claim released content", err)
			}
		})
	}
	// Renewing a lease during the read is safe; the original read deadline remains.
	observed.beforeRead = func() {
		if _, err := repo.RenewLease(ctx, *claim, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if s, err := read(*claim, effect, record, 1); s == nil || err != nil {
		t.Fatal("lease renewal broke read", err)
	}
	observed.beforeRead = nil
	// Removing membership between phases must prevent delivery under the new
	// effective digest; restoring membership authorizes only a subsequent read.
	observed.beforeRead = func() { changeSourceReadMembership(t, ctx, pool, sourcePolicy, 1, false) }
	if s, err := read(*claim, effect, record, 1); s != nil || !errors.Is(err, workspace.ErrSourceUnauthorized) {
		t.Fatal("Cedar withdrawal released source", err)
	}
	observed.beforeRead = nil
	before = observed.reads.Load()
	if s, err := read(*claim, effect, record, 1); s != nil || !errors.Is(err, workspace.ErrSourceUnauthorized) || observed.reads.Load() != before {
		t.Fatal("Cedar denial reached storage", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_source_use_decisions WHERE isolation_domain_id=$1 AND operation_id=$2 AND claim_contract='dataground.invocation-source-read/v3' AND policy_outcome='denied' AND outcome='denied'`, scope, claim.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("Cedar denials not retained", count, err)
	}
	changeSourceReadMembership(t, ctx, pool, sourcePolicy, 2, true)
	if s, err := read(*claim, effect, record, 1); s == nil || err != nil {
		t.Fatal("refreshed source authority failed", err)
	}
	before = observed.reads.Load()
	if s, err := repo.ReadClaimedWorkspaceSource(ctx, *claim, effect, observed, record, 1, unavailableSourceEvaluator{}); s != nil || !errors.Is(err, workspace.ErrSourceUnavailable) || observed.reads.Load() != before {
		t.Fatal("unavailable Cedar reached storage", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_source_use_decisions WHERE isolation_domain_id=$1 AND operation_id=$2 AND claim_contract='dataground.invocation-source-read/v3' AND policy_outcome='unavailable' AND outcome='denied'`, scope, claim.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("unavailable evaluation not retained", count, err)
	}
	var missing *reconcile.SourceCedarEvaluator
	if s, err := repo.ReadClaimedWorkspaceSource(ctx, *claim, effect, observed, record, 1, missing); s != nil || !errors.Is(err, workspace.ErrSourceInvalid) {
		t.Fatal("typed nil evaluator accepted", err)
	}
	// An audit failure after storage access must not release the private snapshot.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_claim_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.claim_contract IS NOT NULL AND NEW.phase='read-delivery' THEN RAISE EXCEPTION 'injected claim audit failure'; END IF; RETURN NEW; END; $$; CREATE TRIGGER reject_claim_delivery BEFORE INSERT ON workspace_source_use_decisions FOR EACH ROW EXECUTE FUNCTION reject_claim_delivery()`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS reject_claim_delivery() CASCADE`)
	if s, err := read(*claim, effect, record, 1); s != nil || !errors.Is(err, workspace.ErrSourceUnavailable) {
		t.Fatal("delivery audit failure released content", err)
	}
	if _, err := pool.Exec(ctx, `DROP FUNCTION reject_claim_delivery() CASCADE`); err != nil {
		t.Fatal(err)
	}
	// Forged future timestamps cannot extend the database lease deadline.
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()+interval '200 milliseconds' WHERE isolation_domain_id=$1 AND id=$2`, scope, claim.ID); err != nil {
		t.Fatal(err)
	}
	observed.beforeRead = func() { time.Sleep(250 * time.Millisecond) }
	if s, err := read(*claim, effect, record, 1); s != nil || err == nil {
		t.Fatal("expired lease released content", err)
	}
	observed.beforeRead = nil
	// Direct SQL withdrawal takes the same policy lock as reads. A blocked read
	// must observe the committed withdrawal before it can reach storage.
	if _, err := pool.Exec(ctx, `UPDATE invocation_execution_operations SET lease_expires_at=clock_timestamp()+interval '1 minute' WHERE isolation_domain_id=$1 AND id=$2`, scope, claim.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO invocation_authorization_policy_withdrawals(contract,isolation_domain_id,service_id,revision_id,policy_digest,withdrawn_by,reason_digest,correlation_id) VALUES ($1,$2,$3,$4,$5,'operator',$6,$7)`, persistence.InvocationAuthorizationPolicyWithdrawalContract, scope, service, revision, sourcePolicy.PolicyDigest, sourcePolicy.ReasonDigest, identity.New("cor")); err != nil {
		t.Fatal(err)
	}
	before = observed.reads.Load()
	result := make(chan error, 1)
	go func() {
		s, err := read(*claim, effect, record, 1)
		if s != nil {
			result <- errors.New("withdrawn source delivered")
			return
		}
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatal("read did not serialize with withdrawal", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, workspace.ErrSourceUnauthorized) || observed.reads.Load() != before {
			t.Fatal("withdrawn policy reached source", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := persistence.MigrateDownTo(ctx, database, 66); err == nil {
		t.Fatal("downgrade removed claim evidence")
	}
}
