package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/execution/runtimeevidence"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/runtime/nativehost"
	"github.com/asabla/dataground/internal/workspace"
)

func ownedWorkspaceFixture(t *testing.T) (workerConfig, execution.ExecutionPlan, map[string]string) {
	t.Helper()
	_, plan, values := workspaceWorkerFixture(t)
	values["DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"] = ownedWorkspaceRuntimeProfile
	values["DATAGROUND_INVOCATION_ARTIFACT_MAX_BYTES"] = strconv.Itoa(workspace.MaximumChangeSetBytes)
	config, err := loadWorkerConfig(mapEnvironment(values))
	if err != nil {
		t.Fatal(err)
	}
	return config, plan, values
}

func ownedAcceptanceReceipt(config localRuntimeAcceptanceConfig) map[string]any {
	receipt := strictAcceptanceReceipt(config)
	receipt["profile"] = ownedWorkspaceRuntimeProfile
	receipt["workspaceSourceContract"] = "dataground.workspace-source-materialization/v1"
	receipt["nativeOwnerContract"] = "dataground.native-owner/v1"
	receipt["workspaceChangesContract"] = "dataground.workspace-changes/v1"
	return receipt
}

func TestOwnedWorkspaceAcceptanceRequiresExactHelpersAndProfile(t *testing.T) {
	for _, field := range []string{"nativeOwnerContract", "workspaceChangesContract", "workspaceSourceContract", "profile"} {
		for index, value := range []any{nil, "unknown", 1} {
			t.Run(field+"/"+strconv.Itoa(index), func(t *testing.T) {
				config, _, _ := ownedWorkspaceFixture(t)
				receipt := ownedAcceptanceReceipt(*config.localAcceptance)
				receipt[field] = value
				checker := &localRuntimeAcceptanceChecker{config: *config.localAcceptance, run: func(context.Context, string, []string, []string) ([]byte, error) { return json.Marshal(receipt) }}
				defer checker.Close()
				if err := checker.Check(context.Background()); err == nil {
					t.Fatal("substituted helper or profile accepted")
				}
				delete(receipt, field)
				if err := checker.Check(context.Background()); err == nil {
					t.Fatal("missing helper or profile accepted")
				}
			})
		}
	}
	config, _, _ := ownedWorkspaceFixture(t)
	deployment := &fakeRuntimeDeployment{}
	receipt := ownedAcceptanceReceipt(*config.localAcceptance)
	checker := &localRuntimeAcceptanceChecker{config: *config.localAcceptance, run: func(context.Context, string, []string, []string) ([]byte, error) { return json.Marshal(receipt) }, observe: func(runtimeevidence.ObservedDockerTopologyConfig) (runtimeDeploymentObservation, error) {
		return deployment, nil
	}}
	defer checker.Close()
	if err := checker.Check(context.Background()); err != nil || deployment.checks.Load() != 1 {
		t.Fatal("owned acceptance omitted deployment observation", err)
	}
	deployment.check = func(context.Context) error { return errors.New("gateway changed") }
	if err := checker.Check(context.Background()); err == nil {
		t.Fatal("changed gateway accepted")
	}
}

func TestOlderAcceptanceRejectsOwnedHelperFields(t *testing.T) {
	for _, profile := range []string{localRuntimeProfile, strictLocalRuntimeProfile, workspaceLocalRuntimeProfile} {
		for _, field := range []string{"nativeOwnerContract", "workspaceChangesContract"} {
			for _, value := range []any{nil, "dataground.native-owner/v1", "dataground.workspace-changes/v1"} {
				values := validStrictAcceptanceEnvironment()
				values["DATAGROUND_DEVELOPMENT_RUNTIME_PROFILE"] = profile
				config, err := loadLocalRuntimeAcceptanceConfig(mapEnvironment(values))
				if err != nil {
					t.Fatal(err)
				}
				receipt := acceptanceReceipt(*config)
				if config.strict != nil {
					receipt = strictAcceptanceReceipt(*config)
				}
				receipt["profile"] = profile
				if config.workspaceSource {
					receipt["workspaceSourceContract"] = "dataground.workspace-source-materialization/v1"
				}
				receipt[field] = value
				checker := &localRuntimeAcceptanceChecker{config: *config, run: func(context.Context, string, []string, []string) ([]byte, error) { return json.Marshal(receipt) }}
				if err := checker.Check(context.Background()); err == nil {
					t.Fatal("older profile accepted owned helper fields", profile, field)
				}
				checker.Close()
			}
		}
	}
}

func TestOwnedWorkspaceWorkerRequiresExportPolicyAndCapacity(t *testing.T) {
	config, plan, values := ownedWorkspaceFixture(t)
	if !config.sourceEnabled() || !config.exportEnabled() || !config.acceptsInvocationPolicy(reconcile.InvocationAuthorizationPolicyWorkspaceExportContract) {
		t.Fatal("owned profile did not bind source and export policy")
	}
	for _, policy := range []string{reconcile.InvocationAuthorizationPolicyApprovalContract, reconcile.InvocationAuthorizationPolicyPublicationContract, reconcile.InvocationAuthorizationPolicySourceContract} {
		if config.acceptsInvocationPolicy(policy) {
			t.Fatal("older policy accepted")
		}
	}
	if _, err := config.workspaceConfiguration(plan); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{workspacePlanEnvironment, workspaceGrantEnvironment, "DATAGROUND_LOCAL_RUNTIME_SUPERVISOR_IMAGE"} {
		old := values[key]
		delete(values, key)
		if _, err := loadWorkerConfig(mapEnvironment(values)); err == nil {
			t.Fatal("missing independent pin accepted", key)
		}
		values[key] = old
	}
	values["DATAGROUND_INVOCATION_ARTIFACT_MAX_BYTES"] = strconv.Itoa(workspace.MaximumChangeSetBytes - 1)
	if _, err := loadWorkerConfig(mapEnvironment(values)); err == nil {
		t.Fatal("worker accepted insufficient artifact capacity")
	}
}

type ownedWorkerProvider struct {
	execution.ExecutionProvider
	starts, captures int
}

func (p *ownedWorkerProvider) StartOwnedRuntime(context.Context, execution.ExecutionRef) (*nativehost.Session, error) {
	p.starts++
	return nil, nil
}
func (p *ownedWorkerProvider) ExportWorkspaceChanges(context.Context, execution.ExecutionRef, *workspace.Snapshot) (*workspace.ChangeSet, error) {
	p.captures++
	return nil, nil
}

func TestOwnedWorkspaceProviderRejectsScopeExpiryAndUnownedStartup(t *testing.T) {
	config, _, _ := ownedWorkspaceFixture(t)
	underlying := &ownedWorkerProvider{}
	denied := false
	provider := &certifiedExecutionProvider{ExecutionProvider: underlying, target: config.runtimeTarget(), workspaceExport: true, readiness: expiryCertificationFunc(func(context.Context) error {
		if denied {
			return ErrRuntimeCertificationUnavailable
		}
		return nil
	})}
	ref := execution.ExecutionRef{IsolationDomainID: config.isolationDomainID, ID: "exe_0123456789abcdefghij"}
	if _, err := provider.StartOwnedRuntime(context.Background(), ref); err != nil || underlying.starts != 1 {
		t.Fatal(err)
	}
	if _, err := provider.ExportWorkspaceChanges(context.Background(), ref, nil); err != nil || underlying.captures != 1 {
		t.Fatal(err)
	}
	if _, err := provider.StartRuntime(context.Background(), ref); err == nil {
		t.Fatal("owned profile permitted unowned startup")
	}
	for _, mode := range []string{"expired", "scope", "profile"} {
		denied = mode == "expired"
		provider.workspaceExport = mode != "profile"
		request := ref
		if mode == "scope" {
			request.IsolationDomainID = "iso_99999999999999999999"
		}
		if _, err := provider.StartOwnedRuntime(context.Background(), request); err == nil || underlying.starts != 1 {
			t.Fatal("invalid owned startup reached provider", mode)
		}
		if _, err := provider.ExportWorkspaceChanges(context.Background(), request, nil); err == nil || underlying.captures != 1 {
			t.Fatal("invalid export reached provider", mode)
		}
	}
}
