package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/reconcile"
	dgruntime "github.com/asabla/dataground/internal/runtime"
	"github.com/asabla/dataground/internal/workspace"
)

func workspaceWorkerFixture(t *testing.T) (workerConfig, execution.ExecutionPlan, map[string]string) {
	t.Helper()
	values := validStrictAcceptanceEnvironment()
	values["DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"] = workspaceLocalRuntimeProfile
	acceptance, err := loadLocalRuntimeAcceptanceConfig(mapEnvironment(values))
	if err != nil {
		t.Fatal(err)
	}
	plan := execution.ExecutionPlan{SchemaVersion: execution.ExecutionPlanSchemaV2, IsolationDomainID: acceptance.target.isolationDomainID, RevisionID: acceptance.target.revisionID, RuntimeProfile: reconcile.CodexAppServerRuntimeProfileV1, EnvironmentRevisionID: "environment", ImageReference: acceptance.image, EnvironmentManifestDigest: "sha256:" + strings.Repeat("a", 64), EnforcementBundleID: "strict-policy", EnforcementBundleDigest: strictLocalEnforcementDigest, RuntimeMatrixID: "matrix", RuntimeMatrixDigest: "sha256:" + strings.Repeat("b", 64), ProviderProfiles: []string{"codex"}, RequiredCapabilities: []string{reconcile.CodexAppServerRuntimeProfileV1}}
	plan.WorkspaceSource = &workspace.SourceRecord{SchemaVersion: workspace.SourceRecordV1, IsolationDomainID: plan.IsolationDomainID, WorkspaceID: "wsp_0123456789abcdefghij", ID: "src_0123456789abcdefghij", ArchiveDigest: "sha256:" + strings.Repeat("c", 64), StorageProfileDigest: "sha256:" + strings.Repeat("d", 64), ArchiveBytes: 2048, EntryCount: 1, SourceBytes: 1}
	digest, err := execution.DigestExecutionPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	values[workspacePlanEnvironment], values[workspaceGrantEnvironment] = digest, "1"
	config, err := loadWorkerConfig(mapEnvironment(values))
	if err != nil {
		t.Fatal(err)
	}
	return config, plan, values
}

func TestWorkspaceWorkerRequiresIndependentPlanAndGrantPins(t *testing.T) {
	config, _, values := workspaceWorkerFixture(t)
	if !config.sourceEnabled() || config.workspaceGrantGeneration != 1 || config.workspacePlanDigest == "" {
		t.Fatal("missing source worker binding")
	}
	for _, key := range []string{workspacePlanEnvironment, workspaceGrantEnvironment} {
		for _, value := range []string{"", "0", "-1", "01", "1.0", "9223372036854775808", "sha256:bad"} {
			changed := map[string]string{}
			for k, v := range values {
				changed[k] = v
			}
			changed[key] = value
			if _, err := loadWorkerConfig(mapEnvironment(changed)); err == nil {
				t.Fatal("invalid source pin accepted", key)
			}
		}
		for _, profile := range []string{strictLocalRuntimeProfile, localRuntimeProfile, governedCertificationProfile} {
			changed := validStrictAcceptanceEnvironment()
			changed["DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"] = profile
			changed[key] = values[key]
			if _, err := loadWorkerConfig(mapEnvironment(changed)); err == nil {
				t.Fatal("source pin ignored by another profile")
			}
		}
		if _, err := loadWorkerConfig(mapEnvironment(map[string]string{key: values[key]})); err == nil {
			t.Fatal("source pin ignored by reference mode")
		}
	}
}

type workspaceWorkerObjects struct {
	workspace.SourceObjectReader
	digest string
}

func (s workspaceWorkerObjects) WorkspaceSourceStorageDigest() string { return s.digest }

type workspaceWorkerStore struct {
	reconcile.InvocationWorkspaceStore
}

func TestWorkspaceWorkerBindsPreparationAndAdmissionToSamePlan(t *testing.T) {
	for _, mode := range []string{"valid", "source", "plan", "scope", "revision", "storage", "grant", "image", "policy", "version"} {
		t.Run(mode, func(t *testing.T) {
			config, plan, _ := workspaceWorkerFixture(t)
			objects := workspaceWorkerObjects{digest: plan.WorkspaceSource.StorageProfileDigest}
			switch mode {
			case "source":
				plan.WorkspaceSource.ID = "src_00000000000000000001"
			case "plan":
				config.workspacePlanDigest = "sha256:" + strings.Repeat("f", 64)
			case "scope":
				plan.IsolationDomainID = "iso_99999999999999999999"
			case "revision":
				plan.RevisionID = "rev_00000000000000000001"
			case "storage":
				objects.digest = "sha256:" + strings.Repeat("f", 64)
			case "grant":
				config.workspaceGrantGeneration = 0
			case "image":
				plan.ImageReference += "other"
			case "policy":
				plan.EnforcementBundleDigest = localEnforcementDigest
			case "version":
				plan.SchemaVersion, plan.WorkspaceSource = execution.ExecutionPlanSchemaV1, nil
			}
			preparer, err := config.workspacePreparer(workspaceWorkerStore{}, objects, plan)
			if mode == "valid" {
				if err != nil || preparer == nil {
					t.Fatal("valid source composition rejected", err)
				}
			} else if err == nil || preparer != nil {
				t.Fatal("substituted source composition accepted")
			}
		})
	}
	config, plan, _ := workspaceWorkerFixture(t)
	calls := 0
	readiness := expiryCertificationFunc(func(context.Context) error {
		calls++
		if calls > 1 {
			return ErrRuntimeCertificationUnavailable
		}
		return nil
	})
	plans := governedExecutionPlanStore{ExecutionPlanStore: publicationPlans{plan: plan}, readiness: readiness, target: config.runtimeTarget(), candidateImage: config.localAcceptance.image, acceptanceProfile: workspaceLocalRuntimeProfile, workspacePlanDigest: config.workspacePlanDigest}
	if _, err := plans.GetExecutionPlan(context.Background(), plan.IsolationDomainID, plan.RevisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := plans.GetExecutionPlan(context.Background(), plan.IsolationDomainID, plan.RevisionID); err == nil {
		t.Fatal("plan resolution ignored acceptance loss")
	}
	if _, err := plans.GetExecutionPlan(context.Background(), "iso_99999999999999999999", plan.RevisionID); err == nil || calls != 2 {
		t.Fatal("foreign plan reached readiness")
	}
	if !config.acceptsInvocationPolicy(reconcile.InvocationAuthorizationPolicySourceContract) || config.acceptsInvocationPolicy(reconcile.InvocationAuthorizationPolicyApprovalContract) || config.acceptsInvocationPolicy(reconcile.InvocationAuthorizationPolicyPublicationContract) {
		t.Fatal("source worker accepted an older policy")
	}
}

type workspaceWorkerProvider struct {
	execution.ExecutionProvider
	calls int
}

func (p *workspaceWorkerProvider) MaterializeWorkspace(context.Context, execution.ExecutionRef, *workspace.Snapshot) (workspace.Receipt, error) {
	p.calls++
	return workspace.Receipt{SchemaVersion: "dataground.workspace-source-materialization/v1"}, nil
}

func TestWorkspaceTransferChecksAcceptanceAndScopeBeforeProvider(t *testing.T) {
	config, _, _ := workspaceWorkerFixture(t)
	underlying := &workspaceWorkerProvider{}
	denied := false
	provider := &certifiedExecutionProvider{ExecutionProvider: underlying, target: config.runtimeTarget(), readiness: expiryCertificationFunc(func(context.Context) error {
		if denied {
			return ErrRuntimeCertificationUnavailable
		}
		return nil
	})}
	ref := execution.ExecutionRef{IsolationDomainID: config.isolationDomainID, ID: "exe_0123456789abcdefghij"}
	if _, err := provider.MaterializeWorkspace(context.Background(), ref, nil); err != nil || underlying.calls != 1 {
		t.Fatal(err)
	}
	denied = true
	if _, err := provider.MaterializeWorkspace(context.Background(), ref, nil); !errors.Is(err, ErrRuntimeCertificationUnavailable) || underlying.calls != 1 {
		t.Fatal("readiness loss reached transfer", err)
	}
	denied = false
	ref.IsolationDomainID = "iso_99999999999999999999"
	if _, err := provider.MaterializeWorkspace(context.Background(), ref, nil); err == nil || underlying.calls != 1 {
		t.Fatal("foreign source reached transfer")
	}
}

func TestWorkspaceRuntimeUsesFixedPrivateDirectoryAndArtifactBoundary(t *testing.T) {
	for _, path := range []string{"", "/sandbox/workspace/report.json", "/sandbox/report.json", "/sandbox/workspace-other/report.json"} {
		target := governedArtifactTarget(path)
		if path == "" {
			target.Input = map[string]any{"prompt": "inspect the source"}
		}
		request, err := (governedCodexRuntimeRequestBuilder{workspaceSource: true, model: "accepted-model"}).BuildInvocationRuntimeRequest(target)
		if path == "" || path == "/sandbox/workspace/report.json" {
			if err != nil || request.WorkingDir != "/sandbox/workspace" || request.SandboxMode != dgruntime.SandboxWorkspaceWrite || request.ApprovalMode != dgruntime.ApprovalInteractive || request.Model != "accepted-model" {
				t.Fatal("source runtime boundary changed", err)
			}
		} else if !errors.Is(err, reconcile.ErrInvocationRuntimeInputInvalid) {
			t.Fatal("artifact escaped source workspace", err)
		}
	}
}
