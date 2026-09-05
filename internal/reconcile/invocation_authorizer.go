package reconcile

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/asabla/dataground/internal/persistence"
	dgruntime "github.com/asabla/dataground/internal/runtime"
)

type InvocationAuthorizationAction string

const (
	InvocationAuthorizationAdmit   InvocationAuthorizationAction = "admit"
	InvocationAuthorizationRun     InvocationAuthorizationAction = "run"
	InvocationAuthorizationCancel  InvocationAuthorizationAction = "cancel"
	InvocationAuthorizationApprove InvocationAuthorizationAction = "approve"
	InvocationAuthorizationAnswer  InvocationAuthorizationAction = "answer"
)

var (
	ErrInvocationAuthorizationDenied  = errors.New("invocation authorization denied")
	ErrInvocationAuthorizationInvalid = errors.New("invocation authorization request is invalid")
)

type InvocationAuthorizationRequest struct {
	Action            InvocationAuthorizationAction
	IsolationDomainID string
	OperationID       string
	InvocationID      string
	ServiceID         string
	RevisionID        string
	ActorID           string
	CorrelationID     string
	Runtime           *dgruntime.StartRequest
	Approval          *InvocationApprovalAuthorizationContext
	Question          *InvocationQuestionAuthorizationContext
}

type InvocationAuthorizationDecision interface {
	AuthorizeInvocationEffect(context.Context, InvocationAuthorizationRequest) error
}

// InvocationAuthorizer maps all governed invocation phases onto one closed,
// provider-independent decision contract. A policy implementation remains an
// explicit deployment dependency.
type InvocationAuthorizer struct {
	decision InvocationAuthorizationDecision
}

func NewInvocationAuthorizer(decision InvocationAuthorizationDecision) (*InvocationAuthorizer, error) {
	if governedInvocationDependencyMissing(decision) {
		return nil, errors.New("invocation authorization decision is required")
	}
	return &InvocationAuthorizer{decision: decision}, nil
}

func (authorizer *InvocationAuthorizer) AuthorizeInvocationAdmission(
	ctx context.Context,
	target persistence.InvocationAdmissionTarget,
) error {
	request := invocationAuthorizationRequest(
		InvocationAuthorizationAdmit,
		target.IsolationDomainID,
		target.OperationID,
		target.InvocationID,
		target.ServiceID,
		target.RevisionID,
		target.ActorID,
		target.CorrelationID,
	)
	return authorizer.authorize(ctx, request, ErrInvocationAdmissionDenied)
}

func (authorizer *InvocationAuthorizer) AuthorizeInvocationRuntime(
	ctx context.Context,
	target persistence.InvocationRuntimeTarget,
	runtimeRequest dgruntime.StartRequest,
) error {
	request := invocationAuthorizationRequest(
		InvocationAuthorizationRun,
		target.IsolationDomainID,
		target.OperationID,
		target.InvocationID,
		target.ServiceID,
		target.RevisionID,
		target.ActorID,
		target.CorrelationID,
	)
	cloned, err := cloneInvocationAuthorizationRuntime(runtimeRequest)
	if err != nil {
		return err
	}
	request.Runtime = &cloned
	return authorizer.authorize(ctx, request, ErrInvocationRuntimeDenied)
}

func (authorizer *InvocationAuthorizer) AuthorizeInvocationApproval(
	ctx context.Context,
	approval persistence.InvocationRuntimeApproval,
	phase string,
) error {
	request := invocationAuthorizationRequest(
		InvocationAuthorizationApprove,
		approval.IsolationDomainID,
		approval.OperationID,
		approval.InvocationID,
		approval.ServiceID,
		approval.RevisionID,
		approval.ResolvedBy,
		approval.ResolutionCorrelationID,
	)
	request.Approval = &InvocationApprovalAuthorizationContext{
		ID: approval.ID, RequestedAction: approval.RequestedAction,
		Decision: approval.Decision, Phase: phase,
	}
	return authorizer.authorize(ctx, request, ErrInvocationApprovalDenied)
}

func (authorizer *InvocationAuthorizer) AuthorizeInvocationCancellation(
	ctx context.Context,
	target persistence.InvocationCancellationTarget,
) error {
	request := invocationAuthorizationRequest(
		InvocationAuthorizationCancel,
		target.IsolationDomainID,
		target.OperationID,
		target.InvocationID,
		target.ServiceID,
		target.RevisionID,
		target.ActorID,
		target.CorrelationID,
	)
	return authorizer.authorize(ctx, request, ErrInvocationCancellationDenied)
}

func invocationAuthorizationRequest(
	action InvocationAuthorizationAction,
	isolationDomainID string,
	operationID string,
	invocationID string,
	serviceID string,
	revisionID string,
	actorID string,
	correlationID string,
) InvocationAuthorizationRequest {
	return InvocationAuthorizationRequest{
		Action:            action,
		IsolationDomainID: isolationDomainID,
		OperationID:       operationID,
		InvocationID:      invocationID,
		ServiceID:         serviceID,
		RevisionID:        revisionID,
		ActorID:           actorID,
		CorrelationID:     correlationID,
	}
}

func (authorizer *InvocationAuthorizer) authorize(
	ctx context.Context,
	request InvocationAuthorizationRequest,
	phaseDenied error,
) error {
	if !validInvocationAuthorizationRequest(request) {
		return ErrInvocationAuthorizationInvalid
	}
	if err := authorizer.decision.AuthorizeInvocationEffect(ctx, request); err != nil {
		if errors.Is(err, ErrInvocationAuthorizationDenied) {
			return errors.Join(phaseDenied, err)
		}
		return err
	}
	return nil
}

func validInvocationAuthorizationRequest(request InvocationAuthorizationRequest) bool {
	switch request.Action {
	case InvocationAuthorizationAdmit, InvocationAuthorizationCancel:
		if request.Runtime != nil || request.Approval != nil || request.Question != nil {
			return false
		}
	case InvocationAuthorizationRun:
		if request.Runtime == nil || request.Approval != nil || request.Question != nil || !validInvocationRuntimeQuestionMode(*request.Runtime) {
			return false
		}
	case InvocationAuthorizationApprove:
		if request.Runtime != nil || request.Question != nil || request.Approval == nil ||
			!request.Approval.Valid() {
			return false
		}
	case InvocationAuthorizationAnswer:
		if request.Runtime != nil || request.Approval != nil || request.Question == nil || !request.Question.Valid() {
			return false
		}
	default:
		return false
	}
	return request.IsolationDomainID != "" &&
		request.OperationID != "" &&
		request.InvocationID != "" &&
		request.ServiceID != "" &&
		request.RevisionID != "" &&
		request.ActorID != "" &&
		request.CorrelationID != ""
}

func cloneInvocationAuthorizationRuntime(request dgruntime.StartRequest) (dgruntime.StartRequest, error) {
	cloned := request
	cloned.Artifacts = append([]dgruntime.ArtifactDeclaration(nil), request.Artifacts...)
	if request.OutputSchema == nil {
		return cloned, nil
	}
	encoded, err := json.Marshal(request.OutputSchema)
	if err != nil {
		return dgruntime.StartRequest{}, ErrInvocationAuthorizationInvalid
	}
	cloned.OutputSchema = nil
	if err := json.Unmarshal(encoded, &cloned.OutputSchema); err != nil {
		return dgruntime.StartRequest{}, ErrInvocationAuthorizationInvalid
	}
	return cloned, nil
}

var (
	_ InvocationAdmissionAuthorizer    = (*InvocationAuthorizer)(nil)
	_ InvocationRuntimeAuthorizer      = (*InvocationAuthorizer)(nil)
	_ InvocationCancellationAuthorizer = (*InvocationAuthorizer)(nil)
)
