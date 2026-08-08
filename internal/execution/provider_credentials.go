package execution

import (
	"context"
	"errors"
)

const (
	ProviderCredentialPurposeAgentInference = "agent-inference"
	ProviderCredentialPhaseAdmission        = "admission"
	ProviderCredentialPhaseEffect           = "effect"
)

var ErrProviderCredentialUseDenied = errors.New("execution provider credential use denied")

type ProviderCredentialUse struct {
	IsolationDomainID string
	RevisionID        string
	OperationID       string
	ProviderProfile   string
	Purpose           string
	Phase             string
	ActorID           string
	CorrelationID     string
}

// ProviderCredentialUseAuthorizer is a secret-free effect boundary. It receives
// only the immutable OpenShell profile identity and durable invocation scope;
// provider credential bytes remain owned by OpenShell.
type ProviderCredentialUseAuthorizer interface {
	AuthorizeProviderCredentialUse(context.Context, ProviderCredentialUse) error
}
