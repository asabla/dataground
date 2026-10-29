package persistence_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
)

// Publication is a database-only transition. These fixtures install catalog
// metadata directly; source byte and S3 conformance are tested separately.
func preparePublicationSourceFixture(t *testing.T, ctx context.Context, repo *persistence.Repository, scope string) workspace.SourceRecord {
	t.Helper()
	source := workspace.SourceRecord{SchemaVersion: workspace.SourceRecordV1, IsolationDomainID: scope, WorkspaceID: identity.New("wsp"), ID: identity.New("src"), ArchiveDigest: "sha256:" + strings.Repeat("a", 64), StorageProfileDigest: "sha256:" + strings.Repeat("b", 64), ArchiveBytes: 2048, EntryCount: 1, SourceBytes: 1}
	if _, err := repo.CreateWorkspace(ctx, testIdempotency(scope, "publication-workspace"), persistence.CreateWorkspaceInput{ID: source.WorkspaceID, Name: "Source publication", ActorID: "operator", CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	installation := workspace.SourceInstallation{Record: source, ActorID: "operator", CorrelationID: identity.New("cor")}
	if _, err := repo.PrepareWorkspaceSource(ctx, installation); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CompleteWorkspaceSource(ctx, installation); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestWorkspaceDevelopmentPublicationPinsSourceBindingAndAtomicReplay(t *testing.T) {
	t.Run("source", func(t *testing.T) { testWorkspaceDevelopmentPublication(t, false) })
	t.Run("owned", func(t *testing.T) { testWorkspaceDevelopmentPublication(t, true) })
}

func testWorkspaceDevelopmentPublication(t *testing.T, owned bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := resetOperatorAuditDatabase(t, ctx)
	defer pool.Close()
	defer func() {
		if _, err := pool.Exec(context.Background(), `TRUNCATE workspace_change_export_decisions,invocation_workspace_quiescence_completions,invocation_workspace_quiescence_intents,invocation_workspace_change_completions,invocation_workspace_change_exports,invocation_workspace_completions,invocation_workspace_preparations,service_revision_execution_plans,workspace_source_grants,workspace_source_use_decisions,workspace_source_completions,workspace_source_installations,workspaces,
 audit_records,invocation_authorization_entity_activations,invocation_authorization_entity_generations,invocation_authorization_policy_withdrawals,invocation_authorization_policies,provider_credential_authorization_decisions,provider_credential_grant_events CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	repo := persistence.NewRepository(pool)
	store := executionpostgres.New(pool)
	create := func(t *testing.T) developmentPublicationFixture {
		return newDevelopmentPublicationFixtureForProfile(t, ctx, repo, store, nil, true, owned)
	}
	t.Run("publication precedes source grants and replay remains read-only", func(t *testing.T) {
		fixture := create(t)
		calls := 0
		verify := func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
			calls++
			return fixture.evidence, nil
		}
		result, err := repo.PublishDevelopmentRevision(ctx, fixture.input, verify)
		if err != nil || result.Replayed || calls != 1 {
			t.Fatal("source publication failed", err)
		}
		var receipt map[string]any
		if json.Unmarshal(result.Body, &receipt) != nil || receipt["contract"] != fixture.input.Contract || len(receipt) != 7 {
			t.Fatal("unsafe source publication receipt")
		}
		var metadata []byte
		if err := pool.QueryRow(ctx, `SELECT safe_metadata FROM audit_records WHERE isolation_domain_id=$1 AND action='development-publication.accept'`, fixture.input.Target.IsolationDomainID).Scan(&metadata); err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if json.Unmarshal(metadata, &fields) != nil || fields["publicationContract"] != fixture.input.Contract {
			t.Fatal("missing source publication audit provenance")
		}
		if owned {
			db, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := persistence.MigrateDownTo(ctx, db, 82); err == nil {
				t.Fatal("downgrade removed owned atomic publication support")
			}
		}
		var grants int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspace_source_grants WHERE isolation_domain_id=$1`, fixture.input.Target.IsolationDomainID).Scan(&grants); err != nil || grants != 0 {
			t.Fatal("publication created source authority", grants, err)
		}
		if err := repo.WithdrawInvocationAuthorizationPolicy(ctx, persistence.InvocationAuthorizationPolicyWithdrawal{
			Contract:          persistence.InvocationAuthorizationPolicyWithdrawalContract,
			IsolationDomainID: fixture.policy.IsolationDomainID, ServiceID: fixture.policy.ServiceID,
			RevisionID: fixture.policy.RevisionID, PolicyDigest: fixture.policy.PolicyDigest,
			WithdrawnBy: "operator", CorrelationID: identity.New("cor"), ReasonDigest: fixture.policy.ReasonDigest,
		}); err != nil {
			t.Fatal(err)
		}
		replay, err := repo.PublishDevelopmentRevision(ctx, fixture.input, verify)
		if err != nil || !replay.Replayed || calls != 1 || string(replay.Body) != string(result.Body) {
			t.Fatal("exact replay repeated verification or changed receipt", err)
		}
		changed := fixture.input
		changed.Contract = persistence.DevelopmentPublicationContract
		if owned {
			changed.Contract = persistence.WorkspaceDevelopmentPublicationContract
		}
		if _, err := repo.PublishDevelopmentRevision(ctx, changed, verify); err == nil {
			t.Fatal("replay replaced publication contract")
		}
	})
	for _, mode := range []string{"wrong-profile", "legacy-contract", "policy-withdrawn", "wrong-scope", "plan-digest", "expired-acceptance"} {
		t.Run(mode, func(t *testing.T) {
			fixture := create(t)
			switch mode {
			case "plan-digest":
				fixture.input.PlanDigest = "sha256:" + strings.Repeat("f", 64)
			case "expired-acceptance":
				fixture.evidence.ExpiresAt = time.Now().Add(-time.Second)
			case "wrong-profile":
				fixture.evidence.Profile = persistence.StrictDevelopmentPublicationProfile
				if owned {
					fixture.evidence.Profile = persistence.WorkspaceDevelopmentPublicationProfile
				}
			case "legacy-contract":
				fixture.input.Contract = persistence.DevelopmentPublicationContract
				if owned {
					fixture.input.Contract = persistence.WorkspaceDevelopmentPublicationContract
				}
			case "wrong-scope":
				fixture.input.Target.IsolationDomainID = identity.New("iso")
			case "policy-withdrawn":
				err := repo.WithdrawInvocationAuthorizationPolicy(ctx, persistence.InvocationAuthorizationPolicyWithdrawal{
					Contract:          persistence.InvocationAuthorizationPolicyWithdrawalContract,
					IsolationDomainID: fixture.policy.IsolationDomainID, ServiceID: fixture.policy.ServiceID,
					RevisionID: fixture.policy.RevisionID, PolicyDigest: fixture.policy.PolicyDigest,
					WithdrawnBy: "operator", CorrelationID: identity.New("cor"), ReasonDigest: fixture.policy.ReasonDigest,
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			verify := func(context.Context) (persistence.DevelopmentPublicationEvidence, error) {
				return fixture.evidence, nil
			}

			if _, err := repo.PublishDevelopmentRevision(ctx, fixture.input, verify); err == nil {
				t.Fatal("invalid source authority published")
			}
			var state string
			var count int
			if err := pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND action='development-publication.accept') FROM service_revisions WHERE isolation_domain_id=$1 AND id=$2`, fixture.source.IsolationDomainID, fixture.input.Target.RevisionID).Scan(&state, &count); err != nil || state != "draft" || count != 0 {
				t.Fatal("failed publication retained state or evidence", state, count, err)
			}
		})
	}
	t.Run("source intent cannot enter unversioned queues", func(t *testing.T) {
		fixture := create(t)
		if _, err := repo.QueueDevelopmentPublication(ctx, testIdempotency(fixture.input.Target.IsolationDomainID, "source-queue"), fixture.input, time.Now().Add(time.Minute)); err == nil {
			t.Fatal("source intent entered old queue")
		}
		if found, err := repo.FindAuthorizedDevelopmentPublication(ctx, fixture.input); err != nil || found != nil {
			t.Fatal("source draft did not wait for authorized publication", err)
		}
	})
}
