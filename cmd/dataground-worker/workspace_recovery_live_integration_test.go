package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/domain"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
)

// The public API's real operation deadline expires while the worker is parked.
// No test clock or direct mutation creates the failure or its recovery authority.
func recoverExpiredLiveWorkspace(t *testing.T, ctx context.Context, repo *persistence.Repository, worker *reconcile.Reconciler, ready func(context.Context) error, public *liveWorkspaceAPI, invocation domain.Invocation) {
	t.Helper()
	binary := os.Getenv("DATAGROUND_TEST_REPAIR_BINARY")
	if binary == "" {
		t.Fatal("live recovery requires the built operator command")
	}
	scope := invocation.Metadata.IsolationDomainID
	stopped, err := repo.GetInvocationWorkspaceOwnerStop(ctx, scope, invocation.OperationID)
	if err != nil {
		t.Fatal("original owner did not record shutdown", err)
	}
	operation, err := repo.GetOperation(ctx, scope, invocation.OperationID)
	if err != nil || operation.ObservedState != "cancelling" || operation.DeadlineAt == nil || !operation.DeadlineAt.After(time.Now()) {
		t.Fatal("cancellation did not retain its original live deadline", err)
	}
	t.Log("Original owner stopped; waiting for the public operation deadline without retention effects")
	timer := time.NewTimer(time.Until(*operation.DeadlineAt) + 100*time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal("original deadline wait", ctx.Err())
	}
	if err := ready(ctx); err != nil {
		t.Fatal("recovery fixture readiness", err)
	}
	if ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil || !ran {
		t.Fatal("expired cancellation reconciliation", err)
	}
	failed := public.invocation(invocation.Metadata.ID)
	if failed.State != "failed" || failed.Error == nil || failed.Error.Code != "OPERATION_DEADLINE_EXCEEDED" {
		t.Fatal("public cancellation did not fail at its original deadline")
	}
	operation, err = repo.GetOperation(ctx, scope, invocation.OperationID)
	if err != nil || operation.ObservedState != "failed" {
		t.Fatal("read failed operation", err)
	}
	reason := sha256.Sum256([]byte("live expired owned cancellation retention fixture"))
	args := []string{"retain-workspace", "--isolation-domain", scope, "--operation", invocation.OperationID, "--expected-generation", strconv.Itoa(operation.Metadata.Generation), "--owner-binding", stopped.OwnerBinding, "--actor", "live-source-conformance", "--correlation-id", identity.New("cor"), "--reason-sha256", fmt.Sprintf("sha256:%x", reason), "--deadline-at", time.Now().UTC().Truncate(time.Microsecond).Add(5 * time.Minute).Format(time.RFC3339Nano)}
	for range 2 {
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = []string{"PATH=/usr/bin:/bin", "DATAGROUND_DATABASE_URL=" + os.Getenv("DATAGROUND_TEST_DATABASE_URL")}
		// The command has a fixed safe receipt; withhold diagnostics on failure.
		if err := command.Run(); err != nil {
			t.Fatal("operator recovery or exact replay failed")
		}
	}
	current := public.invocation(invocation.Metadata.ID)
	if current.State != "cancelling" || current.Error != nil || current.CompletedAt != nil {
		t.Fatal("operator command did not reopen the original cancellation")
	}
	t.Log("Expired cancellation recovered through the operator command and exact replay")
}
