package domain

import (
	"encoding/json"
	"time"
)

type ResourceMetadata struct {
	ID                string            `json:"id"`
	IsolationDomainID string            `json:"isolationDomainId"`
	Generation        int               `json:"generation"`
	Version           int               `json:"version"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	CreatedBy         string            `json:"createdBy"`
	Labels            map[string]string `json:"labels,omitempty"`
}

type AgentService struct {
	Metadata    ResourceMetadata `json:"metadata"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
}

type ServiceRevision struct {
	Metadata             ResourceMetadata `json:"metadata"`
	ServiceID            string           `json:"serviceId"`
	RevisionNumber       int              `json:"revisionNumber"`
	State                string           `json:"state"`
	RuntimeProfile       string           `json:"runtimeProfile"`
	RequiredCapabilities []string         `json:"requiredCapabilities"`
	InputSchema          map[string]any   `json:"inputSchema,omitempty"`
	OutputSchema         map[string]any   `json:"outputSchema,omitempty"`
	PublishedAt          *time.Time       `json:"publishedAt,omitempty"`
}

type ServiceAlias struct {
	WithdrawnAt *time.Time       `json:"withdrawnAt,omitempty"`
	Metadata    ResourceMetadata `json:"metadata"`
	ServiceID   string           `json:"serviceId"`
	Name        string           `json:"name"`
	RevisionID  string           `json:"revisionId"`
}

type OperationLease struct {
	Owner        string    `json:"owner"`
	FencingToken int64     `json:"fencingToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type Operation struct {
	Metadata            ResourceMetadata `json:"metadata"`
	Kind                string           `json:"kind"`
	Command             string           `json:"command"`
	ResourceID          string           `json:"resourceId,omitempty"`
	DesiredState        string           `json:"desiredState"`
	ObservedState       string           `json:"observedState"`
	StateMachineVersion int              `json:"stateMachineVersion"`
	Attempt             int              `json:"attempt"`
	CorrelationID       string           `json:"correlationId"`
	Lease               *OperationLease  `json:"lease,omitempty"`
	DueAt               *time.Time       `json:"dueAt,omitempty"`
	DeadlineAt          *time.Time       `json:"deadlineAt,omitempty"`
	ErrorClassification string           `json:"errorClassification,omitempty"`
	TerminalResult      map[string]any   `json:"terminalResult,omitempty"`
	Error               *APIError        `json:"error,omitempty"`
}

type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	TotalTokens  int `json:"totalTokens"`
}

type Invocation struct {
	Metadata      ResourceMetadata `json:"metadata"`
	ServiceID     string           `json:"serviceId"`
	RevisionID    string           `json:"revisionId"`
	Alias         string           `json:"alias"`
	State         string           `json:"state"`
	Input         map[string]any   `json:"input"`
	Result        map[string]any   `json:"result,omitempty"`
	Error         *APIError        `json:"error,omitempty"`
	Usage         *Usage           `json:"usage,omitempty"`
	CorrelationID string           `json:"correlationId"`
	OperationID   string           `json:"operationId"`
	ArtifactIDs   []string         `json:"artifactIds"`
	CompletedAt   *time.Time       `json:"completedAt,omitempty"`
}

// InvocationSummary supports history discovery without disclosing invocation
// inputs, results, runtime errors, or artifact contents. Those remain separately
// authorized reads against the invocation and artifact resources.
type InvocationSummary struct {
	Metadata      ResourceMetadata `json:"metadata"`
	ServiceID     string           `json:"serviceId"`
	RevisionID    string           `json:"revisionId"`
	Alias         string           `json:"alias"`
	State         string           `json:"state"`
	CorrelationID string           `json:"correlationId"`
	OperationID   string           `json:"operationId"`
	CompletedAt   *time.Time       `json:"completedAt,omitempty"`
}

type EventEnvelope struct {
	SchemaVersion     string                     `json:"schemaVersion"`
	ID                string                     `json:"id"`
	IsolationDomainID string                     `json:"isolationDomainId"`
	InvocationID      string                     `json:"invocationId"`
	Sequence          uint64                     `json:"sequence"`
	Type              string                     `json:"type"`
	OccurredAt        time.Time                  `json:"occurredAt"`
	RecordedAt        time.Time                  `json:"recordedAt"`
	CorrelationID     string                     `json:"correlationId"`
	ActorID           string                     `json:"actorId"`
	ServiceID         string                     `json:"serviceId"`
	RevisionID        string                     `json:"revisionId"`
	Payload           map[string]any             `json:"payload"`
	Extensions        map[string]json.RawMessage `json:"extensions,omitempty"`
}

type ArtifactDescriptor struct {
	Metadata     ResourceMetadata `json:"metadata"`
	InvocationID string           `json:"invocationId"`
	Name         string           `json:"name"`
	Kind         string           `json:"kind"`
	MediaType    string           `json:"mediaType"`
	SizeBytes    int64            `json:"sizeBytes"`
	Digest       string           `json:"digest"`
	State        string           `json:"state"`
	Sensitive    bool             `json:"sensitive"`
}

type APIError struct {
	Code          string       `json:"code"`
	Message       string       `json:"message"`
	CorrelationID string       `json:"correlationId"`
	Retryable     bool         `json:"retryable"`
	FieldErrors   []FieldError `json:"fieldErrors,omitempty"`
}

type FieldError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ErrorEnvelope struct {
	Error APIError `json:"error"`
}
