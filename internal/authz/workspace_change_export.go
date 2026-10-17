package authz

import (
	"regexp"

	"github.com/asabla/dataground/internal/identity"
	"github.com/asabla/dataground/internal/workspace"
)

const WorkspaceChangeExportDecisionContract = "dataground.workspace-change-export-authorization/v1"

var workspaceExportExecutionPattern = regexp.MustCompile(`^exe_[0-9a-z]{20,32}$`)

// WorkspaceChangeExportRequest carries durable scope and bounded metadata only.
// Capture has no result receipt; retention must bind the exact verified result.
type WorkspaceChangeExportRequest struct {
	Source        workspace.SourceRecord     `json:"source"`
	ServiceID     string                     `json:"serviceId"`
	RevisionID    string                     `json:"revisionId"`
	InvocationID  string                     `json:"invocationId"`
	OperationID   string                     `json:"operationId"`
	EffectID      string                     `json:"effectId"`
	ExecutionID   string                     `json:"executionId"`
	ActorID       string                     `json:"actorId"`
	CorrelationID string                     `json:"correlationId"`
	PlanDigest    string                     `json:"planDigest"`
	OwnerBinding  string                     `json:"ownerBinding"`
	RuntimeToken  int64                      `json:"runtimeToken"`
	FencingToken  int64                      `json:"fencingToken"`
	Phase         string                     `json:"phase"`
	Changes       workspace.ChangeSetReceipt `json:"changes"`
}

func (r WorkspaceChangeExportRequest) Valid() bool {
	if !workspace.ValidSourceInstallation(workspace.SourceInstallation{Record: r.Source, ActorID: r.ActorID, CorrelationID: r.CorrelationID}) || !invocationAuditServiceIDPattern.MatchString(r.ServiceID) || !invocationAuditRevisionIDPattern.MatchString(r.RevisionID) || !invocationAuditInvocationIDPattern.MatchString(r.InvocationID) || !invocationAuditOperationIDPattern.MatchString(r.OperationID) || !workspaceExportExecutionPattern.MatchString(r.ExecutionID) || !policyDigestPattern.MatchString(r.PlanDigest) || !policyDigestPattern.MatchString(r.OwnerBinding) || r.RuntimeToken < 1 || r.FencingToken < 1 || r.EffectID != identity.Derived("eff", r.Source.IsolationDomainID+":invocation-execution:"+r.OperationID+":run-invocation") {
		return false
	}
	if r.Phase == "capture" {
		return r.Changes == (workspace.ChangeSetReceipt{})
	}
	return r.Phase == "retain" && workspace.ValidChangeExportRecord(workspace.ChangeExportRecord{SchemaVersion: workspace.ChangeExportContract, Source: r.Source, InvocationID: r.InvocationID, OperationID: r.OperationID, EffectID: r.EffectID, ExecutionID: r.ExecutionID, PlanDigest: r.PlanDigest, Changes: r.Changes})
}
