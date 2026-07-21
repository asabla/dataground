package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
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
	policyDigest := sha256.Sum256(policy)
	runner := &executionRunner{results: []openshell.CommandResult{
		{Stdout: []byte("[]")}, {},
	}}
	workspace, err := openshell.OpenPolicyWorkspace(filepath.Join(t.TempDir(), "policies"))
	if err != nil {
		t.Fatalf("open policy workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := workspace.Close(); err != nil {
			t.Errorf("close policy workspace: %v", err)
		}
	})
	provider := openshell.New(openshell.Config{
		ExpectedVersion: "0.0.86", StateStore: store, PolicyWorkspace: workspace,
	}, runner)
	created, err := provider.Create(ctx, execution.CreateRequest{
		Placement: firstPlacement, IsolationDomainID: domainID, OperationID: firstOperation,
		Image:        "ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:" + strings.Repeat("a", 64),
		Policy:       policy,
		PolicyDigest: "sha256:" + hex.EncodeToString(policyDigest[:]),
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

func TestDurableExecutionPlanBindingIsImmutableAuditedAndScoped(t *testing.T) {
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

	domainID := identity.New("iso")
	serviceID := identity.New("svc")
	revisionID := identity.New("rev")
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_services (
			isolation_domain_id, id, name, description, created_at, updated_at, created_by
		) VALUES ($1, $2, 'execution plan fixture', '', $3, $3, 'test:author')
	`, domainID, serviceID, now); err != nil {
		t.Fatalf("insert service fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_revisions (
			isolation_domain_id, id, service_id, revision_number, state,
			runtime_profile, required_capabilities, created_at, updated_at, created_by
		) VALUES ($1, $2, $3, 1, 'draft', 'codex.app-server/v1',
		          ARRAY['runtime.codex', 'artifact.export'], $4, $4, 'test:author')
	`, domainID, revisionID, serviceID, now); err != nil {
		t.Fatalf("insert revision fixture: %v", err)
	}

	store := executionpostgres.New(pool)
	missingRevisionPlan := durableExecutionPlan(domainID, identity.New("rev"))
	if _, err := store.BindExecutionPlan(ctx, execution.ExecutionPlanBinding{
		Plan: missingRevisionPlan, ActorID: "worker:resolver", CorrelationID: "correlation-missing",
	}); !errors.Is(err, execution.ErrExecutionPlanRevisionMissing) {
		t.Fatalf("bind missing revision plan = %v, want ErrExecutionPlanRevisionMissing", err)
	}
	mismatchedRevisionPlan := durableExecutionPlan(domainID, revisionID)
	mismatchedRevisionPlan.RuntimeProfile = "claude.agent-sdk/v1"
	if _, err := store.BindExecutionPlan(ctx, execution.ExecutionPlanBinding{
		Plan: mismatchedRevisionPlan, ActorID: "worker:resolver", CorrelationID: "correlation-mismatch",
	}); !errors.Is(err, execution.ErrExecutionPlanRevisionMismatch) {
		t.Fatalf("bind mismatched revision plan = %v, want ErrExecutionPlanRevisionMismatch", err)
	}

	plan := durableExecutionPlan(domainID, revisionID)
	plan.ProviderProfiles = []string{"codex", "anthropic", "codex"}
	plan.RequiredCapabilities = []string{"runtime.codex", "artifact.export", "runtime.codex"}
	binding := execution.ExecutionPlanBinding{
		Plan: plan, ActorID: "worker:resolver", CorrelationID: "correlation-plan-1",
	}
	bound, err := store.BindExecutionPlan(ctx, binding)
	if err != nil {
		t.Fatalf("bind execution plan: %v", err)
	}
	if got, want := bound.ProviderProfiles, []string{"anthropic", "codex"}; !slicesEqualIntegration(got, want) {
		t.Fatalf("bound provider profiles = %#v, want %#v", got, want)
	}

	replayed, err := store.BindExecutionPlan(ctx, execution.ExecutionPlanBinding{
		Plan: plan, ActorID: "worker:retry", CorrelationID: "correlation-plan-retry",
	})
	if err != nil || !execution.EqualExecutionPlans(bound, replayed) {
		t.Fatalf("replay execution plan: %#v, %v", replayed, err)
	}
	restartedStore := executionpostgres.New(pool)
	restored, err := restartedStore.GetExecutionPlan(ctx, domainID, revisionID)
	if err != nil || !execution.EqualExecutionPlans(bound, restored) {
		t.Fatalf("restore execution plan: %#v, %v", restored, err)
	}

	changed := plan
	changed.ImageReference = "registry.invalid/dataground/runtime@sha256:" + strings.Repeat("0", 64)
	if _, err := store.BindExecutionPlan(ctx, execution.ExecutionPlanBinding{
		Plan: changed, ActorID: binding.ActorID, CorrelationID: binding.CorrelationID,
	}); !errors.Is(err, execution.ErrExecutionPlanConflict) {
		t.Fatalf("replace execution plan = %v, want ErrExecutionPlanConflict", err)
	}
	if _, err := store.GetExecutionPlan(ctx, identity.New("iso"), revisionID); !errors.Is(err, execution.ErrExecutionPlanMissing) {
		t.Fatalf("cross-domain plan lookup = %v, want ErrExecutionPlanMissing", err)
	}

	var auditCount int
	var auditActor, auditCorrelation string
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(actor_id), min(correlation_id)
		FROM audit_records
		WHERE isolation_domain_id = $1 AND resource_type = 'service-revision'
		  AND resource_id = $2 AND action = 'execution-plan.bind'
	`, domainID, revisionID).Scan(&auditCount, &auditActor, &auditCorrelation); err != nil {
		t.Fatalf("read execution plan audit: %v", err)
	}
	if auditCount != 1 || auditActor != binding.ActorID || auditCorrelation != binding.CorrelationID {
		t.Fatalf("execution plan audit = count %d, actor %q, correlation %q", auditCount, auditActor, auditCorrelation)
	}

	if _, err := pool.Exec(ctx, `
		DELETE FROM service_revisions WHERE isolation_domain_id = $1 AND id = $2
	`, domainID, revisionID); err != nil {
		t.Fatalf("delete revision: %v", err)
	}
	if _, err := store.GetExecutionPlan(ctx, domainID, revisionID); !errors.Is(err, execution.ErrExecutionPlanMissing) {
		t.Fatalf("plan after revision deletion = %v, want ErrExecutionPlanMissing", err)
	}
}

func durableExecutionPlan(domainID, revisionID string) execution.ExecutionPlan {
	return execution.ExecutionPlan{
		SchemaVersion:             execution.ExecutionPlanSchemaV1,
		IsolationDomainID:         domainID,
		RevisionID:                revisionID,
		RuntimeProfile:            "codex.app-server/v1",
		EnvironmentRevisionID:     "environment-v1",
		ImageReference:            "registry.invalid/dataground/runtime@sha256:" + strings.Repeat("a", 64),
		EnvironmentManifestDigest: "sha256:" + strings.Repeat("b", 64),
		EnforcementBundleID:       "enforcement-bundle-v1",
		EnforcementBundleDigest:   "sha256:" + strings.Repeat("c", 64),
		RuntimeMatrixID:           "runtime-matrix-v1",
		RuntimeMatrixDigest:       "sha256:" + strings.Repeat("d", 64),
		ProviderProfiles:          []string{"codex"},
		RequiredCapabilities:      []string{"runtime.codex", "artifact.export"},
	}
}

func TestDurableEnforcementBundleCatalogIsImmutableAuditedAndScoped(t *testing.T) {
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

	domainID := identity.New("iso")
	serviceID := identity.New("svc")
	revisionID := identity.New("rev")
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_services (
			isolation_domain_id, id, name, description, created_at, updated_at, created_by
		) VALUES ($1, $2, 'enforcement bundle fixture', '', $3, $3, 'test:author')
	`, domainID, serviceID, now); err != nil {
		t.Fatalf("insert service fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO service_revisions (
			isolation_domain_id, id, service_id, revision_number, state,
			runtime_profile, required_capabilities, created_at, updated_at, created_by
		) VALUES ($1, $2, $3, 1, 'draft', 'codex.app-server/v1',
		          ARRAY['runtime.codex'], $4, $4, 'test:author')
	`, domainID, revisionID, serviceID, now); err != nil {
		t.Fatalf("insert revision fixture: %v", err)
	}

	store := executionpostgres.New(pool)
	missingRevision := durableEnforcementBundle(domainID, identity.New("rev"), []byte("version: 1\n"))
	if _, err := store.BindEnforcementBundle(ctx, execution.EnforcementBundleBinding{
		Record: missingRevision, ActorID: "worker:rosetta", CorrelationID: "correlation-missing",
	}); !errors.Is(err, execution.ErrEnforcementBundleRevisionMissing) {
		t.Fatalf("bind bundle to missing revision = %v, want ErrEnforcementBundleRevisionMissing", err)
	}

	record := durableEnforcementBundle(domainID, revisionID, []byte("version: 1\n"))
	binding := execution.EnforcementBundleBinding{
		Record: record, ActorID: "worker:rosetta", CorrelationID: "correlation-bundle-1",
	}
	bound, err := store.BindEnforcementBundle(ctx, binding)
	if err != nil {
		t.Fatalf("bind enforcement bundle: %v", err)
	}
	if bound.ObjectKey != execution.EnforcementBundleObjectKey(record) {
		t.Fatalf("derived object key = %q", bound.ObjectKey)
	}
	replayed, err := store.BindEnforcementBundle(ctx, execution.EnforcementBundleBinding{
		Record: record, ActorID: "worker:retry", CorrelationID: "correlation-bundle-retry",
	})
	if err != nil || !execution.EqualEnforcementBundleRecords(bound, replayed) {
		t.Fatalf("replay enforcement bundle: %#v, %v", replayed, err)
	}
	restored, err := executionpostgres.New(pool).GetEnforcementBundleRecord(ctx, domainID, record.ID)
	if err != nil || !execution.EqualEnforcementBundleRecords(bound, restored) {
		t.Fatalf("restore enforcement bundle: %#v, %v", restored, err)
	}
	changed := record
	changed.Provenance.BindingDigest = "sha256:" + strings.Repeat("0", 64)
	if _, err := store.BindEnforcementBundle(ctx, execution.EnforcementBundleBinding{
		Record: changed, ActorID: binding.ActorID, CorrelationID: binding.CorrelationID,
	}); !errors.Is(err, execution.ErrEnforcementBundleConflict) {
		t.Fatalf("replace enforcement bundle = %v, want ErrEnforcementBundleConflict", err)
	}
	if _, err := store.GetEnforcementBundleRecord(ctx, identity.New("iso"), record.ID); !errors.Is(err, execution.ErrEnforcementBundleMissing) {
		t.Fatalf("cross-domain bundle lookup = %v, want ErrEnforcementBundleMissing", err)
	}

	var auditCount int
	var auditActor, auditCorrelation, auditMetadata string
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(actor_id), min(correlation_id), min(safe_metadata::text)
		FROM audit_records
		WHERE isolation_domain_id = $1 AND resource_type = 'enforcement-bundle'
		  AND resource_id = $2 AND action = 'enforcement-bundle.bind'
	`, domainID, record.ID).Scan(&auditCount, &auditActor, &auditCorrelation, &auditMetadata); err != nil {
		t.Fatalf("read enforcement bundle audit: %v", err)
	}
	if auditCount != 1 || auditActor != binding.ActorID || auditCorrelation != binding.CorrelationID {
		t.Fatalf("enforcement bundle audit = count %d, actor %q, correlation %q", auditCount, auditActor, auditCorrelation)
	}
	if strings.Contains(auditMetadata, bound.ObjectKey) {
		t.Fatalf("audit metadata exposes internal object key: %s", auditMetadata)
	}

	if _, err := pool.Exec(ctx, `
		DELETE FROM service_revisions WHERE isolation_domain_id = $1 AND id = $2
	`, domainID, revisionID); err != nil {
		t.Fatalf("delete revision: %v", err)
	}
	if _, err := store.GetEnforcementBundleRecord(ctx, domainID, record.ID); !errors.Is(err, execution.ErrEnforcementBundleMissing) {
		t.Fatalf("bundle after revision deletion = %v, want ErrEnforcementBundleMissing", err)
	}
}

func durableEnforcementBundle(domainID, revisionID string, policy []byte) execution.EnforcementBundleRecord {
	digest := sha256.Sum256(policy)
	return execution.EnforcementBundleRecord{
		SchemaVersion:     execution.EnforcementBundleSchemaV1,
		IsolationDomainID: domainID,
		ID:                "rosetta-" + hex.EncodeToString(digest[:]),
		RevisionID:        revisionID,
		Digest:            "sha256:" + hex.EncodeToString(digest[:]),
		MediaType:         execution.EnforcementBundleMediaType,
		SizeBytes:         int64(len(policy)),
		Provenance: execution.EnforcementBundleProvenance{
			Producer:              "rosetta",
			SourceRevision:        strings.Repeat("a", 40),
			CompilerVersion:       "1.0.0",
			CatalogVersion:        "rosetta/v1",
			TargetContractVersion: "rosetta/openshell-policy-v1",
			Mode:                  "strict",
			InputDigest:           "sha256:" + strings.Repeat("b", 64),
			BindingDigest:         "sha256:" + strings.Repeat("c", 64),
		},
	}
}

func slicesEqualIntegration(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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
