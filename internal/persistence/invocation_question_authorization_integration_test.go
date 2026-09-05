package persistence_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func installQuestionAuthorizationFixture(t *testing.T, ctx context.Context, fixture *runtimeQuestionFixture, policies string) (*reconcile.InvocationAuthorizer, reconcile.InvocationAuthorizationPolicy) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := fixture.pool.Exec(cleanupCtx, `TRUNCATE invocation_question_authorization_decisions, invocation_authorization_policies CASCADE`); err != nil {
			t.Error(err)
		}
	})
	var entities cedar.EntityMap
	if err := json.Unmarshal([]byte(`[{"uid":{"type":"DataGround::Actor","id":"answerer"},"attrs":{},"parents":[]}]`), &entities); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(entities)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := reconcile.NewInvocationAuthorizationPolicyWithQuestionEntities(reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: fixture.target.IsolationDomainID, ServiceID: fixture.target.ServiceID, RevisionID: fixture.target.RevisionID}, "question-integration", reconcile.CanonicalInvocationCedarQuestionSchema(), []byte(policies), canonical)
	if err != nil {
		t.Fatal(err)
	}
	reason := sha256.Sum256([]byte("question fixture policy"))
	if err := fixture.repository.InstallInvocationAuthorizationPolicy(ctx, persistence.InvocationAuthorizationPolicyRecord{
		Contract: policy.Contract, IsolationDomainID: policy.IsolationDomainID, ServiceID: policy.ServiceID, RevisionID: policy.RevisionID, PolicySetID: policy.PolicySetID, PolicyDigest: policy.Digest[:], Schema: policy.Schema, Policies: policy.Policies, Entities: policy.Entities,
		InstalledBy: identity.New("usr"), InstallationCorrelationID: identity.New("cor"), ReasonDigest: reason[:],
	}); err != nil {
		t.Fatal(err)
	}
	source, err := reconcile.NewDurableInvocationAuthorizationPolicySource(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := reconcile.NewAuditedCedarInvocationAuthorizer(source, fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	return authorizer, policy
}

func TestQuestionAuthorizationCommitsExactAuditBeforeAnswerAndDelivery(t *testing.T) {
	for _, effectAllowed := range []bool{true, false} {
		t.Run(map[bool]string{true: "effect allowed", false: "effect denied"}[effectAllowed], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			fixture := newRuntimeQuestionFixture(t, ctx)
			policies := `permit(principal == DataGround::Actor::"answerer",action == DataGround::Action::"answer",resource) when { context.question.phase == "entry" };`
			if effectAllowed {
				policies += `permit(principal == DataGround::Actor::"answerer",action == DataGround::Action::"answer",resource) when { context.question.phase == "effect" };`
			}
			authorizer, policy := installQuestionAuthorizationFixture(t, ctx, fixture, policies)
			value := fixture.request(t, ctx, 20*time.Second)
			answer := questionAnswer(value)
			observer := answer
			observer.ActorID = "observer"
			if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, observer, authorizer.AuthorizeInvocationQuestion); !errors.Is(err, reconcile.ErrInvocationQuestionDenied) {
				t.Fatalf("observer answer: %v", err)
			}
			answered, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, answer, authorizer.AuthorizeInvocationQuestion)
			if err != nil || answered.State != "answered" {
				t.Fatalf("answer with independent audit: %s, %v", answered.State, err)
			}
			delivered, err := fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, authorizer.AuthorizeInvocationQuestion)
			if effectAllowed {
				if err != nil || delivered.State != "delivering" {
					t.Fatalf("delivery with independent audit: %s, %v", delivered.State, err)
				}
			} else if !errors.Is(err, reconcile.ErrInvocationQuestionDenied) {
				t.Fatalf("effect denial: %v", err)
			}
			rows, err := fixture.pool.Query(ctx, `SELECT actor_id,question_id,question_version,phase,outcome,policy_contract,question_count,free_text_count,selected_option_count,row_to_json(decision)::text FROM invocation_question_authorization_decisions decision WHERE isolation_domain_id=$1 ORDER BY sequence`, value.IsolationDomainID)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for rows.Next() {
				var actor, id, phase, outcome, contract, encoded string
				var version int64
				var questions, freeText, options int
				if err := rows.Scan(&actor, &id, &version, &phase, &outcome, &contract, &questions, &freeText, &options, &encoded); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				wantActor, wantPhase, wantOutcome, wantVersion := "answerer", "entry", "allowed", int64(1)
				if count == 0 {
					wantActor, wantOutcome = "observer", "denied"
				}
				if count == 2 {
					wantPhase, wantVersion = "effect", 2
					if !effectAllowed {
						wantOutcome = "denied"
					}
				}
				if actor != wantActor || id != value.ID || version != wantVersion || phase != wantPhase || outcome != wantOutcome || contract != policy.Contract || questions != 1 || freeText != 1 || options != 0 || strings.Contains(encoded, "sentinel") {
					rows.Close()
					t.Fatal("question authorization audit lost exact context or exposed content")
				}
				count++
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if count != 3 {
				t.Fatalf("question decisions: %d", count)
			}
			var legacyCount int
			if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM invocation_authorization_decisions WHERE isolation_domain_id=$1`, value.IsolationDomainID).Scan(&legacyCount); err != nil || legacyCount != 0 {
				t.Fatalf("legacy decision stream: %d, %v", legacyCount, err)
			}
			for _, sql := range []string{`UPDATE invocation_question_authorization_decisions SET outcome='allowed' WHERE isolation_domain_id=$1`, `DELETE FROM invocation_question_authorization_decisions WHERE isolation_domain_id=$1`} {
				if _, err := fixture.pool.Exec(ctx, sql, value.IsolationDomainID); err == nil {
					t.Fatal("question authorization evidence mutated")
				}
			}
			database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := persistence.MigrateDownTo(ctx, database, 50); err == nil {
				t.Fatal("question policy evidence permitted downgrade")
			}
		})
	}
}

func TestQuestionPolicyWithdrawalStopsAcceptedAnswerDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	authorizer, policy := installQuestionAuthorizationFixture(t, ctx, fixture, `permit(principal,action == DataGround::Action::"answer",resource);`)
	value := fixture.request(t, ctx, 20*time.Second)
	if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(value), authorizer.AuthorizeInvocationQuestion); err != nil {
		t.Fatal(err)
	}
	reason := sha256.Sum256([]byte("withdraw question policy"))
	if err := fixture.repository.WithdrawInvocationAuthorizationPolicy(ctx, persistence.InvocationAuthorizationPolicyWithdrawal{Contract: persistence.InvocationAuthorizationPolicyWithdrawalContract, IsolationDomainID: policy.IsolationDomainID, ServiceID: policy.ServiceID, RevisionID: policy.RevisionID, PolicyDigest: policy.Digest[:], WithdrawnBy: identity.New("usr"), ReasonDigest: reason[:], CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, authorizer.AuthorizeInvocationQuestion); !errors.Is(err, reconcile.ErrInvocationAuthorizationPolicyUnavailable) {
		t.Fatalf("withdrawn policy delivered answer: %v", err)
	}
	actual, err := fixture.repository.GetInvocationRuntimeQuestion(ctx, value.IsolationDomainID, value.InvocationID, value.ID)
	if err != nil || actual.State != "answered" {
		t.Fatalf("withdrawal changed delivery state: %s, %v", actual.State, err)
	}
	var decisions int
	if err := fixture.pool.QueryRow(ctx, `SELECT count(*) FROM invocation_question_authorization_decisions WHERE isolation_domain_id=$1`, value.IsolationDomainID).Scan(&decisions); err != nil || decisions != 1 {
		t.Fatalf("policy lookup failure mislabeled as completed decision: %d, %v", decisions, err)
	}
}

func TestQuestionEntityRefreshRemovesDeliveryAuthorityAfterAnswerAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	authorizer, policy := installQuestionAuthorizationFixture(t, ctx, fixture, `permit(principal in DataGround::Role::"invoker", action == DataGround::Action::"answer", resource);`)
	reason := sha256.Sum256([]byte("review question controller membership"))
	publish := func(number int64, member string) persistence.InvocationAuthorizationEntityActivation {
		t.Helper()
		entities := canonicalRefreshEntities(t, member)
		digest := sha256.Sum256(entities)
		if err := fixture.repository.PublishInvocationAuthorizationEntityGeneration(ctx, persistence.InvocationAuthorizationEntityGeneration{
			Contract: persistence.InvocationAuthorizationEntityGenerationContract, IsolationDomainID: policy.IsolationDomainID, ServiceID: policy.ServiceID, RevisionID: policy.RevisionID,
			Generation: number, Entities: entities, EntityDigest: digest[:], PublishedBy: "operator", CorrelationID: identity.New("cor"), ReasonDigest: reason[:],
		}); err != nil {
			t.Fatal(err)
		}
		effective := authz.InvocationAuthorizationPolicyV4Digest(policy.Schema, policy.Policies, entities)
		return persistence.InvocationAuthorizationEntityActivation{
			Contract: persistence.InvocationAuthorizationEntityActivationContract, IsolationDomainID: policy.IsolationDomainID, ServiceID: policy.ServiceID, RevisionID: policy.RevisionID,
			Generation: number, InstalledPolicyDigest: policy.Digest[:], EffectivePolicyDigest: effective[:], ActivatedBy: "operator", CorrelationID: identity.New("cor"), ReasonDigest: reason[:],
		}
	}
	first := publish(1, "answerer")
	if err := fixture.repository.ActivateInvocationAuthorizationEntityGeneration(ctx, first); err != nil {
		t.Fatal(err)
	}
	value := fixture.request(t, ctx, 20*time.Second)
	if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, questionAnswer(value), authorizer.AuthorizeInvocationQuestion); err != nil {
		t.Fatalf("current controller could not answer: %v", err)
	}
	second := publish(2, "replacement")
	staged, err := fixture.repository.GetActiveInvocationAuthorizationPolicy(ctx, policy.IsolationDomainID, policy.ServiceID, policy.RevisionID)
	if err != nil || !bytes.Equal(staged.PolicyDigest, first.EffectivePolicyDigest) {
		t.Fatalf("publication changed effective membership: %v", err)
	}
	if err := fixture.repository.ActivateInvocationAuthorizationEntityGeneration(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, value.ID, authorizer.AuthorizeInvocationQuestion); !errors.Is(err, reconcile.ErrInvocationQuestionDenied) {
		t.Fatalf("removed controller retained delivery authority: %v", err)
	}
	actual, err := fixture.repository.GetInvocationRuntimeQuestion(ctx, value.IsolationDomainID, value.InvocationID, value.ID)
	if err != nil || actual.State != "answered" || actual.Version != 2 || actual.AnsweredBy != "answerer" {
		t.Fatalf("denial changed frozen answer or reserved delivery: %s, %v", actual.State, err)
	}
	rows, err := fixture.pool.Query(ctx, `SELECT phase,outcome,policy_digest,actor_id FROM invocation_question_authorization_decisions WHERE isolation_domain_id=$1 ORDER BY sequence`, value.IsolationDomainID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for _, want := range []struct {
		phase, outcome string
		digest         []byte
	}{{"entry", "allowed", first.EffectivePolicyDigest}, {"effect", "denied", second.EffectivePolicyDigest}} {
		if !rows.Next() {
			t.Fatalf("missing %s decision: %v", want.phase, rows.Err())
		}
		var phase, outcome, actor string
		var digest string
		if err := rows.Scan(&phase, &outcome, &digest, &actor); err != nil {
			t.Fatal(err)
		}
		if phase != want.phase || outcome != want.outcome || actor != "answerer" || digest != "sha256:"+hex.EncodeToString(want.digest) {
			t.Fatal("question decision did not retain its exact effective membership provenance")
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatalf("unexpected additional question decision: %v", rows.Err())
	}
}
