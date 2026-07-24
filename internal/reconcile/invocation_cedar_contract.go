package reconcile

import (
	"errors"
	"slices"
)

const InvocationCedarContract = "dataground.invocation-authorization-cedar/v1"

const (
	invocationCedarPrincipalType = "DataGround::Actor"
	invocationCedarResourceType  = "DataGround::Invocation"
	invocationCedarActionType    = "DataGround::Action"
)

type InvocationCedarEntityUID struct {
	Type string
	ID   string
}

type InvocationCedarRuntimeContext struct {
	ApprovalMode    string
	SandboxMode     string
	HasOutputSchema bool
	ArtifactCount   int
	ArtifactKinds   []string
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
	return input, nil
}

func cloneInvocationCedarInput(input InvocationCedarInput) (InvocationCedarInput, error) {
	if input.Contract != InvocationCedarContract ||
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
		return InvocationCedarInput{}, errors.New("invocation Cedar input is invalid")
	}
	cloned := input
	if input.Runtime != nil {
		runtimeContext := *input.Runtime
		runtimeContext.ArtifactKinds = append([]string(nil), input.Runtime.ArtifactKinds...)
		cloned.Runtime = &runtimeContext
	}
	return cloned, nil
}
