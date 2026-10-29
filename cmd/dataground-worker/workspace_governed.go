package main

import (
	"context"
	"regexp"
	"strconv"

	"github.com/asabla/dataground/internal/artifact"
	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/runtime/nativehost"
	"github.com/asabla/dataground/internal/workspace"
)

var workspacePlanDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

const workspacePlanEnvironment = "DATAGROUND_WORKSPACE_PLAN_DIGEST"
const workspaceGrantEnvironment = "DATAGROUND_WORKSPACE_SOURCE_GRANT_GENERATION"

func (config workerConfig) sourceEnabled() bool {
	return config.localAcceptance != nil && config.localAcceptance.workspaceSource
}

func (config workerConfig) exportEnabled() bool {
	return config.localAcceptance != nil && config.localAcceptance.workspaceExport
}

func (config *workerConfig) loadWorkspacePins(lookup environmentLookup) error {
	digest, hasDigest := lookup(workspacePlanEnvironment)
	generation, hasGeneration := lookup(workspaceGrantEnvironment)
	if !config.sourceEnabled() {
		if hasDigest || hasGeneration {
			return execution.ErrWorkspaceSourceProvisioningUnavailable
		}
		return nil
	}
	parsed, err := strconv.ParseInt(generation, 10, 64)
	if !hasDigest || !hasGeneration || !workspacePlanDigestPattern.MatchString(digest) || err != nil || parsed < 1 || strconv.FormatInt(parsed, 10) != generation {
		return execution.ErrWorkspaceSourceProvisioningUnavailable
	}
	config.workspacePlanDigest, config.workspaceGrantGeneration = digest, parsed
	return nil
}

func (store governedExecutionPlanStore) validPlan(plan execution.ExecutionPlan) bool {
	if store.acceptanceProfile != workspaceLocalRuntimeProfile && store.acceptanceProfile != ownedWorkspaceRuntimeProfile {
		return store.workspacePlanDigest == "" && validGovernedDevelopmentPlan(plan, store.candidateImage, store.acceptanceProfile)
	}
	if !workspacePlanDigestPattern.MatchString(store.workspacePlanDigest) || !validPublicationDevelopmentPlan(plan, localRuntimeAcceptanceConfig{workspaceSource: true, image: store.candidateImage}) {
		return false
	}
	digest, err := execution.DigestExecutionPlan(plan)
	return err == nil && digest == store.workspacePlanDigest
}

func (config workerConfig) acceptsInvocationPolicy(contract string) bool {
	if config.exportEnabled() {
		return contract == reconcile.InvocationAuthorizationPolicyWorkspaceExportContract
	}
	if config.sourceEnabled() {
		return contract == reconcile.InvocationAuthorizationPolicySourceContract
	}
	return contract == reconcile.InvocationAuthorizationPolicyApprovalContract || contract == reconcile.InvocationAuthorizationPolicyPublicationContract
}

func (config workerConfig) workspacePreparer(store reconcile.InvocationWorkspaceStore, objects workspace.SourceObjectReader, plan execution.ExecutionPlan) (*reconcile.InvocationWorkspacePreparer, error) {
	configuration, err := config.workspaceConfiguration(plan)
	if err != nil {
		return nil, err
	}
	return reconcile.NewInvocationWorkspacePreparer(store, objects, configuration)
}

func (config workerConfig) workspaceConfiguration(plan execution.ExecutionPlan) (reconcile.InvocationWorkspaceConfig, error) {
	target := config.runtimeTarget()
	if !config.sourceEnabled() || config.workspaceGrantGeneration < 1 || plan.IsolationDomainID != target.isolationDomainID || plan.RevisionID != target.revisionID || !(governedExecutionPlanStore{candidateImage: config.localAcceptance.image, acceptanceProfile: config.localAcceptance.acceptanceProfile(), workspacePlanDigest: config.workspacePlanDigest}).validPlan(plan) {
		return reconcile.InvocationWorkspaceConfig{}, execution.ErrWorkspaceSourceProvisioningUnavailable
	}
	return reconcile.InvocationWorkspaceConfig{
		Scope:  reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: target.isolationDomainID, ServiceID: target.serviceID, RevisionID: target.revisionID},
		Source: *plan.WorkspaceSource, PlanDigest: config.workspacePlanDigest, GrantGeneration: config.workspaceGrantGeneration,
	}, nil
}

type workspaceCompletionStore interface {
	reconcile.InvocationWorkspaceQuiescenceStore
	reconcile.InvocationWorkspaceExportStore
}
type workspaceCompletionObjects interface {
	artifact.ObjectReader
	artifact.ObjectWriter
}

func (config workerConfig) workspaceCompletion(store workspaceCompletionStore, source workspace.SourceObjectReader, objects workspaceCompletionObjects, plan execution.ExecutionPlan) (*reconcile.InvocationWorkspaceCompletion, error) {
	if !config.exportEnabled() {
		return nil, execution.ErrWorkspaceSourceProvisioningUnavailable
	}
	configuration, err := config.workspaceConfiguration(plan)
	if err != nil {
		return nil, err
	}
	exporter, err := reconcile.NewInvocationWorkspaceExporter(store, source, objects, objects, configuration)
	if err != nil {
		return nil, err
	}
	return reconcile.NewInvocationWorkspaceCompletion(store, exporter)
}

func (provider *certifiedExecutionProvider) StartOwnedRuntime(ctx context.Context, ref execution.ExecutionRef) (*nativehost.Session, error) {
	if provider == nil || !provider.workspaceExport {
		return nil, ErrRuntimeCertificationUnavailable
	}
	if err := provider.check(ctx, ref.IsolationDomainID); err != nil {
		return nil, err
	}
	transport, ok := provider.ExecutionProvider.(interface {
		StartOwnedRuntime(context.Context, execution.ExecutionRef) (*nativehost.Session, error)
	})
	if !ok {
		return nil, ErrRuntimeCertificationUnavailable
	}
	return transport.StartOwnedRuntime(ctx, ref)
}

func (provider *certifiedExecutionProvider) ExportWorkspaceChanges(ctx context.Context, ref execution.ExecutionRef, base *workspace.Snapshot) (*workspace.ChangeSet, error) {
	if provider == nil || !provider.workspaceExport {
		return nil, ErrRuntimeCertificationUnavailable
	}
	if err := provider.check(ctx, ref.IsolationDomainID); err != nil {
		return nil, err
	}
	transport, ok := provider.ExecutionProvider.(reconcile.InvocationWorkspaceChangeProvider)
	if !ok {
		return nil, ErrRuntimeCertificationUnavailable
	}
	return transport.ExportWorkspaceChanges(ctx, ref, base)
}

func (provider *certifiedExecutionProvider) MaterializeWorkspace(ctx context.Context, ref execution.ExecutionRef, snapshot *workspace.Snapshot) (workspace.Receipt, error) {
	if err := provider.check(ctx, ref.IsolationDomainID); err != nil {
		return workspace.Receipt{}, err
	}
	transport, ok := provider.ExecutionProvider.(interface {
		MaterializeWorkspace(context.Context, execution.ExecutionRef, *workspace.Snapshot) (workspace.Receipt, error)
	})
	if !ok {
		return workspace.Receipt{}, execution.ErrWorkspaceSourceProvisioningUnavailable
	}
	return transport.MaterializeWorkspace(ctx, ref, snapshot)
}
