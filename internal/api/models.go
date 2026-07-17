package api

import "github.com/asabla/dataground/internal/domain"

type ResourceMetadata = domain.ResourceMetadata
type AgentService = domain.AgentService
type ServiceRevision = domain.ServiceRevision
type ServiceAlias = domain.ServiceAlias
type Operation = domain.Operation
type Usage = domain.Usage
type Invocation = domain.Invocation
type EventEnvelope = domain.EventEnvelope
type ArtifactDescriptor = domain.ArtifactDescriptor
type APIError = domain.APIError
type FieldError = domain.FieldError
type ErrorEnvelope = domain.ErrorEnvelope

type createAgentServiceRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type createServiceRevisionRequest struct {
	RuntimeProfile       string         `json:"runtimeProfile"`
	RequiredCapabilities []string       `json:"requiredCapabilities"`
	InputSchema          map[string]any `json:"inputSchema,omitempty"`
	OutputSchema         map[string]any `json:"outputSchema,omitempty"`
}

type publishServiceRevisionRequest struct {
	ExpectedVersion int `json:"expectedVersion"`
}

type assignServiceAliasRequest struct {
	RevisionID      string `json:"revisionId"`
	ExpectedVersion *int   `json:"expectedVersion,omitempty"`
}

type invokeAgentServiceRequest struct {
	Alias string         `json:"alias"`
	Input map[string]any `json:"input"`
}

type cancelInvocationRequest struct {
	Reason string `json:"reason,omitempty"`
}
