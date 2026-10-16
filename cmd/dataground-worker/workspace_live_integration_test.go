package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/openshell"
	executionpostgres "github.com/asabla/dataground/internal/execution/postgres"
	"github.com/asabla/dataground/internal/execution/s3store"
	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/workspace"
	"github.com/cedar-policy/cedar-go"
)

// This test uses a caller-owned disposable database, object store, Rosetta
// server, retained gateway, and provider installation. It must never use an
// operational acceptance: the supplied signature belongs to an ephemeral test
// reviewer, and the exact accepted scope must not already contain a service.
func TestGovernedWorkspaceLiveInvocation(t *testing.T) {
	if os.Getenv("DATAGROUND_TEST_GOVERNED_SOURCE_LIVE") != "true" {
		t.Skip("explicit live governed source fixture is required")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("live source profile requires Linux ARM64")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	acceptance, err := loadLocalRuntimeAcceptanceConfig(os.LookupEnv)
	if err != nil || acceptance == nil || !acceptance.workspaceSource || acceptance.strict == nil {
		t.Fatal("exact source acceptance configuration is required", err)
	}
	var envelope struct {
		Statement struct {
			ReviewerID string `json:"reviewerId"`
			Reason     string `json:"reason"`
		} `json:"statement"`
	}
	data, err := os.ReadFile(acceptance.envelopeFile)
	if err != nil || json.Unmarshal(data, &envelope) != nil || envelope.Statement.ReviewerID != "ephemeral-conformance-test" || envelope.Statement.Reason != "Ephemeral conformance test only; not operational acceptance or release evidence." {
		t.Fatal("ephemeral test acceptance is required")
	}
	checker := &localRuntimeAcceptanceChecker{config: *acceptance}
	defer checker.Close()
	if err := checker.Check(ctx); err != nil {
		t.Fatal("live acceptance and gateway observation", err)
	}
	databaseURL := os.Getenv("DATAGROUND_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("disposable test database is required")
	}
	t.Setenv("DATAGROUND_DATABASE_URL", databaseURL)
	db, err := persistence.OpenSQL(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	err = persistence.MigrateUp(ctx, db)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatal("test schema", err, closeErr)
	}
	pool, err := persistence.OpenPool(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	repo, store := persistence.NewRepository(pool), executionpostgres.New(pool)
	target := acceptance.target
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_services WHERE isolation_domain_id=$1`, target.isolationDomainID).Scan(&existing); err != nil || existing != 0 {
		t.Fatal("test acceptance scope must be unused", err)
	}
	const actor = "live-source-conformance"
	idem := func(key string) persistence.Idempotency {
		return persistence.Idempotency{IsolationDomainID: target.isolationDomainID, Method: "POST", Path: "/test/live-source/" + key, Key: "live-source-" + key, RequestDigest: sha256.Sum256([]byte(key))}
	}
	if _, err := repo.CreateService(ctx, idem("service"), persistence.CreateServiceInput{ID: target.serviceID, Name: "live source test", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRevision(ctx, idem("revision"), persistence.CreateRevisionInput{ID: target.revisionID, ServiceID: target.serviceID, RuntimeProfile: persistence.GovernedInvocationRuntimeProfile, RequiredCapabilities: []string{persistence.GovernedInvocationRuntimeProfile}, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	var entities cedar.EntityMap
	if err := json.Unmarshal([]byte(`[{"uid":{"type":"DataGround::Actor","id":"live-source-conformance"},"attrs":{},"parents":[]}]`), &entities); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(entities)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := reconcile.NewInvocationAuthorizationPolicyWithSourceEntities(reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: target.isolationDomainID, ServiceID: target.serviceID, RevisionID: target.revisionID}, "live-source-test", reconcile.CanonicalSourceCedarSchema(), []byte(`permit(principal == DataGround::Actor::"live-source-conformance", action, resource);`), canonical)
	if err != nil {
		t.Fatal(err)
	}
	reason := sha256.Sum256([]byte("ephemeral live source conformance"))
	if err := repo.InstallInvocationAuthorizationPolicy(ctx, persistence.InvocationAuthorizationPolicyRecord{Contract: policy.Contract, IsolationDomainID: target.isolationDomainID, ServiceID: target.serviceID, RevisionID: target.revisionID, PolicySetID: policy.PolicySetID, PolicyDigest: policy.Digest[:], Schema: policy.Schema, Policies: policy.Policies, Entities: policy.Entities, InstalledBy: actor, InstallationCorrelationID: identity.New("cor"), ReasonDigest: reason[:]}); err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	objects, err := s3store.New(s3store.Config{Endpoint: os.Getenv("DATAGROUND_S3_ENDPOINT"), Bucket: os.Getenv("DATAGROUND_S3_BUCKET"), AddressingStyle: s3store.PathStyle, AllowHTTPForLoopback: true, HTTPClient: &http.Client{Transport: transport, Timeout: 15 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	sources, err := s3store.NewWorkspaceSourceStore(objects)
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := identity.New("wsp")
	if _, err := repo.CreateWorkspace(ctx, idem("workspace"), persistence.CreateWorkspaceInput{ID: workspaceID, Name: "live source", ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	content := make([]byte, 32)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	content = []byte(hex.EncodeToString(content) + "\n")
	defer clear(content)
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "source.txt", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	defer clear(archive.Bytes())
	sum := sha256.Sum256(archive.Bytes())
	source := workspace.SourceRecord{SchemaVersion: workspace.SourceRecordV1, IsolationDomainID: target.isolationDomainID, WorkspaceID: workspaceID, ID: identity.New("src"), ArchiveDigest: "sha256:" + hex.EncodeToString(sum[:]), StorageProfileDigest: sources.WorkspaceSourceStorageDigest(), ArchiveBytes: int64(archive.Len()), SourceBytes: int64(len(content)), EntryCount: 1}
	if _, err := workspace.InstallSource(ctx, repo, sources, workspace.SourceInstallation{Record: source, ActorID: actor, CorrelationID: identity.New("cor")}, archive.Bytes()); err != nil {
		t.Fatal("live source installation", err)
	}
	// The command compiles through the real pinned Rosetta service and performs
	// immutable object installation. A local policy-byte fallback is not used.
	enforcementBinary := os.Getenv("DATAGROUND_TEST_ENFORCEMENT_BINARY")
	if enforcementBinary == "" {
		t.Fatal("built development enforcement command is required")
	}
	command := exec.CommandContext(ctx, enforcementBinary, "--repository-root", os.Getenv("DATAGROUND_TEST_REPOSITORY_ROOT"), "--isolation-domain", target.isolationDomainID, "--revision", target.revisionID, "--actor", actor, "--correlation-id", identity.New("cor"), "--rosetta-endpoint", os.Getenv("DATAGROUND_ROSETTA_CONFORMANCE_ENDPOINT"), "--s3-endpoint", os.Getenv("DATAGROUND_S3_ENDPOINT"), "--s3-bucket", os.Getenv("DATAGROUND_S3_BUCKET"))
	output, err := command.Output()
	if err != nil {
		t.Fatal("live enforcement installation failed")
	}
	var enforcement struct {
		BundleID              string `json:"bundleId"`
		Digest                string `json:"digest"`
		CertificationEligible bool   `json:"certificationEligible"`
	}
	if json.Unmarshal(output, &enforcement) != nil || enforcement.BundleID == "" || enforcement.Digest != strictLocalEnforcementDigest || enforcement.CertificationEligible {
		t.Fatal("invalid enforcement receipt")
	}
	plan := execution.ExecutionPlan{SchemaVersion: execution.ExecutionPlanSchemaV2, IsolationDomainID: target.isolationDomainID, RevisionID: target.revisionID, RuntimeProfile: persistence.GovernedInvocationRuntimeProfile, EnvironmentRevisionID: "live-source-test", ImageReference: acceptance.image, EnvironmentManifestDigest: "sha256:" + acceptance.envelopeSHA256, EnforcementBundleID: enforcement.BundleID, EnforcementBundleDigest: enforcement.Digest, RuntimeMatrixID: "live-source-test", RuntimeMatrixDigest: "sha256:" + acceptance.trustSHA256, ProviderProfiles: []string{governedProviderProfile}, RequiredCapabilities: []string{persistence.GovernedInvocationRuntimeProfile}, WorkspaceSource: &source}
	if _, err := store.BindExecutionPlan(ctx, execution.ExecutionPlanBinding{Plan: plan, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	planDigest, err := execution.DigestExecutionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(workspacePlanEnvironment, planDigest)
	t.Setenv(workspaceGrantEnvironment, "1")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.ChangeProviderCredentialGrant(ctx, persistence.ProviderCredentialGrantChange{Contract: persistence.ProviderCredentialGrantContract, IsolationDomainID: target.isolationDomainID, RevisionID: target.revisionID, ProviderProfile: governedProviderProfile, Purpose: persistence.ProviderCredentialPurposeAgentInference, Generation: 1, Operation: "activate", ActivatedAt: now.Add(-time.Second), ExpiresAt: now.Add(20 * time.Minute), ActorID: actor, ReasonDigest: reason[:], CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	var published bytes.Buffer
	if err := runDevelopmentPublication(ctx, []string{"publish-development", "--expected-version", "1", "--plan-digest", planDigest, "--policy-digest", "sha256:" + hex.EncodeToString(policy.Digest[:]), "--actor", actor, "--correlation-id", identity.New("cor")}, &published); err != nil {
		t.Fatal("live publication", err)
	}
	if err := repo.ChangeWorkspaceSourceGrant(ctx, workspace.SourceGrantChange{Contract: workspace.SourceGrantV1, Record: source, RevisionID: target.revisionID, Generation: 1, Operation: "activate", ActivatedAt: now.Add(-time.Second), ExpiresAt: now.Add(20 * time.Minute), ActorID: actor, ReasonDigest: "sha256:" + hex.EncodeToString(reason[:]), CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AssignAlias(ctx, idem("alias"), persistence.AssignAliasInput{ID: identity.New("als"), ServiceID: target.serviceID, Name: "live-source", RevisionID: target.revisionID, ActorID: actor, CorrelationID: identity.New("cor")}); err != nil {
		t.Fatal(err)
	}
	config, err := loadWorkerConfig(os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	driver, resources, err := composeWorkerDriver(ctx, pool, repo, config)
	if err != nil {
		t.Fatal("live worker composition", err)
	}
	defer func() {
		if err := resources.Close(); err != nil {
			t.Error("worker resource cleanup", err)
		}
	}()
	invocationID := identity.New("inv")
	if _, err := repo.AcceptInvocation(ctx, idem("invoke"), persistence.AcceptInvocationInput{ID: invocationID, ServiceID: target.serviceID, Alias: "live-source", ActorID: actor, CorrelationID: identity.New("cor"), Deadline: time.Now().UTC().Add(5 * time.Minute), Input: map[string]any{"prompt": "Copy the exact bytes of source.txt in the current workspace to result.txt using the shell command cp source.txt result.txt. Do not change the source or invent its contents. Then report completion.", "artifacts": []any{map[string]any{"id": "source-copy", "name": "source-copy", "kind": "file", "mediaType": "text/plain", "sandboxPath": "/sandbox/workspace/result.txt"}}}}); err != nil {
		t.Fatal(err)
	}
	invocation, err := repo.GetInvocation(ctx, target.isolationDomainID, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		record, err := store.GetExecutionByOperation(cleanup, target.isolationDomainID, invocation.OperationID)
		if errors.Is(err, execution.ErrExecutionMissing) {
			return
		}
		if err != nil {
			t.Error("read test execution for cleanup", err)
			return
		}
		provider := openshell.New(openshell.Config{Binary: config.openShellBinary, ExpectedVersion: governedOpenShellVersion, StateStore: store}, openshell.ExecRunner{})
		if err := provider.Terminate(cleanup, execution.ExecutionRef{IsolationDomainID: target.isolationDomainID, ID: record.ID}); err != nil {
			t.Error("test sandbox cleanup", err)
		}
	}()
	worker := reconcile.New(workerReconcileStore(repo, config), driver, "live-source-conformance")
	for invocation.CompletedAt == nil {
		if err := resources.Ready(ctx); err != nil {
			t.Fatal("worker readiness", err)
		}
		if ran, err := worker.RunOne(ctx, persistence.OperationKindInvocation); err != nil {
			t.Fatal("live reconciliation", err)
		} else if !ran {
			select {
			case <-time.After(250 * time.Millisecond):
			case <-ctx.Done():
				t.Fatal("live reconciliation deadline", ctx.Err())
			}
		}
		invocation, err = repo.GetInvocation(ctx, target.isolationDomainID, invocationID)
		if err != nil {
			t.Fatal(err)
		}
		if invocation.CompletedAt != nil {
			break
		}
	}
	if invocation.State != "succeeded" || invocation.CompletedAt == nil {
		t.Fatalf("live invocation did not succeed: state=%s", invocation.State)
	}
	artifacts, err := s3store.NewArtifactStore(objects, config.maximumArtifactBytes)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := artifact.NewReader(repo, artifacts, config.maximumArtifactBytes)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, artifactID := range invocation.ArtifactIDs {
		value, err := reader.Read(ctx, target.isolationDomainID, invocationID, artifactID)
		if err != nil {
			t.Fatal("durable artifact read", err)
		}
		if value.Record.Name == "source-copy" {
			found = bytes.Equal(value.Bytes, content)
		}
		clear(value.Bytes)
		if _, err := reader.Read(ctx, identity.New("iso"), invocationID, artifactID); !errors.Is(err, artifact.ErrInvocationArtifactMissing) {
			t.Fatal("cross-domain artifact read was not denied", err)
		}
	}
	if !found {
		t.Fatal("source bytes did not reach the declared durable artifact")
	}
	for _, table := range []string{"invocation_workspace_preparations", "invocation_workspace_completions", "workspace_source_use_decisions", "invocation_runtime_attempts"} {
		var count int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE isolation_domain_id=$1 AND operation_id=$2", target.isolationDomainID, invocation.OperationID).Scan(&count); err != nil || count < 1 {
			t.Fatal("missing durable source/runtime evidence", table, err)
		}
	}
}
