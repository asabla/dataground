package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/runtimeevidence"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
)

func TestWorkspacePublicationRequiresSeparateProfileAndOperatorCommand(t *testing.T) {
	values := validStrictAcceptanceEnvironment()
	values["DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"] = workspaceLocalRuntimeProfile
	args := publicationArguments()
	config, err := loadDevelopmentPublication(args, mapEnvironment(values))
	if err != nil || config.input.Contract != persistence.WorkspaceDevelopmentPublicationContract || !config.acceptance.workspaceSource || config.acceptance.strict == nil {
		t.Fatal("source publication binding missing", err)
	}
	for _, command := range []string{"queue-development-publication", "reconcile-development-publication", "reconcile-authorized-publication", "prepare-publication-configuration"} {
		changed := append([]string(nil), args...)
		changed[0] = command
		if _, err := loadDevelopmentPublication(changed, mapEnvironment(values)); err == nil {
			t.Fatal("unversioned source queue path accepted")
		}
	}
	if _, err := loadWorkerConfig(mapEnvironment(values)); err == nil {
		t.Fatal("publication profile activated a worker without source pins")
	}
	plan := execution.ExecutionPlan{SchemaVersion: execution.ExecutionPlanSchemaV2, IsolationDomainID: config.input.Target.IsolationDomainID, RevisionID: config.input.Target.RevisionID, RuntimeProfile: config.input.Target.RuntimeProfile, EnvironmentRevisionID: "environment", ImageReference: config.acceptance.image, EnvironmentManifestDigest: "sha256:" + strings.Repeat("a", 64), EnforcementBundleID: "strict-policy", EnforcementBundleDigest: strictLocalEnforcementDigest, RuntimeMatrixID: "matrix", RuntimeMatrixDigest: "sha256:" + strings.Repeat("b", 64), ProviderProfiles: []string{"codex"}, RequiredCapabilities: []string{config.input.Target.RuntimeProfile}}
	plan.WorkspaceSource = &workspace.SourceRecord{SchemaVersion: workspace.SourceRecordV1, IsolationDomainID: plan.IsolationDomainID, WorkspaceID: "wsp_0123456789abcdefghij", ID: "src_0123456789abcdefghij", ArchiveDigest: "sha256:" + strings.Repeat("c", 64), StorageProfileDigest: "sha256:" + strings.Repeat("d", 64), ArchiveBytes: 2048, EntryCount: 1, SourceBytes: 1}
	if !validPublicationDevelopmentPlan(plan, config.acceptance) || validGovernedDevelopmentPlan(plan, config.acceptance.image, strictLocalRuntimeProfile) {
		t.Fatal("source publication widened worker admission")
	}
	plan.WorkspaceSource = nil
	if validPublicationDevelopmentPlan(plan, config.acceptance) {
		t.Fatal("source acceptance accepted a source-free plan")
	}
}

func TestWorkspaceAcceptanceConsumerRequiresHelperContractAndStrictDeployment(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "null", "changed", "old-profile", "deployment"} {
		t.Run(mode, func(t *testing.T) {
			values := validStrictAcceptanceEnvironment()
			values["DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"] = workspaceLocalRuntimeProfile
			config, err := loadLocalRuntimeAcceptanceConfig(mapEnvironment(values))
			if err != nil {
				t.Fatal(err)
			}
			receipt := strictAcceptanceReceipt(*config)
			receipt["profile"] = workspaceLocalRuntimeProfile
			receipt["workspaceSourceContract"] = "dataground.workspace-source-materialization/v1"
			switch mode {
			case "missing":
				delete(receipt, "workspaceSourceContract")
			case "null":
				receipt["workspaceSourceContract"] = nil
			case "changed":
				receipt["workspaceSourceContract"] = "dataground.workspace-source-materialization/v2"
			case "old-profile":
				receipt["profile"] = strictLocalRuntimeProfile
			case "deployment":
				receipt["gatewayConfigSHA256"] = strings.Repeat("0", 64)
			}
			deployment := &fakeRuntimeDeployment{}
			checker := &localRuntimeAcceptanceChecker{config: *config, run: func(context.Context, string, []string, []string) ([]byte, error) { return json.Marshal(receipt) }, observe: func(runtimeevidence.ObservedDockerTopologyConfig) (runtimeDeploymentObservation, error) {
				return deployment, nil
			}}
			defer checker.Close()
			if err := checker.Check(context.Background()); (err == nil) != (mode == "valid") {
				t.Fatal("unexpected source acceptance result", err)
			}
			if mode == "valid" && deployment.checks.Load() == 0 {
				t.Fatal("source acceptance skipped deployed gateway observation")
			}
		})
	}
}

func TestSourceFreeAcceptanceRejectsSourceContractField(t *testing.T) {
	for _, value := range []any{nil, "dataground.workspace-source-materialization/v1"} {
		config, err := loadLocalRuntimeAcceptanceConfig(mapEnvironment(validStrictAcceptanceEnvironment()))
		if err != nil {
			t.Fatal(err)
		}
		receipt := strictAcceptanceReceipt(*config)
		receipt["workspaceSourceContract"] = value
		checker := &localRuntimeAcceptanceChecker{config: *config, run: func(context.Context, string, []string, []string) ([]byte, error) { return json.Marshal(receipt) }}
		if err := checker.Check(context.Background()); err == nil {
			t.Fatal("source-free acceptance accepted source contract")
		}
		checker.Close()
	}
}
