package domain

import "time"

const InvocationApprovalSchemaV2 = "dataground.invocation-approval/v2"
const InvocationApprovalSchemaV1 = "dataground.invocation-approval/v1"

// InvocationApproval is the public, provider-neutral view of one durable
// runtime approval. Native adapter identifiers and execution bookkeeping stay
// behind the runtime boundary.
type InvocationApproval struct {
	ExpiresAt         *time.Time `json:"expiresAt,omitempty"`
	ClosedAt          *time.Time `json:"closedAt,omitempty"`
	CloseReason       string     `json:"closeReason,omitempty"`
	SchemaVersion     string     `json:"schemaVersion"`
	ID                string     `json:"id"`
	IsolationDomainID string     `json:"isolationDomainId"`
	InvocationID      string     `json:"invocationId"`
	RequestedAction   string     `json:"requestedAction"`
	State             string     `json:"state"`
	Version           int64      `json:"version"`
	Decision          string     `json:"decision,omitempty"`
	ResolvedBy        string     `json:"resolvedBy,omitempty"`
	ResolvedAt        *time.Time `json:"resolvedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}
