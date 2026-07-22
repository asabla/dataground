package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/asabla/dataground/internal/identity"
)

func TestMemoryStateStoreScopesIdempotencyAndRejectsTerminalRegression(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStateStore()
	domainID := identity.New("iso")
	gatewayID := identity.New("gtw")
	_, err := store.RegisterGateway(ctx, GatewayRegistration{
		IsolationDomainID: domainID,
		ID:                gatewayID,
		Endpoint:          "http://127.0.0.1:8080",
		Driver:            "docker",
		Capabilities:      []string{"runtime.codex", "artifact.export"},
	})
	if err != nil {
		t.Fatal(err)
	}
	operationID := identity.New("op")
	placement, err := store.ReservePlacement(ctx, PlacementRequest{
		IsolationDomainID: domainID, OperationID: operationID,
		RequiredCapabilities: []string{"runtime.codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ReservePlacement(ctx, PlacementRequest{
		IsolationDomainID: domainID, OperationID: operationID,
		RequiredCapabilities: []string{"artifact.export"},
	})
	if !errors.Is(err, ErrStateConflict) {
		t.Fatalf("changed idempotent placement = %v, want ErrStateConflict", err)
	}
	executionID := identity.Derived("exe", domainID+":"+operationID)
	ref := ExecutionRef{IsolationDomainID: domainID, ID: executionID}
	if err := store.SaveExecution(ctx, ExecutionRecord{
		Execution: Execution{
			IsolationDomainID: domainID, ID: executionID, GatewayID: gatewayID, State: "running",
		},
		PlacementID: placement.ID, OperationID: operationID, SandboxName: "sandbox-a",
	}); err != nil {
		t.Fatal(err)
	}
	byOperation, err := store.GetExecutionByOperation(ctx, domainID, operationID)
	if err != nil || byOperation.ID != executionID || byOperation.IsolationDomainID != domainID {
		t.Fatalf("execution by operation = %#v, %v", byOperation, err)
	}
	if _, err := store.GetExecutionByOperation(ctx, identity.New("iso"), operationID); !errors.Is(err, ErrExecutionMissing) {
		t.Fatalf("cross-domain operation lookup = %v, want ErrExecutionMissing", err)
	}
	if err := store.UpdateExecutionState(ctx, ref, "terminated"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateExecutionState(ctx, ref, "running"); !errors.Is(err, ErrStateConflict) {
		t.Fatalf("terminal regression = %v, want ErrStateConflict", err)
	}
	if _, err := store.GetExecution(ctx, ExecutionRef{IsolationDomainID: identity.New("iso"), ID: executionID}); !errors.Is(err, ErrExecutionMissing) {
		t.Fatalf("cross-domain lookup = %v, want ErrExecutionMissing", err)
	}
}

func TestMemoryStateStoreMarksGatewayLoss(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStateStore()
	domainID := identity.New("iso")
	gatewayID := identity.New("gtw")
	_, err := store.RegisterGateway(ctx, GatewayRegistration{
		IsolationDomainID: domainID, ID: gatewayID, Endpoint: "http://127.0.0.1:8080", Driver: "docker",
	})
	if err != nil {
		t.Fatal(err)
	}
	operationID := identity.New("op")
	placement, err := store.ReservePlacement(ctx, PlacementRequest{
		IsolationDomainID: domainID, OperationID: operationID,
	})
	if err != nil {
		t.Fatal(err)
	}
	executionID := identity.Derived("exe", domainID+":"+operationID)
	ref := ExecutionRef{IsolationDomainID: domainID, ID: executionID}
	if err := store.SaveExecution(ctx, ExecutionRecord{
		Execution: Execution{
			IsolationDomainID: domainID, ID: executionID, GatewayID: gatewayID, State: "running",
		},
		PlacementID: placement.ID, OperationID: operationID, SandboxName: "sandbox-a",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGatewayState(ctx, domainID, gatewayID, GatewayLost); err != nil {
		t.Fatal(err)
	}
	record, err := store.GetExecution(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if record.Execution.State != "unknown" {
		t.Fatalf("lost execution state = %q, want unknown", record.Execution.State)
	}
	if _, err := store.ReservePlacement(ctx, PlacementRequest{
		IsolationDomainID: domainID, OperationID: identity.New("op"),
	}); !errors.Is(err, ErrNoGateway) {
		t.Fatalf("placement on lost gateway = %v, want ErrNoGateway", err)
	}
}
