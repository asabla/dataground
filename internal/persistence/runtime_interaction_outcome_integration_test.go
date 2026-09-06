package persistence_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestInteractionOutcomesShareSafeDurableJournal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture := newExpiringApprovalFixture(t, ctx)
	for _, kind := range []string{"approval", "question"} {
		for _, phase := range []string{"pending", "resolved", "delivering"} {
			id := ""
			version := int64(2)
			state := "closed"
			if phase == "resolved" {
				version = 3
			} else if phase == "delivering" {
				version, state = 4, "delivery_unknown"
			}
			if kind == "approval" {
				value := requestExpiringApproval(t, ctx, fixture, 15*time.Second)
				id = value.ID
				if phase != "pending" {
					if _, err := fixture.repository.ResolveInvocationRuntimeApproval(ctx, expiringApprovalResolution(value)); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "delivering" {
					if _, err := fixture.repository.BeginInvocationRuntimeApprovalDelivery(ctx, fixture.claim, fixture.effect, id, "deny"); err != nil {
						t.Fatal(err)
					}
				}
				for range 2 {
					if _, err := persistence.NewRepository(fixture.pool).CloseInvocationRuntimeApproval(ctx, fixture.claim, fixture.effect, id, "runtime-ended"); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				value := fixture.request(t, ctx, 15*time.Second)
				id = value.ID
				if phase != "pending" {
					answer := questionAnswer(value)
					for range 2 {
						if _, err := fixture.repository.AnswerInvocationRuntimeQuestion(ctx, answer, allowQuestion); err != nil {
							t.Fatal(err)
						}
					}
					assertInteractionOutcome(t, ctx, fixture, kind, id, "answered", 2, answer.ActorID, answer.CorrelationID, "")
				}
				if phase == "delivering" {
					if _, err := fixture.repository.BeginInvocationRuntimeQuestionDelivery(ctx, fixture.claim, fixture.effect, id, allowQuestion); err != nil {
						t.Fatal(err)
					}
				}
				for range 2 {
					if _, err := persistence.NewRepository(fixture.pool).CloseInvocationRuntimeQuestion(ctx, fixture.claim, fixture.effect, id, "runtime-ended"); err != nil {
						t.Fatal(err)
					}
				}
			}
			assertInteractionOutcome(t, ctx, fixture, kind, id, state, version, fixture.claim.ActorID, fixture.claim.CorrelationID, "runtime-ended")
		}
	}
	events, err := fixture.repository.ListEvents(ctx, fixture.claim.IsolationDomainID, fixture.claim.ResourceID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for index, event := range events {
		if event.Sequence != uint64(index+1) {
			t.Fatal("interaction changes lost invocation journal ordering")
		}
	}
}

func assertInteractionOutcome(t *testing.T, ctx context.Context, fixture *runtimeQuestionFixture, kind, id, state string, version int64, actor, correlation, reason string) {
	t.Helper()
	action := state
	if action == "delivery_unknown" {
		action = "delivery.unknown"
	}
	events, err := fixture.repository.ListEvents(ctx, fixture.claim.IsolationDomainID, fixture.claim.ResourceID, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type != "interaction."+kind+"."+action || event.Payload[kind+"Id"] != id {
			continue
		}
		count++
		if event.Source != "platform" || event.ActorID != actor || event.CorrelationID != correlation || event.IsolationDomainID != fixture.target.IsolationDomainID || event.ServiceID != fixture.target.ServiceID || event.RevisionID != fixture.target.RevisionID || event.Payload["state"] != state || event.Payload["version"] != float64(version) {
			t.Fatal("interaction outcome lost authoritative scope, actor or version")
		}
		fields := 3
		if reason != "" {
			fields = 5
			closed, ok := event.Payload["closedAt"].(string)
			if _, err := time.Parse(time.RFC3339Nano, closed); !ok || err != nil || event.Payload["closeReason"] != reason {
				t.Fatal("interaction outcome lost closure facts")
			}
		}
		if len(event.Payload) != fields {
			t.Fatal("interaction outcome exposed fields beyond its safe contract")
		}
	}
	if count != 1 {
		t.Fatalf("expected one %s %s event, got %d", kind, state, count)
	}
}

func TestInteractionExpirySkipsBusyJournalAndRollsBackFailedEvent(t *testing.T) {
	for _, kind := range []string{"approval", "question"} {
		for _, boundary := range []string{"journal owner", "event failure"} {
			t.Run(kind+"/"+boundary, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				fixture := newExpiringApprovalFixture(t, ctx)
				var id string
				var expiry time.Time
				if kind == "approval" {
					value := requestExpiringApproval(t, ctx, fixture, 300*time.Millisecond)
					id, expiry = value.ID, value.ExpiresAt
				} else {
					if err := fixture.pool.QueryRow(ctx, `SELECT clock_timestamp()+interval '300 milliseconds'`).Scan(&expiry); err != nil {
						t.Fatal(err)
					}
					fixture.sequence++
					value, err := fixture.repository.RecordInvocationRuntimeQuestionRequest(ctx, fixture.claim, fixture.effect, fixture.target, persistence.InvocationRuntimeQuestionRequest{SourceSequence: fixture.sequence, Prompts: questionPrompts(), ExpiresAt: expiry})
					if err != nil {
						t.Fatal(err)
					}
					id = value.ID
				}
				actor, correlation := "expiry-owner", identity.New("cor")
				expire := fixture.repository.ExpireInvocationRuntimeApprovals
				if kind == "question" {
					expire = fixture.repository.ExpireInvocationRuntimeQuestions
				}
				if _, err := fixture.pool.Exec(ctx, `SELECT pg_sleep(GREATEST(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.01)`, expiry); err != nil {
					t.Fatal(err)
				}
				if boundary == "journal owner" {
					transaction, err := fixture.pool.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer transaction.Rollback(ctx)
					if _, err := transaction.Exec(ctx, `SELECT id FROM invocations WHERE isolation_domain_id=$1 AND id=$2 FOR UPDATE`, fixture.claim.IsolationDomainID, fixture.claim.ResourceID); err != nil {
						t.Fatal(err)
					}
					if count, err := expire(ctx, fixture.claim.IsolationDomainID, actor, correlation, 100); err != nil || count != 0 {
						t.Fatalf("expiry waited on journal owner: %d, %v", count, err)
					}
					if err := transaction.Rollback(ctx); err != nil {
						t.Fatal(err)
					}
					if count, err := expire(ctx, fixture.claim.IsolationDomainID, actor, correlation, 100); err != nil || count != 1 {
						t.Fatalf("expiry did not resume: %d, %v", count, err)
					}
					assertInteractionOutcome(t, ctx, fixture, kind, id, "expired", 2, actor, correlation, "expired")
				} else {
					collision := identity.Derived("evt", fixture.claim.IsolationDomainID+":"+fixture.claim.ResourceID+":"+kind+":"+id+":"+strconv.Itoa(2))
					if _, err := fixture.pool.Exec(ctx, `INSERT INTO invocation_events (isolation_domain_id,invocation_id,id,sequence,schema_version,event_type,occurred_at,recorded_at,correlation_id,actor_id,service_id,revision_id,payload)
 SELECT isolation_domain_id,invocation_id,$3,99999,schema_version,'test.collision',occurred_at,recorded_at,correlation_id,actor_id,service_id,revision_id,'{}' FROM invocation_events WHERE isolation_domain_id=$1 AND invocation_id=$2 LIMIT 1`, fixture.claim.IsolationDomainID, fixture.claim.ResourceID, collision); err != nil {
						t.Fatal(err)
					}
					if _, err := expire(ctx, fixture.claim.IsolationDomainID, actor, correlation, 100); err == nil {
						t.Fatal("expiry committed without its journal event")
					}
					var snapshot []byte
					if kind == "approval" {
						value, err := fixture.repository.GetInvocationRuntimeApproval(ctx, fixture.claim.IsolationDomainID, id)
						if err != nil || value.State != "pending" || value.Version != 1 {
							t.Fatal("event failure retained approval expiry")
						}
					} else {
						value, err := fixture.repository.GetInvocationRuntimeQuestion(ctx, fixture.claim.IsolationDomainID, fixture.claim.ResourceID, id)
						if err != nil || value.State != "pending" || value.Version != 1 {
							t.Fatal("event failure retained question expiry")
						}
					}
					if err := fixture.pool.QueryRow(ctx, `SELECT jsonb_build_array((SELECT count(*) FROM audit_records WHERE isolation_domain_id=$1 AND resource_id=$2 AND action=$3),(SELECT count(*) FROM outbox_events WHERE isolation_domain_id=$1 AND aggregate_id=$2 AND event_type=$3))`, fixture.claim.IsolationDomainID, id, "invocation-"+kind+".expired").Scan(&snapshot); err != nil {
						t.Fatal(err)
					}
					var counts []int
					if json.Unmarshal(snapshot, &counts) != nil || len(counts) != 2 || counts[0] != 0 || counts[1] != 0 {
						t.Fatal("event failure retained partial expiry evidence")
					}
				}
			})
		}
	}
}
