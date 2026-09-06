package persistence_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

func TestInvocationApprovalDecisionIntegrityAcrossDelivery(t *testing.T) {
	for _, test := range []struct{ decision, effective string }{{"approve", "approve"}, {"approve", "deny"}, {"deny", "deny"}, {"deny", "approve"}} {
		t.Run(test.decision+" to "+test.effective, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			fixture := newRuntimeQuestionFixture(t, ctx)
			t.Cleanup(func() {
				cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if _, err := fixture.pool.Exec(cleanupCtx, `TRUNCATE invocation_runtime_approvals`); err != nil {
					t.Error(err)
				}
			})
			approval, err := fixture.repository.RecordInvocationRuntimeApprovalRequest(ctx, fixture.claim, fixture.effect, fixture.target, persistence.InvocationRuntimeApprovalRequest{SourceSequence: 73, RequestedAction: "process.execute"})
			if err != nil {
				t.Fatal(err)
			}
			resolution := persistence.InvocationRuntimeApprovalResolution{IsolationDomainID: approval.IsolationDomainID, InvocationID: approval.InvocationID, ApprovalID: approval.ID, ExpectedVersion: 1, Decision: test.decision, ActorID: "controller", CorrelationID: identity.New("cor")}
			if _, err := fixture.repository.ResolveInvocationRuntimeApprovalCommand(ctx, testIdempotency(approval.IsolationDomainID, "approval-integrity-resolution"), resolution, func(context.Context, persistence.InvocationRuntimeApproval) error { return nil }); err != nil {
				t.Fatal(err)
			}
			resolved, err := fixture.repository.GetInvocationRuntimeApproval(ctx, approval.IsolationDomainID, approval.ID)
			if err != nil {
				t.Fatal(err)
			}
			changedDecision := "approve"
			if test.decision == "approve" {
				changedDecision = "deny"
			}
			mutations := []string{
				"decision='" + changedDecision + "'",
				"resolved_by='substituted-controller'",
				"resolution_correlation_id='cor_00000000000000000001'",
				"resolved_at=resolved_at+interval '1 second'",
			}
			for _, mutation := range mutations {
				if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_runtime_approvals SET state='delivering',version=3,effective_decision='deny',updated_at=clock_timestamp(),`+mutation+` WHERE isolation_domain_id=$1 AND id=$2`, approval.IsolationDomainID, approval.ID); err == nil {
					t.Fatalf("reservation rewrote immutable resolution: %s", mutation)
				}
			}
			if test.decision == "deny" {
				if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_runtime_approvals SET state='delivering',version=3,effective_decision='approve',updated_at=clock_timestamp() WHERE isolation_domain_id=$1 AND id=$2`, approval.IsolationDomainID, approval.ID); err == nil {
					t.Fatal("database escalated a recorded denial")
				}
			}
			delivering, err := fixture.repository.BeginInvocationRuntimeApprovalDelivery(ctx, fixture.claim, fixture.effect, approval.ID, test.effective)
			if test.decision == "deny" && test.effective == "approve" {
				if !errors.Is(err, persistence.ErrInvocationRuntimeApprovalConflict) {
					t.Fatalf("denial escalated at repository boundary: %v", err)
				}
			} else {
				if err != nil || delivering.State != "delivering" || delivering.Version != 3 {
					t.Fatalf("reserve approval: %s, %v", delivering.State, err)
				}
				otherEffective := "approve"
				if test.effective == "approve" {
					otherEffective = "deny"
				}
				for _, mutation := range append(mutations, "effective_decision='"+otherEffective+"'") {
					if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_runtime_approvals SET state='delivered',version=4,delivered_at=clock_timestamp(),updated_at=clock_timestamp(),`+mutation+` WHERE isolation_domain_id=$1 AND id=$2`, approval.IsolationDomainID, approval.ID); err == nil {
						t.Fatalf("completion rewrote reserved decision: %s", mutation)
					}
				}
				if _, err := fixture.repository.CompleteInvocationRuntimeApprovalDelivery(ctx, fixture.claim, fixture.effect, approval.ID); err != nil {
					t.Fatal(err)
				}
			}
			actual, err := persistence.NewRepository(fixture.pool).GetInvocationRuntimeApproval(ctx, approval.IsolationDomainID, approval.ID)
			if err != nil || actual.Decision != resolved.Decision || actual.ResolvedBy != resolved.ResolvedBy || actual.ResolutionCorrelationID != resolved.ResolutionCorrelationID || !actual.ResolvedAt.Equal(resolved.ResolvedAt) {
				t.Fatalf("resolution evidence changed: %v", err)
			}
			if test.decision == "deny" && test.effective == "approve" {
				if actual.State != "resolved" || actual.Version != 2 || actual.EffectiveDecision != "" {
					t.Fatal("rejected escalation changed durable state")
				}
			} else if actual.State != "delivered" || actual.Version != 4 || actual.EffectiveDecision != test.effective {
				t.Fatal("delivery changed the effective decision")
			}
			database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if err := persistence.MigrateDownTo(ctx, database, 52); err == nil {
				t.Fatal("downgrade removed retained decision protections")
			}
			if err := persistence.RequireCurrentSchema(ctx, database); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.pool.Exec(ctx, `TRUNCATE invocation_runtime_approvals`); err != nil {
				t.Fatal(err)
			}
			if err := persistence.MigrateDownTo(ctx, database, 52); err != nil {
				t.Fatal(err)
			}
			if err := persistence.MigrateUp(ctx, database); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInvocationApprovalIntegrityUpgradeRejectsExistingEscalation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := newRuntimeQuestionFixture(t, ctx)
	database, err := persistence.OpenSQL(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := fixture.pool.Exec(cleanupCtx, `TRUNCATE invocation_runtime_approvals`); err != nil {
			t.Error(err)
		}
		if err := persistence.MigrateUp(cleanupCtx, database); err != nil {
			t.Error(err)
		}
	}()
	if err := persistence.MigrateDownTo(ctx, database, 52); err != nil {
		t.Fatal(err)
	}
	approvalID := identity.New("apr")
	if _, err := fixture.pool.Exec(ctx, `INSERT INTO invocation_runtime_approvals (contract,isolation_domain_id,id,operation_id,invocation_id,service_id,revision_id,effect_id,source_sequence,requested_action,state,version,decision,resolved_by,resolution_correlation_id,resolved_at,created_at,updated_at)
 VALUES ('dataground.invocation-runtime-approval/v1',$1,$2,$3,$4,$5,$6,$7,74,'workspace.change','resolved',2,'deny','controller',$8,clock_timestamp(),clock_timestamp(),clock_timestamp())`, fixture.target.IsolationDomainID, approvalID, fixture.target.OperationID, fixture.target.InvocationID, fixture.target.ServiceID, fixture.target.RevisionID, fixture.effect.EffectID, identity.New("cor")); err != nil {
		t.Fatal(err)
	}
	// Schema 52 admitted this contradictory retained state. The upgrade must
	// reject it without rewriting either the controller decision or its evidence.
	if _, err := fixture.pool.Exec(ctx, `UPDATE invocation_runtime_approvals SET state='delivering',version=3,effective_decision='approve',updated_at=clock_timestamp() WHERE isolation_domain_id=$1 AND id=$2`, fixture.target.IsolationDomainID, approvalID); err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateUp(ctx, database); err == nil {
		t.Fatal("upgrade accepted an escalated denial")
	}
	var decision, effective string
	var version int64
	if err := fixture.pool.QueryRow(ctx, `SELECT decision,effective_decision,version FROM invocation_runtime_approvals WHERE isolation_domain_id=$1 AND id=$2`, fixture.target.IsolationDomainID, approvalID).Scan(&decision, &effective, &version); err != nil || decision != "deny" || effective != "approve" || version != 3 {
		t.Fatalf("failed migration rewrote retained evidence: %v", err)
	}
	if err := persistence.RequireCurrentSchema(ctx, database); err == nil {
		t.Fatal("failed upgrade advertised the new protection")
	}
}
