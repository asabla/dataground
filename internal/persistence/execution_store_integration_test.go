package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
)

type executionRunner struct {
	mu      sync.Mutex
	results []openshell.CommandResult
	calls   [][]string
}

func (runner *executionRunner) Run(_ context.Context, binary string, args ...string) (openshell.CommandResult, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.calls = append(runner.calls, append([]string{binary}, args...))
	if len(runner.results) == 0 {
		return openshell.CommandResult{}, errors.New("unexpected command")
	}
	result := runner.results[0]
	runner.results = runner.results[1:]
	return result, nil
}

func (runner *executionRunner) Start(_ context.Context, binary string, args ...string) (execution.RuntimeSession, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	runner.calls = append(runner.calls, append([]string{binary}, args...))
	return executionSession{}, nil
}

type executionSession struct{}

func (executionSession) Input() io.WriteCloser { return integrationWriteCloser{Writer: io.Discard} }
func (executionSession) Output() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (executionSession) Errors() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (executionSession) Wait() error           { return nil }
func (executionSession) Close() error          { return nil }

type integrationWriteCloser struct{ io.Writer }

func (integrationWriteCloser) Close() error { return nil }

func TestDurableExecutionPlacementAndProviderRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databaseURL := testDatabaseURL(t)
	database, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.MigrateDownTo(ctx, database, 0); err != nil {
		database.Close()
		t.Fatalf("reset schema: %v", err)
	}
	if err := persistence.MigrateUp(ctx, database); err != nil {
		database.Close()
		t.Fatalf("migrate schema: %v", err)
	}
	database.Close()
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	store := executionpostgres.New(pool)
	domainID := identity.New("iso")
	gatewayIDs := []string{identity.New("gtw"), identity.New("gtw")}
	sort.Strings(gatewayIDs)
	for index, gatewayID := range gatewayIDs {
		registration := execution.GatewayRegistration{
			IsolationDomainID: domainID, ID: gatewayID,
			Endpoint: "https://gateway-" + string(rune('a'+index)) + ".example.invalid",
			Driver:   "docker", Capabilities: []string{"codex.app-server", "artifact.export", "codex.app-server"},
		}
		first, err := store.RegisterGateway(ctx, registration)
		if err != nil {
			t.Fatalf("register gateway: %v", err)
		}
		replayed, err := store.RegisterGateway(ctx, registration)
		if err != nil || replayed.ID != first.ID {
			t.Fatalf("replay gateway registration: %#v, %v", replayed, err)
		}
	}
	firstOperation := identity.New("op")
	firstPlacement, err := store.ReservePlacement(ctx, execution.PlacementRequest{
		IsolationDomainID: domainID, OperationID: firstOperation,
		RequiredCapabilities: []string{"codex.app-server"},
	})
	if err != nil {
		t.Fatalf("reserve first placement: %v", err)
	}
	if firstPlacement.GatewayID != gatewayIDs[0] {
		t.Fatalf("deterministic gateway tie-break = %q, want %q", firstPlacement.GatewayID, gatewayIDs[0])
	}
	if err := store.SetGatewayState(ctx, domainID, gatewayIDs[0], execution.GatewayDraining); err != nil {
		t.Fatalf("drain gateway: %v", err)
	}
	replayedPlacement, err := store.ReservePlacement(ctx, execution.PlacementRequest{
		IsolationDomainID: domainID, OperationID: firstOperation,
		RequiredCapabilities: []string{"codex.app-server"},
	})
	if err != nil || replayedPlacement != firstPlacement {
		t.Fatalf("placement replay after drain: %#v, %v", replayedPlacement, err)
	}
	if _, err := store.ReservePlacement(ctx, execution.PlacementRequest{
		IsolationDomainID: domainID, OperationID: firstOperation,
		RequiredCapabilities: []string{"artifact.export"},
	}); !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("changed idempotent placement = %v, want ErrStateConflict", err)
	}
	secondPlacement, err := store.ReservePlacement(ctx, execution.PlacementRequest{
		IsolationDomainID: domainID, OperationID: identity.New("op"),
		RequiredCapabilities: []string{"codex.app-server"},
	})
	if err != nil || secondPlacement.GatewayID != gatewayIDs[1] {
		t.Fatalf("new placement used drained gateway: %#v, %v", secondPlacement, err)
	}
	concurrentOperation := identity.New("op")
	concurrentPlacement, err := store.ReservePlacement(ctx, execution.PlacementRequest{
		IsolationDomainID: domainID, OperationID: concurrentOperation,
		RequiredCapabilities: []string{"codex.app-server"},
	})
	if err != nil {
		t.Fatalf("reserve concurrent execution placement: %v", err)
	}
	concurrentRecord := execution.ExecutionRecord{
		Execution: execution.Execution{
			IsolationDomainID: domainID,
			ID:                identity.Derived("exe", domainID+":"+concurrentOperation),
			GatewayID:         concurrentPlacement.GatewayID,
			State:             "provisioning",
		},
		PlacementID: concurrentPlacement.ID,
		OperationID: concurrentOperation,
		SandboxName: "dg-concurrent-replay",
	}
	start := make(chan struct{})
	errorsByWorker := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errorsByWorker <- store.SaveExecution(ctx, concurrentRecord)
		}()
	}
	close(start)
	workers.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatalf("concurrent execution replay: %v", err)
		}
	}
	if err := store.SetGatewayState(ctx, domainID, gatewayIDs[1], execution.GatewayLost); err != nil {
		t.Fatalf("mark gateway lost: %v", err)
	}
	lostRecord, err := store.GetExecution(ctx, execution.ExecutionRef{
		IsolationDomainID: domainID, ID: concurrentRecord.Execution.ID,
	})
	if err != nil || lostRecord.Execution.State != "unknown" {
		t.Fatalf("lost execution state: %#v, %v", lostRecord.Execution, err)
	}
	var lostPlacementState string
	if err := pool.QueryRow(ctx, `
		SELECT state FROM execution_placements
		WHERE isolation_domain_id = $1 AND id = $2
	`, domainID, concurrentPlacement.ID).Scan(&lostPlacementState); err != nil {
		t.Fatal(err)
	}
	if lostPlacementState != "lost" {
		t.Fatalf("lost placement state = %q, want lost", lostPlacementState)
	}
	if _, err := store.ReservePlacement(ctx, execution.PlacementRequest{
		IsolationDomainID: domainID, OperationID: identity.New("op"),
	}); !errors.Is(err, execution.ErrNoGateway) {
		t.Fatalf("placement without active gateway = %v, want ErrNoGateway", err)
	}

	policy := []byte("version: 1\n")
	policyPath := filepath.Join(t.TempDir(), "deny-all.yaml")
	if err := os.WriteFile(policyPath, policy, 0o600); err != nil {
		t.Fatal(err)
	}
	policyDigest := sha256.Sum256(policy)
	runner := &executionRunner{results: []openshell.CommandResult{
		{Stdout: []byte("[]")}, {},
	}}
	provider := openshell.New(openshell.Config{ExpectedVersion: "0.0.86", StateStore: store}, runner)
	created, err := provider.Create(ctx, execution.CreateRequest{
		Placement: firstPlacement, IsolationDomainID: domainID, OperationID: firstOperation,
		Image:      "ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:" + strings.Repeat("a", 64),
		PolicyPath: policyPath, PolicySHA256: hex.EncodeToString(policyDigest[:]),
	})
	if err != nil {
		t.Fatalf("create durable execution: %v", err)
	}
	ref := execution.ExecutionRef{IsolationDomainID: domainID, ID: created.ID}
	restartedRunner := &executionRunner{}
	restartedProvider := openshell.New(openshell.Config{ExpectedVersion: "0.0.86", StateStore: executionpostgres.New(pool)}, restartedRunner)
	session, err := restartedProvider.StartRuntime(ctx, ref)
	if err != nil || session == nil {
		t.Fatalf("restore runtime routing after provider restart: %v", err)
	}
	if len(restartedRunner.calls) != 1 || !containsIntegrationSequence(restartedRunner.calls[0], "codex", "app-server") {
		t.Fatalf("restart did not recover native runtime route: %#v", restartedRunner.calls)
	}
	restartedRunner.results = []openshell.CommandResult{{}}
	if err := restartedProvider.Terminate(ctx, ref); err != nil {
		t.Fatalf("terminate durable execution: %v", err)
	}
	thirdRunner := &executionRunner{}
	thirdProvider := openshell.New(openshell.Config{ExpectedVersion: "0.0.86", StateStore: executionpostgres.New(pool)}, thirdRunner)
	if err := thirdProvider.Terminate(ctx, ref); err != nil {
		t.Fatalf("repeat termination after restart: %v", err)
	}
	if len(thirdRunner.calls) != 0 {
		t.Fatalf("terminated sandbox was contacted again: %#v", thirdRunner.calls)
	}
	if _, err := store.GetExecution(ctx, execution.ExecutionRef{IsolationDomainID: identity.New("iso"), ID: created.ID}); !errors.Is(err, execution.ErrExecutionMissing) {
		t.Fatalf("cross-domain execution lookup = %v, want ErrExecutionMissing", err)
	}
	var placementState string
	if err := pool.QueryRow(ctx, `
		SELECT state FROM execution_placements
		WHERE isolation_domain_id = $1 AND id = $2
	`, domainID, firstPlacement.ID).Scan(&placementState); err != nil {
		t.Fatal(err)
	}
	if placementState != "released" {
		t.Fatalf("terminated placement state = %q, want released", placementState)
	}
	if err := store.UpdateExecutionState(ctx, ref, "running"); !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("terminal regression = %v, want ErrStateConflict", err)
	}
}

func containsIntegrationSequence(items []string, sequence ...string) bool {
	for index := 0; index+len(sequence) <= len(items); index++ {
		match := true
		for offset := range sequence {
			if items[index+offset] != sequence[offset] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
