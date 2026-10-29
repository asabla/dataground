package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/runtimeevidence"
	"github.com/asabla/dataground/internal/persistence"
	"github.com/asabla/dataground/internal/workspace"
)

func TestWorkspacePublicationRequiresSeparateProfileAndVersionedCommands(t *testing.T) {
	values := validStrictAcceptanceEnvironment()
	values["DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"] = workspaceLocalRuntimeProfile
	args := publicationArguments()
	config, err := loadDevelopmentPublication(args, mapEnvironment(values))
	if err != nil || config.input.Contract != persistence.WorkspaceDevelopmentPublicationContract || !config.acceptance.workspaceSource || config.acceptance.strict == nil {
		t.Fatal("source publication binding missing", err)
	}
	for _, command := range []string{"queue-development-publication", "reconcile-development-publication"} {
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

func TestWorkspacePublicationExportsVersionedPublicConfiguration(t *testing.T) {
	values := validStrictAcceptanceEnvironment()
	values["DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"] = workspaceLocalRuntimeProfile
	var digest string
	for _, command := range []string{"prepare-publication-configuration", "reconcile-authorized-publication"} {
		args := publicationArguments()[:7]
		args[0] = command
		config, err := loadDevelopmentPublication(args, mapEnvironment(values))
		if err != nil || config.input.Contract != persistence.WorkspaceDevelopmentPublicationContract || config.publicationPolicyContract() != "dataground.invocation-authorization-policy/v6" || config.input.AuthorizedPublicationVersion() != 5 {
			t.Fatal("source command lost reviewed contract", command, err)
		}
		if digest != "" && digest != config.input.VerificationDigest {
			t.Fatal("producer and consumer pins differ")
		}
		digest = config.input.VerificationDigest
		var output bytes.Buffer
		if err := writePublicPublicationConfiguration(&output, config.input); err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if json.Unmarshal(output.Bytes(), &fields) != nil || fields["contract"] != "dataground.api-governed-publication/v2" || len(fields) != 9 {
			t.Fatal("unsafe or unversioned source configuration")
		}
		for _, private := range []string{config.acceptance.envelopeFile, config.acceptance.trustFile, config.endpoint} {
			if private != "" && strings.Contains(output.String(), private) {
				t.Fatal("configuration disclosed deployment routing")
			}
		}
	}
}

func TestOwnedWorkspacePublicationKeepsProducerConsumerAndLegacyContractsDistinct(t *testing.T) {
	values := validStrictAcceptanceEnvironment()
	values["DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"] = ownedWorkspaceRuntimeProfile
	var digest string
	for _, command := range []string{"publish-development", "prepare-publication-configuration", "reconcile-authorized-publication"} {
		args := publicationArguments()
		if command != "publish-development" {
			args = args[:7]
		}
		args[0] = command
		config, err := loadDevelopmentPublication(args, mapEnvironment(values))
		if err != nil || config.input.Contract != persistence.OwnedWorkspacePublicationContract || config.publicationPolicyContract() != "dataground.invocation-authorization-policy/v7" || config.input.AuthorizedPublicationVersion() != 6 {
			t.Fatal("owned publication lost its contract", command, err)
		}
		if digest != "" && digest != config.input.VerificationDigest {
			t.Fatal("owned producer and consumer pins differ")
		}
		digest = config.input.VerificationDigest
		var output bytes.Buffer
		if err := writePublicPublicationConfiguration(&output, config.input); err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if json.Unmarshal(output.Bytes(), &fields) != nil || fields["contract"] != "dataground.api-governed-publication/v3" || len(fields) != 9 {
			t.Fatal("owned configuration is not closed and versioned")
		}
		for _, private := range []string{config.acceptance.envelopeFile, config.acceptance.trustFile, config.endpoint} {
			if strings.Contains(output.String(), private) {
				t.Fatal("configuration disclosed deployment routing")
			}
		}
	}
	for _, command := range []string{"queue-development-publication", "reconcile-development-publication"} {
		args := publicationArguments()
		args[0] = command
		if _, err := loadDevelopmentPublication(args, mapEnvironment(values)); err == nil {
			t.Fatal("owned intent entered an unversioned operator queue")
		}
	}
	values["DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"] = workspaceLocalRuntimeProfile
	legacy, err := loadDevelopmentPublication(publicationArguments(), mapEnvironment(values))
	if err != nil || legacy.input.VerificationDigest == digest {
		t.Fatal("owned acceptance shares legacy verification identity", err)
	}
}
