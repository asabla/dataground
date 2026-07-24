package reconcile

import (
	"bytes"
	"context"
	"errors"

	cedar "github.com/cedar-policy/cedar-go"
)

const invocationCedarSchemaV1 = `{"DataGround":{"entityTypes":{"Actor":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"Invocation":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}}},"actions":{"admit":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true}},"additionalAttributes":false}}},"run":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true},"runtime":{"type":"Record","required":true,"attributes":{"approvalMode":{"type":"String","required":true},"sandboxMode":{"type":"String","required":true},"hasOutputSchema":{"type":"Boolean","required":true},"artifactCount":{"type":"Long","required":true},"artifactKinds":{"type":"Set","element":{"type":"String"},"required":true}},"additionalAttributes":false}},"additionalAttributes":false}}},"cancel":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true}},"additionalAttributes":false}}}}}}`

var errInvocationCedarEvaluation = errors.New("invocation Cedar evaluation failed")

type CedarInvocationAuthorizationEvaluator struct{}

func NewCedarInvocationAuthorizationEvaluator() *CedarInvocationAuthorizationEvaluator {
	return &CedarInvocationAuthorizationEvaluator{}
}

func CanonicalInvocationCedarSchema() []byte {
	return []byte(invocationCedarSchemaV1)
}

func (*CedarInvocationAuthorizationEvaluator) EvaluateInvocationAuthorization(
	ctx context.Context,
	policy InvocationAuthorizationPolicy,
	input InvocationCedarInput,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	input, err := cloneInvocationCedarInput(input)
	if err != nil {
		return errInvocationCedarEvaluation
	}
	scope := InvocationAuthorizationPolicyScope{
		IsolationDomainID: input.IsolationDomainID,
		ServiceID:         input.ServiceID,
		RevisionID:        input.RevisionID,
	}
	policies, err := validatedInvocationCedarPolicySet(policy, scope)
	if err != nil {
		return errInvocationCedarEvaluation
	}
	request, err := invocationCedarRequest(input)
	if err != nil {
		return errInvocationCedarEvaluation
	}
	decision, diagnostic := cedar.Authorize(policies, nil, request)
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(diagnostic.Errors) != 0 {
		return errInvocationCedarEvaluation
	}
	if decision == cedar.Deny {
		return ErrInvocationAuthorizationDenied
	}
	return nil
}

func validatedInvocationCedarPolicySet(
	policy InvocationAuthorizationPolicy,
	scope InvocationAuthorizationPolicyScope,
) (*cedar.PolicySet, error) {
	if !validInvocationAuthorizationPolicy(policy, scope) ||
		!bytes.Equal(policy.Schema, []byte(invocationCedarSchemaV1)) {
		return nil, errInvocationCedarEvaluation
	}
	policies, err := cedar.NewPolicySetFromBytes(policy.PolicySetID+".cedar", policy.Policies)
	if err != nil {
		return nil, errInvocationCedarEvaluation
	}
	for range policies.All() {
		return policies, nil
	}
	return nil, errInvocationCedarEvaluation
}

func invocationCedarRequest(input InvocationCedarInput) (cedar.Request, error) {
	if !validInvocationCedarInput(input) {
		return cedar.Request{}, errInvocationCedarEvaluation
	}
	contextValues := cedar.RecordMap{
		"isolationDomainID": cedar.String(input.IsolationDomainID),
		"operationID":       cedar.String(input.OperationID),
		"serviceID":         cedar.String(input.ServiceID),
		"revisionID":        cedar.String(input.RevisionID),
		"correlationID":     cedar.String(input.CorrelationID),
	}
	if input.Runtime != nil {
		artifactKinds := make([]cedar.Value, 0, len(input.Runtime.ArtifactKinds))
		for _, kind := range input.Runtime.ArtifactKinds {
			artifactKinds = append(artifactKinds, cedar.String(kind))
		}
		hasOutputSchema := cedar.False
		if input.Runtime.HasOutputSchema {
			hasOutputSchema = cedar.True
		}
		contextValues["runtime"] = cedar.NewRecord(cedar.RecordMap{
			"approvalMode":    cedar.String(input.Runtime.ApprovalMode),
			"sandboxMode":     cedar.String(input.Runtime.SandboxMode),
			"hasOutputSchema": hasOutputSchema,
			"artifactCount":   cedar.Long(input.Runtime.ArtifactCount),
			"artifactKinds":   cedar.NewSet(artifactKinds...),
		})
	}
	return cedar.Request{
		Principal: cedar.NewEntityUID(
			cedar.EntityType(input.Principal.Type),
			cedar.String(input.Principal.ID),
		),
		Action: cedar.NewEntityUID(
			cedar.EntityType(input.Action.Type),
			cedar.String(input.Action.ID),
		),
		Resource: cedar.NewEntityUID(
			cedar.EntityType(input.Resource.Type),
			cedar.String(input.Resource.ID),
		),
		Context: cedar.NewRecord(contextValues),
	}, nil
}

var _ InvocationCedarEvaluator = (*CedarInvocationAuthorizationEvaluator)(nil)
