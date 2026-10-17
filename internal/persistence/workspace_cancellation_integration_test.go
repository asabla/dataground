package persistence_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Use the source fixture after runtime preparation and policy withdrawal. Source
// retention is required even when the former runtime authority is no longer valid.
func testSourceCancellationRetention(t *testing.T, ctx context.Context, pool *pgxpool.Pool, claim persistence.OperationClaim, effect persistence.EffectRecord, ref execution.ExecutionRef) {
	t.Helper()
	repo := persistence.NewRepository(pool)
	if _, err := repo.AcceptCancellation(ctx, testIdempotency(claim.IsolationDomainID, "source-cancellation"), persistence.AcceptCancellationInput{
		InvocationID: claim.ResourceID, ActorID: "cancellation-operator", CorrelationID: identity.New("cor"),
	}); err != nil {
		t.Fatal(err)
	}
	target, err := repo.GetInvocationCancellationTarget(ctx, claim.IsolationDomainID, claim.ID)
	if err != nil || !target.WorkspaceSourceSelected || !target.AdmissionPrepared || target.InvocationID != claim.ResourceID {
		t.Fatal("source cancellation target", target, err)
	}
	if _, err := repo.GetInvocationCancellationTarget(ctx, identity.New("iso"), claim.ID); !errors.Is(err, persistence.ErrInvocationCancellationTargetMissing) {
		t.Fatal("cross-domain source cancellation", err)
	}
	if _, err := repo.RenewLease(ctx, claim, time.Minute); !errors.Is(err, persistence.ErrLeaseLost) {
		t.Fatal("cancellation retained runtime authority", err)
	}
	if _, err := repo.BeginInvocationWorkspaceQuiescence(ctx, claim, effect, ref, "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("cancelled runtime claim acquired cleanup authority")
	}
	provider := &sourceCancellationNoEffects{t: t}
	executions := executionpostgres.New(pool)
	for range 2 {
		driver, err := reconcile.NewInvocationCancellationDriver(persistence.NewRepository(pool), provider, executions, provider)
		if err != nil {
			t.Fatal(err)
		}
		cancelEffect := persistence.EffectRecord{IsolationDomainID: claim.IsolationDomainID, OperationKind: persistence.OperationKindInvocation, OperationID: claim.ID, Phase: "cancel-invocation"}
		if result, found, err := driver.Observe(ctx, cancelEffect); result != nil || found || !errors.Is(err, reconcile.ErrInvocationCancellationRetentionRequired) {
			t.Fatal("source cancellation observation", result, found, err)
		}
		if result, err := driver.Apply(ctx, cancelEffect); result != nil || !errors.Is(err, reconcile.ErrInvocationCancellationRetentionRequired) {
			t.Fatal("source cancellation apply", result, err)
		}
	}
	instance, err := executions.GetExecutionByOperation(ctx, claim.IsolationDomainID, claim.ID)
	if err != nil || instance.ID != ref.ID || instance.State != "ready" {
		t.Fatal("source execution changed", instance, err)
	}
	inv, err := repo.GetInvocation(ctx, claim.IsolationDomainID, claim.ResourceID)
	if err != nil || inv.State != "cancelling" {
		t.Fatal("source cancellation reported complete", inv.State, err)
	}

	// An accepted source selection creates no scratch to retain before admission.
	unadmitted := identity.New("inv")
	if _, err := repo.AcceptInvocation(ctx, testIdempotency(claim.IsolationDomainID, "source-cancellation-unadmitted"), persistence.AcceptInvocationInput{
		ID: unadmitted, ServiceID: target.ServiceID, Alias: "source", Input: map[string]any{"prompt": "never start"}, ActorID: "source-requester", CorrelationID: identity.New("cor"), Deadline: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AcceptCancellation(ctx, testIdempotency(claim.IsolationDomainID, "source-cancel-unadmitted"), persistence.AcceptCancellationInput{
		InvocationID: unadmitted, ActorID: "cancellation-operator", CorrelationID: identity.New("cor"),
	}); err != nil {
		t.Fatal(err)
	}
	inv, err = repo.GetInvocation(ctx, claim.IsolationDomainID, unadmitted)
	if err != nil {
		t.Fatal(err)
	}
	target, err = repo.GetInvocationCancellationTarget(ctx, claim.IsolationDomainID, inv.OperationID)
	if err != nil || !target.WorkspaceSourceSelected || target.AdmissionPrepared {
		t.Fatal("unadmitted source target", target, err)
	}
	driver, err := reconcile.NewInvocationCancellationDriver(repo, provider, executions, provider)
	if err != nil {
		t.Fatal(err)
	}
	result, found, err := driver.Observe(ctx, persistence.EffectRecord{IsolationDomainID: claim.IsolationDomainID, OperationKind: persistence.OperationKindInvocation, OperationID: inv.OperationID, Phase: "cancel-invocation"})
	if err != nil || !found || result["state"] != "absent" {
		t.Fatal("unadmitted source observation", result, found, err)
	}
}

type sourceCancellationNoEffects struct{ t *testing.T }

func (s *sourceCancellationNoEffects) AuthorizeInvocationCancellation(context.Context, persistence.InvocationCancellationTarget) error {
	s.t.Error("source cancellation requested termination authority")
	return errors.New("unexpected authorization")
}
func (s *sourceCancellationNoEffects) Observe(context.Context, execution.ExecutionRef) (execution.Observation, error) {
	s.t.Error("source cancellation reached provider observation")
	return execution.Observation{}, errors.New("unexpected observation")
}
func (s *sourceCancellationNoEffects) Terminate(context.Context, execution.ExecutionRef) error {
	s.t.Error("source cancellation reached provider termination")
	return errors.New("unexpected termination")
}
