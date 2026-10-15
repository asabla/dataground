package main

import (
	"context"
	"regexp"
	"strconv"

	"github.com/asabla/dataground/internal/execution"
	"github.com/asabla/dataground/internal/reconcile"
	"github.com/asabla/dataground/internal/workspace"
)

var workspacePlanDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

const workspacePlanEnvironment = "DATAGROUND_WORKSPACE_PLAN_DIGEST"
const workspaceGrantEnvironment = "DATAGROUND_WORKSPACE_SOURCE_GRANT_GENERATION"

func (config workerConfig) sourceEnabled() bool {
	return config.localAcceptance != nil && config.localAcceptance.workspaceSource
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
	if store.acceptanceProfile != workspaceLocalRuntimeProfile {
		return store.workspacePlanDigest == "" && validGovernedDevelopmentPlan(plan, store.candidateImage, store.acceptanceProfile)
	}
	if !workspacePlanDigestPattern.MatchString(store.workspacePlanDigest) || !validPublicationDevelopmentPlan(plan, localRuntimeAcceptanceConfig{workspaceSource: true, image: store.candidateImage}) {
		return false
	}
	digest, err := execution.DigestExecutionPlan(plan)
	return err == nil && digest == store.workspacePlanDigest
}

func (config workerConfig) acceptsInvocationPolicy(contract string) bool {
	if config.sourceEnabled() {
		return contract == reconcile.InvocationAuthorizationPolicySourceContract
	}
	return contract == reconcile.InvocationAuthorizationPolicyApprovalContract || contract == reconcile.InvocationAuthorizationPolicyPublicationContract
}

func (config workerConfig) workspacePreparer(store reconcile.InvocationWorkspaceStore, objects workspace.SourceObjectReader, plan execution.ExecutionPlan) (*reconcile.InvocationWorkspacePreparer, error) {
	target := config.runtimeTarget()
	if !config.sourceEnabled() || config.workspaceGrantGeneration < 1 || plan.IsolationDomainID != target.isolationDomainID || plan.RevisionID != target.revisionID || !(governedExecutionPlanStore{candidateImage: config.localAcceptance.image, acceptanceProfile: workspaceLocalRuntimeProfile, workspacePlanDigest: config.workspacePlanDigest}).validPlan(plan) {
		return nil, execution.ErrWorkspaceSourceProvisioningUnavailable
	}
	return reconcile.NewInvocationWorkspacePreparer(store, objects, reconcile.InvocationWorkspaceConfig{
		Scope:  reconcile.InvocationAuthorizationPolicyScope{IsolationDomainID: target.isolationDomainID, ServiceID: target.serviceID, RevisionID: target.revisionID},
		Source: *plan.WorkspaceSource, PlanDigest: config.workspacePlanDigest, GrantGeneration: config.workspaceGrantGeneration,
	})
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
