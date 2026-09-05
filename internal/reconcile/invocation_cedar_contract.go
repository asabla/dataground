package reconcile

import (
	"errors"
	"regexp"
	"slices"
	"time"

	dgruntime "github.com/asabla/dataground/internal/runtime"
)

var approvalIDPattern = regexp.MustCompile(`^apr_[0-9a-z]{20,32}$`)

const InvocationCedarContract = "dataground.invocation-authorization-cedar/v1"
const InvocationCedarQuestionContract = "dataground.invocation-authorization-cedar/v2"

const (
	invocationCedarPrincipalType = "DataGround::Actor"
	invocationCedarResourceType  = "DataGround::Invocation"
	invocationCedarActionType    = "DataGround::Action"
)

type InvocationCedarEntityUID struct {
	Type string
	ID   string
}

type InvocationApprovalAuthorizationContext struct {
	ID              string
	RequestedAction string
	Decision        string
	Phase           string
}

func (value InvocationApprovalAuthorizationContext) Valid() bool {
	return approvalIDPattern.MatchString(value.ID) &&
		(value.RequestedAction == "process.execute" || value.RequestedAction == "workspace.change") &&
		(value.Decision == "approve" || value.Decision == "deny") &&
		(value.Phase == InvocationApprovalPhaseEntry || value.Phase == InvocationApprovalPhaseEffect)
}

type InvocationCedarRuntimeContext struct {
	ApprovalMode          string
	SandboxMode           string
	HasOutputSchema       bool
	ArtifactCount         int
	ArtifactKinds         []string
	QuestionTimeoutMillis int64
}

type InvocationCedarInput struct {
	Contract          string
	Principal         InvocationCedarEntityUID
	Action            InvocationCedarEntityUID
	Resource          InvocationCedarEntityUID
	IsolationDomainID string
	OperationID       string
	ServiceID         string
	RevisionID        string
	CorrelationID     string
	Runtime           *InvocationCedarRuntimeContext
	Approval          *InvocationApprovalAuthorizationContext
	Question          *InvocationQuestionAuthorizationContext
}

func mapInvocationCedarInput(request InvocationAuthorizationRequest) (InvocationCedarInput, error) {
	if !validInvocationAuthorizationRequest(request) {
		return InvocationCedarInput{}, ErrInvocationAuthorizationInvalid
	}
	input := InvocationCedarInput{
		Contract: InvocationCedarContract,
		Principal: InvocationCedarEntityUID{
			Type: invocationCedarPrincipalType,
			ID:   request.ActorID,
		},
		Action: InvocationCedarEntityUID{
			Type: invocationCedarActionType,
			ID:   string(request.Action),
		},
		Resource: InvocationCedarEntityUID{
			Type: invocationCedarResourceType,
			ID:   request.InvocationID,
		},
		IsolationDomainID: request.IsolationDomainID,
		OperationID:       request.OperationID,
		ServiceID:         request.ServiceID,
		RevisionID:        request.RevisionID,
		CorrelationID:     request.CorrelationID,
	}
	if request.Question != nil {
		question := *request.Question
		input.Question = &question
		input.Contract = InvocationCedarQuestionContract
		return input, nil
	}
	if request.Approval != nil {
		approval := *request.Approval
		input.Approval = &approval
		return input, nil
	}
	if request.Runtime == nil {
		return input, nil
	}
	kinds := make([]string, 0, len(request.Runtime.Artifacts))
	for _, artifact := range request.Runtime.Artifacts {
		kinds = append(kinds, artifact.Kind)
	}
	slices.Sort(kinds)
	kinds = slices.Compact(kinds)
	input.Runtime = &InvocationCedarRuntimeContext{
		ApprovalMode:    string(request.Runtime.ApprovalMode),
		SandboxMode:     string(request.Runtime.SandboxMode),
		HasOutputSchema: request.Runtime.OutputSchema != nil,
		ArtifactCount:   len(request.Runtime.Artifacts),
		ArtifactKinds:   kinds,
	}
	if request.Runtime.QuestionMode == dgruntime.QuestionInteractive {
		input.Contract = InvocationCedarQuestionContract
		input.Runtime.QuestionTimeoutMillis = request.Runtime.QuestionTimeout.Milliseconds()
	}
	return input, nil
}

func cloneInvocationCedarInput(input InvocationCedarInput) (InvocationCedarInput, error) {
	if !validInvocationCedarInput(input) {
		return InvocationCedarInput{}, errors.New("invocation Cedar input is invalid")
	}
	cloned := input
	if input.Runtime != nil {
		runtimeContext := *input.Runtime
		runtimeContext.ArtifactKinds = append([]string(nil), input.Runtime.ArtifactKinds...)
		cloned.Runtime = &runtimeContext
	}
	if input.Approval != nil {
		approval := *input.Approval
		cloned.Approval = &approval
	}
	if input.Question != nil {
		question := *input.Question
		cloned.Question = &question
	}
	return cloned, nil
}

func validInvocationCedarInput(input InvocationCedarInput) bool {
	if (input.Contract != InvocationCedarContract && input.Contract != InvocationCedarQuestionContract) ||
		input.Principal.Type != invocationCedarPrincipalType ||
		input.Principal.ID == "" ||
		input.Action.Type != invocationCedarActionType ||
		input.Resource.Type != invocationCedarResourceType ||
		input.Resource.ID == "" ||
		input.IsolationDomainID == "" ||
		input.OperationID == "" ||
		input.ServiceID == "" ||
		input.RevisionID == "" ||
		input.CorrelationID == "" {
		return false
	}
	switch input.Action.ID {
	case string(InvocationAuthorizationAdmit), string(InvocationAuthorizationCancel):
		return input.Contract == InvocationCedarContract && input.Runtime == nil && input.Approval == nil && input.Question == nil
	case string(InvocationAuthorizationRun):
		if input.Runtime == nil || input.Approval != nil || input.Question != nil ||
			input.Runtime.ApprovalMode == "" ||
			input.Runtime.SandboxMode == "" ||
			input.Runtime.ArtifactCount < 0 ||
			input.Runtime.ArtifactCount < len(input.Runtime.ArtifactKinds) {
			return false
		}
		if input.Contract == InvocationCedarContract && input.Runtime.QuestionTimeoutMillis != 0 {
			return false
		}
		if input.Contract == InvocationCedarQuestionContract && (input.Runtime.QuestionTimeoutMillis <= 0 || input.Runtime.QuestionTimeoutMillis > (15*time.Minute).Milliseconds()) {
			return false
		}
		for index, kind := range input.Runtime.ArtifactKinds {
			if kind == "" || index > 0 && input.Runtime.ArtifactKinds[index-1] >= kind {
				return false
			}
		}
		return true
	case string(InvocationAuthorizationApprove):
		return input.Contract == InvocationCedarContract && input.Runtime == nil && input.Question == nil && input.Approval != nil && input.Approval.Valid()
	case string(InvocationAuthorizationAnswer):
		return input.Contract == InvocationCedarQuestionContract && input.Runtime == nil && input.Approval == nil && input.Question != nil && input.Question.Valid()
	default:
		return false
	}
}
