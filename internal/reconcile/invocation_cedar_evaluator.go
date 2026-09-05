package reconcile

import (
	"bytes"
	"context"
	"errors"

	"github.com/asabla/dataground/internal/authz"
	cedar "github.com/cedar-policy/cedar-go"
)

const invocationCedarSchemaV1 = `{"DataGround":{"entityTypes":{"Actor":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"Invocation":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}}},"actions":{"admit":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true}},"additionalAttributes":false}}},"run":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true},"runtime":{"type":"Record","required":true,"attributes":{"approvalMode":{"type":"String","required":true},"sandboxMode":{"type":"String","required":true},"hasOutputSchema":{"type":"Boolean","required":true},"artifactCount":{"type":"Long","required":true},"artifactKinds":{"type":"Set","element":{"type":"String"},"required":true}},"additionalAttributes":false}},"additionalAttributes":false}}},"cancel":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true}},"additionalAttributes":false}}}}}}`

const invocationCedarSchemaV2 = `{"DataGround":{"entityTypes":{"Role":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"Actor":{"memberOfTypes":["Role"],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"Invocation":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}}},"actions":{"admit":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true}},"additionalAttributes":false}}},"run":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true},"runtime":{"type":"Record","required":true,"attributes":{"approvalMode":{"type":"String","required":true},"sandboxMode":{"type":"String","required":true},"hasOutputSchema":{"type":"Boolean","required":true},"artifactCount":{"type":"Long","required":true},"artifactKinds":{"type":"Set","element":{"type":"String"},"required":true}},"additionalAttributes":false}},"additionalAttributes":false}}},"cancel":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true}},"additionalAttributes":false}}}}}}`

const invocationCedarSchemaV3 = `{"DataGround":{"entityTypes":{"Role":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"Actor":{"memberOfTypes":["Role"],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"Invocation":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}}},"actions":{"admit":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true}},"additionalAttributes":false}}},"run":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true},"runtime":{"type":"Record","required":true,"attributes":{"approvalMode":{"type":"String","required":true},"sandboxMode":{"type":"String","required":true},"hasOutputSchema":{"type":"Boolean","required":true},"artifactCount":{"type":"Long","required":true},"artifactKinds":{"type":"Set","element":{"type":"String"},"required":true}},"additionalAttributes":false}},"additionalAttributes":false}}},"cancel":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true}},"additionalAttributes":false}}},"approve":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true},"approval":{"type":"Record","required":true,"attributes":{"approvalID":{"type":"String","required":true},"requestedAction":{"type":"String","required":true},"decision":{"type":"String","required":true},"phase":{"type":"String","required":true}},"additionalAttributes":false}},"additionalAttributes":false}}}}}}`

const invocationCedarSchemaV4 = `{"DataGround":{"entityTypes":{"Role":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"Actor":{"memberOfTypes":["Role"],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}},"Invocation":{"memberOfTypes":[],"shape":{"type":"Record","attributes":{},"additionalAttributes":false},"tags":{"type":"String"}}},"actions":{"admit":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true}},"additionalAttributes":false}}},"run":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true},"runtime":{"type":"Record","required":true,"attributes":{"approvalMode":{"type":"String","required":true},"sandboxMode":{"type":"String","required":true},"hasOutputSchema":{"type":"Boolean","required":true},"artifactCount":{"type":"Long","required":true},"artifactKinds":{"type":"Set","element":{"type":"String"},"required":true},"questionTimeoutMillis":{"type":"Long","required":false}},"additionalAttributes":false}},"additionalAttributes":false}}},"cancel":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true}},"additionalAttributes":false}}},"approve":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true},"approval":{"type":"Record","required":true,"attributes":{"approvalID":{"type":"String","required":true},"requestedAction":{"type":"String","required":true},"decision":{"type":"String","required":true},"phase":{"type":"String","required":true}},"additionalAttributes":false}},"additionalAttributes":false}}},"answer":{"appliesTo":{"principalTypes":["Actor"],"resourceTypes":["Invocation"],"context":{"type":"Record","attributes":{"isolationDomainID":{"type":"String","required":true},"operationID":{"type":"String","required":true},"serviceID":{"type":"String","required":true},"revisionID":{"type":"String","required":true},"correlationID":{"type":"String","required":true},"question":{"type":"Record","required":true,"attributes":{"questionID":{"type":"String","required":true},"version":{"type":"Long","required":true},"phase":{"type":"String","required":true},"questionCount":{"type":"Long","required":true},"freeTextCount":{"type":"Long","required":true},"selectedOptionCount":{"type":"Long","required":true}},"additionalAttributes":false}},"additionalAttributes":false}}}}}}`

var errInvocationCedarEvaluation = errors.New("invocation Cedar evaluation failed")

type CedarInvocationAuthorizationEvaluator struct{}

func NewCedarInvocationAuthorizationEvaluator() *CedarInvocationAuthorizationEvaluator {
	return &CedarInvocationAuthorizationEvaluator{}
}

func CanonicalInvocationCedarSchema() []byte {
	return []byte(invocationCedarSchemaV1)
}

func CanonicalInvocationCedarEntitySchema() []byte {
	return []byte(invocationCedarSchemaV2)
}

func CanonicalInvocationCedarQuestionSchema() []byte { return []byte(invocationCedarSchemaV4) }

func CanonicalInvocationCedarApprovalSchema() []byte {
	return []byte(invocationCedarSchemaV3)
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
	// A wildcard in an older policy never acquires question authority.
	if input.Contract == InvocationCedarQuestionContract && policy.Contract != InvocationAuthorizationPolicyQuestionContract {
		return ErrInvocationAuthorizationDenied
	}
	entities, err := validatedInvocationCedarEntities(policy, scope)
	if err != nil {
		return errInvocationCedarEvaluation
	}
	request, err := invocationCedarRequest(input)
	if err != nil {
		return errInvocationCedarEvaluation
	}
	decision, diagnostic := cedar.Authorize(policies, entities, request)
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
	if !validInvocationAuthorizationPolicy(policy, scope) {
		return nil, errInvocationCedarEvaluation
	}
	switch policy.Contract {
	case InvocationAuthorizationPolicyContract:
		if !bytes.Equal(policy.Schema, []byte(invocationCedarSchemaV1)) {
			return nil, errInvocationCedarEvaluation
		}
	case InvocationAuthorizationPolicyEntityContract:
		if !bytes.Equal(policy.Schema, []byte(invocationCedarSchemaV2)) {
			return nil, errInvocationCedarEvaluation
		}
	case InvocationAuthorizationPolicyApprovalContract:
		if !bytes.Equal(policy.Schema, []byte(invocationCedarSchemaV3)) {
			return nil, errInvocationCedarEvaluation
		}
	case InvocationAuthorizationPolicyQuestionContract:
		if !bytes.Equal(policy.Schema, []byte(invocationCedarSchemaV4)) {
			return nil, errInvocationCedarEvaluation
		}
	default:
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

func canonicalInvocationCedarEntities(
	encoded []byte,
) ([]byte, cedar.EntityMap, error) {
	entities, err := authz.ParseInvocationCedarEntitySnapshot(encoded)
	if err != nil {
		return nil, nil, errInvocationCedarEvaluation
	}
	return append([]byte(nil), encoded...), entities, nil
}
func validatedInvocationCedarEntities(
	policy InvocationAuthorizationPolicy,
	scope InvocationAuthorizationPolicyScope,
) (cedar.EntityMap, error) {
	if !validInvocationAuthorizationPolicy(policy, scope) {
		return nil, errInvocationCedarEvaluation
	}
	if policy.Contract == InvocationAuthorizationPolicyContract {
		return nil, nil
	}
	canonical, entities, err := canonicalInvocationCedarEntities(policy.Entities)
	if err != nil || !bytes.Equal(canonical, policy.Entities) {
		return nil, errInvocationCedarEvaluation
	}
	return entities, nil
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
	if input.Question != nil {
		contextValues["question"] = cedar.NewRecord(cedar.RecordMap{
			"questionID": cedar.String(input.Question.ID), "version": cedar.Long(input.Question.Version), "phase": cedar.String(input.Question.Phase),
			"questionCount": cedar.Long(input.Question.QuestionCount), "freeTextCount": cedar.Long(input.Question.FreeTextCount), "selectedOptionCount": cedar.Long(input.Question.SelectedOptionCount),
		})
	}
	if input.Approval != nil {
		contextValues["approval"] = cedar.NewRecord(cedar.RecordMap{
			"approvalID":      cedar.String(input.Approval.ID),
			"requestedAction": cedar.String(input.Approval.RequestedAction),
			"decision":        cedar.String(input.Approval.Decision),
			"phase":           cedar.String(input.Approval.Phase),
		})
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
		runtimeValues := cedar.RecordMap{
			"approvalMode":    cedar.String(input.Runtime.ApprovalMode),
			"sandboxMode":     cedar.String(input.Runtime.SandboxMode),
			"hasOutputSchema": hasOutputSchema,
			"artifactCount":   cedar.Long(input.Runtime.ArtifactCount),
			"artifactKinds":   cedar.NewSet(artifactKinds...),
		}
		if input.Runtime.QuestionTimeoutMillis > 0 {
			runtimeValues["questionTimeoutMillis"] = cedar.Long(input.Runtime.QuestionTimeoutMillis)
		}
		contextValues["runtime"] = cedar.NewRecord(runtimeValues)
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
